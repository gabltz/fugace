// fugace web client: IRC over WebSocket (text.ircv3.net), vanilla JS.
// Rendering uses textContent and DOM nodes only, never HTML strings.
'use strict';
(() => {
  const $ = (id) => document.getElementById(id);
  const NICK_RE = /^[A-Za-z[\]\\`_^{|}][A-Za-z0-9[\]\\`_^{|}-]{0,29}$/;
  const CHAN_RE = /^#[\x21-\x2b\x2d-\x7e]{1,31}$/;
  const URL_RE = /\bhttps?:\/\/[^\s<>"'`]+/g;
  const FORMAT_RE = /\x03(\d{1,2}(,\d{1,2})?)?|[\x02\x0f\x11\x16\x1d\x1e\x1f]/g;
  const SERVER = '*server*';
  const MAX_LINES = 2000; // per buffer, in the DOM and in memory
  const CHUNK_BYTES = 350; // text bytes per PRIVMSG, well under 512 with the prefix
  const BACKOFF = [1, 2, 5, 10, 30];
  const STORAGE_KEY = 'fugace.nick';
  const KEY_STORAGE = 'fugace.key'; // identity secret: lets you delete your own messages later
  const AVATAR_STORAGE = 'fugace.avatar';
  const WANTED_CAPS = ['server-time', 'echo-message', 'message-tags', 'draft/message-redaction', 'fugace/avatar'];
  const AVATAR_PX = 256; // resized in the browser before upload
  const CONFIRM_MS = 3500;
  const UPLOAD_TOKEN = 'fugace/UPLOAD=';
  // Only our own re-encoded images get an inline preview (anti-tracking).
  const PREVIEW_RE = /^\/f\/[A-Za-z0-9_-]{22}\/[^/]+\.(jpg|png|gif)$/;
  const LIVE_MS = 5000; // lines younger than this are animated
  const GROUP_MS = 5 * 60 * 1000; // consecutive lines of one author group together
  const SPARKS = 18;
  const MAGNET_RADIUS = 70; // px around a magnetic button
  const MAGNET_MAX = 6; // px of displacement at most

  const st = {
    ws: null, nick: '', wantNick: '', serverPass: '', adminPass: '',
    registered: false, everRegistered: false, admin: false, closing: false,
    retry: 0, timer: null, buffers: new Map(), active: SERVER,
    channels: new Set(['#general']), focusNext: '#general', uploadToken: '', shownMembers: new Map(),
    avatars: new Map(), // folded nick -> picture version
    clearing: false,
  };

  const fold = (s) => s.replace(/[A-Z]/g, (c) => c.toLowerCase());

  function stored(k) { try { return localStorage.getItem(k); } catch { return null; } }
  function store(k, v) { try { if (v === null) localStorage.removeItem(k); else localStorage.setItem(k, v); } catch { /* storage disabled */ } }

  // A random secret kept in this browser. The server only keeps a keyed
  // hash of it, which makes your messages yours across reconnections.
  function identityKey() {
    let k = stored(KEY_STORAGE);
    if (!k || !/^[A-Za-z0-9_-]{32,128}$/.test(k)) {
      const b = crypto.getRandomValues(new Uint8Array(32));
      k = btoa(String.fromCharCode(...b)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
      store(KEY_STORAGE, k);
    }
    return k;
  }
  const nickOf = (src) => src.split('!')[0];
  const clean = (s) => s.replace(/[\r\n\0]/g, ' ');

  // --- IRC line parsing -------------------------------------------------
  function unescapeTag(v) {
    const map = { ':': ';', s: ' ', r: '\r', n: '\n', '\\': '\\' };
    return v.replace(/\\(.?)/g, (_, c) => map[c] ?? c);
  }

  function parse(line) {
    const m = { tags: {}, source: '', command: '', params: [] };
    let rest = line;
    if (rest.startsWith('@')) {
      const sp = rest.indexOf(' ');
      if (sp < 0) return null;
      for (const t of rest.slice(1, sp).split(';')) {
        if (!t) continue;
        const eq = t.indexOf('=');
        if (eq < 0) m.tags[t] = '';
        else m.tags[t.slice(0, eq)] = unescapeTag(t.slice(eq + 1));
      }
      rest = rest.slice(sp + 1);
    }
    rest = rest.replace(/^ +/, '');
    if (rest.startsWith(':')) {
      const sp = rest.indexOf(' ');
      if (sp < 0) return null;
      m.source = rest.slice(1, sp);
      rest = rest.slice(sp + 1);
    }
    for (;;) {
      rest = rest.replace(/^ +/, '');
      if (!rest) break;
      if (m.command && rest.startsWith(':')) { m.params.push(rest.slice(1)); break; }
      const sp = rest.indexOf(' ');
      const word = sp < 0 ? rest : rest.slice(0, sp);
      if (m.command) m.params.push(word); else m.command = word.toUpperCase();
      if (sp < 0) break;
      rest = rest.slice(sp + 1);
    }
    return m.command ? m : null;
  }

  // Splits text into chunks of at most n UTF-8 bytes, on code points.
  function chunks(text, n) {
    const enc = new TextEncoder();
    const out = [];
    let cur = '';
    let size = 0;
    for (const ch of text) {
      const len = enc.encode(ch).length;
      if (size + len > n) { out.push(cur); cur = ''; size = 0; }
      cur += ch;
      size += len;
    }
    if (cur) out.push(cur);
    return out;
  }

  // --- buffers ----------------------------------------------------------
  function buffer(name, type) {
    const key = name === SERVER ? SERVER : fold(name);
    let b = st.buffers.get(key);
    if (!b) {
      b = { key, name, type, lines: [], members: new Map(), topic: '', unread: 0, joined: false };
      st.buffers.set(key, b);
      renderBuffers();
    }
    return b;
  }

  function dropBuffer(b) {
    st.buffers.delete(b.key);
    if (st.active === b.key) setActive(firstBuffer());
    renderBuffers();
  }

  function firstBuffer() {
    for (const b of st.buffers.values()) if (b.type === 'channel') return b;
    return buffer(SERVER, 'server');
  }

  function timeOf(tags) {
    const d = tags && tags.time ? new Date(tags.time) : new Date();
    return Number.isNaN(d.getTime()) ? new Date() : d;
  }

  function add(b, kind, text, opts = {}) {
    const l = { kind, text, nick: opts.nick || '', at: opts.at || new Date(), msgid: opts.msgid || '', own: Boolean(opts.own), target: b.name };
    if (opts.nick && st.nick && fold(opts.nick) !== fold(st.nick) && mentions(text)) l.kind += ' mention';
    if (opts.nick && st.nick && fold(opts.nick) === fold(st.nick)) l.mine = true;
    const prev = b.lines[b.lines.length - 1];
    b.lines.push(l);
    if (b.lines.length > MAX_LINES) b.lines.shift();
    if (b.key === st.active) {
      const list = $('messages');
      const atBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 80;
      // Only live lines animate: a replayed history appears at once.
      const live = Date.now() - l.at.getTime() < LIVE_MS;
      for (const node of renderLine(l, prev)) {
        if (live && node.classList.contains('line')) node.classList.add('enter');
        list.append(node);
      }
      while (list.childElementCount > MAX_LINES) list.firstElementChild.remove();
      if (atBottom || l.mine) {
        list.scrollTop = list.scrollHeight; // your own lines always bring you back down
        $('jump').hidden = true;
      } else if (live && opts.nick) {
        $('jump').hidden = false;
      }
    } else if (opts.nick && (kind === 'msg' || kind === 'action')) {
      b.unread++;
      renderBuffers();
    }
  }

  function mentions(text) {
    const n = fold(st.nick);
    const i = fold(text).indexOf(n);
    if (i < 0) return false;
    const isWord = (c) => c && /[A-Za-z0-9[\]\\`_^{|}-]/.test(c);
    return !isWord(text[i - 1]) && !isWord(text[i + n.length]);
  }

  // Server-wide notices go to the active buffer, never lost.
  const info = (text, kind = 'info') => add(current(), kind, text);
  const current = () => st.buffers.get(st.active) || buffer(SERVER, 'server');

  // --- rendering --------------------------------------------------------
  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  }

  function hue(nick) {
    let h = 0;
    for (const c of fold(nick)) h = (h * 31 + c.charCodeAt(0)) >>> 0;
    return h % 8;
  }
  const nickClass = (nick) => 'nick n' + hue(nick);

  function initialAvatar(nick, small) {
    const a = el('span', 'avatar a' + hue(nick) + (small ? ' sm' : ''), (nick.match(/[A-Za-z0-9]/) || [nick[0] || '?'])[0]);
    a.setAttribute('aria-hidden', 'true');
    a.dataset.av = fold(nick);
    if (small) a.dataset.small = '1';
    return a;
  }

  // A profile picture if the user has one (served to connected users only,
  // hence the token), otherwise the initial on a gradient.
  function avatar(nick, small) {
    const v = st.avatars.get(fold(nick));
    if (!v || !st.uploadToken) return initialAvatar(nick, small);
    const img = el('img', 'avatar photo' + (small ? ' sm' : ''));
    img.alt = '';
    img.decoding = 'async';
    img.dataset.av = fold(nick);
    if (small) img.dataset.small = '1';
    img.src = '/avatar/' + encodeURIComponent(nick) + '?v=' + v + '&t=' + encodeURIComponent(st.uploadToken);
    img.addEventListener('error', () => img.replaceWith(initialAvatar(nick, small)), { once: true });
    return img;
  }

  // Re-renders every avatar of nick on screen.
  function refreshAvatars(nick) {
    for (const old of document.querySelectorAll('[data-av="' + CSS.escape(fold(nick)) + '"]')) {
      const fresh = avatar(nick, old.dataset.small === '1');
      if (old.id) fresh.id = old.id;
      old.replaceWith(fresh);
    }
  }

  function formatTime(d) {
    const pad = (x) => String(x).padStart(2, '0');
    const hm = pad(d.getHours()) + ':' + pad(d.getMinutes());
    const now = new Date();
    if (d.toDateString() === now.toDateString()) return hm;
    return pad(d.getDate()) + '/' + pad(d.getMonth() + 1) + ' ' + hm;
  }

  // Appends text to parent, turning http(s) URLs into safe links.
  function linkify(parent, text) {
    let last = 0;
    for (const m of text.matchAll(URL_RE)) {
      const url = m[0].replace(/[.,;:!?)\]}]+$/, '');
      if (m.index > last) parent.append(text.slice(last, m.index));
      const a = el('a', '', url);
      a.href = url;
      a.rel = 'noopener noreferrer';
      a.target = '_blank';
      parent.append(a);
      const img = previewOf(url);
      if (img) parent.append(img);
      last = m.index + url.length;
    }
    if (last < text.length) parent.append(text.slice(last));
  }

  function previewOf(url) {
    let u;
    try { u = new URL(url); } catch { return null; }
    if (u.origin !== location.origin || !PREVIEW_RE.test(u.pathname)) return null;
    const a = el('a');
    a.href = u.href;
    a.rel = 'noopener noreferrer';
    a.target = '_blank';
    const img = el('img', 'preview');
    img.src = u.pathname;
    let name = u.pathname.split('/').pop();
    try { name = decodeURIComponent(name); } catch { /* keep it encoded */ }
    img.alt = 'Aperçu : ' + name;
    img.loading = 'lazy';
    a.append(img);
    return a;
  }

  const isTalk = (l) => l.nick && /^(msg|action|notice)/.test(l.kind);

  function dayLabel(d) {
    const today = new Date();
    const yesterday = new Date(today.getFullYear(), today.getMonth(), today.getDate() - 1);
    if (d.toDateString() === today.toDateString()) return 'Aujourd’hui';
    if (d.toDateString() === yesterday.toDateString()) return 'Hier';
    return d.toLocaleDateString('fr-FR', { weekday: 'long', day: 'numeric', month: 'long' });
  }

  // renderLine returns the nodes for l: an optional day separator, then the
  // line. Consecutive lines of one author are grouped (no repeated header).
  function renderLine(l, prev) {
    const nodes = [];
    if (!prev || prev.at.toDateString() !== l.at.toDateString()) nodes.push(el('li', 'day', dayLabel(l.at)));
    const grouped = prev && isTalk(l) && isTalk(prev) && fold(prev.nick) === fold(l.nick) &&
      prev.at.toDateString() === l.at.toDateString() && l.at - prev.at < GROUP_MS && prev.kind.split(' ')[0] === l.kind.split(' ')[0];
    const talk = isTalk(l);
    const li = el('li', 'line ' + l.kind + (talk ? (grouped ? ' cont' : ' first') : '') + (l.mine ? ' mine' : ''));
    if (l.msgid) li.dataset.msgid = l.msgid;
    const t = el('time', '', formatTime(l.at));
    t.dateTime = l.at.toISOString();
    const text = el('span', 'text');
    if (l.kind.startsWith('action')) text.append(l.nick, ' ');
    linkify(text, l.text);
    if (talk && !grouped) {
      li.append(avatar(l.nick));
      const meta = el('div', 'meta');
      meta.append(el('span', nickClass(l.nick), l.kind.startsWith('notice') ? '-' + l.nick + '-' : l.nick), t);
      li.append(meta, text);
    } else if (talk) {
      li.append(t, text);
    } else {
      li.append(text, t);
    }
    if (l.own && l.msgid) li.append(deleteButton(l));
    nodes.push(li);
    return nodes;
  }

  // Your own messages get a delete button: first click asks, second deletes.
  function deleteButton(l) {
    const btn = el('button', 'del', '🗑');
    btn.type = 'button';
    btn.title = 'Supprimer ce message';
    btn.setAttribute('aria-label', 'Supprimer ce message');
    let timer = 0;
    btn.addEventListener('click', () => {
      if (!btn.classList.contains('confirm')) {
        btn.classList.add('confirm');
        btn.textContent = 'Supprimer ?';
        timer = setTimeout(() => { btn.classList.remove('confirm'); btn.textContent = '🗑'; }, CONFIRM_MS);
        return;
      }
      clearTimeout(timer);
      btn.disabled = true;
      send('REDACT ' + l.target + ' ' + l.msgid);
    });
    return btn;
  }

  // Removes a redacted line from its conversation.
  function redact(b, msgid) {
    const i = b.lines.findIndex((l) => l.msgid === msgid);
    if (i < 0) return;
    b.lines.splice(i, 1);
    if (b.key !== st.active) return;
    const li = document.querySelector('#messages li[data-msgid="' + CSS.escape(msgid) + '"]');
    const rerender = () => {
      const list = $('messages');
      const top = list.scrollTop;
      list.replaceChildren(...renderLines(b.lines));
      list.scrollTop = top;
    };
    if (li && motion()) {
      li.classList.add('gone');
      li.addEventListener('animationend', rerender, { once: true });
    } else {
      rerender();
    }
  }

  function renderLines(lines) {
    const out = [];
    lines.forEach((l, i) => out.push(...renderLine(l, lines[i - 1])));
    return out;
  }

  function setActive(b) {
    const switching = st.active !== b.key;
    // State first, synchronously: what is typed or dropped right after a
    // click must go to the new conversation. Only the rendering is deferred.
    st.active = b.key;
    b.unread = 0;
    if (switching && motion() && document.startViewTransition && !$('app').hidden) {
      document.startViewTransition(() => showBuffer(b));
      return;
    }
    showBuffer(b);
  }

  function showBuffer(b) {
    st.active = b.key;
    b.unread = 0;
    $('jump').hidden = true;
    $('buffer-name').textContent = b.type === 'server' ? 'fugace' : b.name;
    $('topic').textContent = b.topic;
    $('topic').title = b.topic;
    const list = $('messages');
    list.replaceChildren(...renderLines(b.lines));
    list.scrollTop = list.scrollHeight;
    $('members-panel').hidden = b.type !== 'channel';
    renderBuffers();
    renderMembers();
    togglePanel('sidebar', false);
  }

  function renderBuffers() {
    for (const [id, type] of [['channels', 'channel'], ['queries', 'query']]) {
      const items = [];
      const sorted = [...st.buffers.values()].filter((b) => b.type === type).sort((a, b) => a.key.localeCompare(b.key));
      for (const b of sorted) {
        const btn = el('button', '');
        btn.type = 'button';
        if (type === 'channel') btn.append(el('span', 'hash', '#'));
        else btn.append(avatar(b.name, true));
        btn.append(el('span', 'name', type === 'channel' ? b.name.slice(1) : b.name));
        if (b.unread) {
          const badge = el('span', 'unread', String(b.unread));
          if (b.unread > (b.shown || 0)) badge.classList.add('bump');
          btn.append(badge);
        }
        b.shown = b.unread;
        btn.setAttribute('aria-current', String(b.key === st.active));
        btn.addEventListener('click', () => { setActive(b); $('input').focus(); });
        const li = el('li');
        li.append(btn);
        items.push(li);
      }
      $(id).replaceChildren(...items);
    }
    moveIndicator();
  }

  // The active conversation is marked by one pill sliding between items.
  function moveIndicator() {
    const ind = $('indicator');
    const btn = document.querySelector('.buffers button[aria-current=true]');
    if (!btn) { ind.classList.remove('on'); return; }
    const wrap = $('buffers-wrap').getBoundingClientRect();
    const r = btn.getBoundingClientRect();
    ind.style.setProperty('--y', (r.top - wrap.top) + 'px');
    ind.style.setProperty('--h', r.height + 'px');
    ind.classList.add('on');
  }

  function renderMembers() {
    const b = st.buffers.get(st.active);
    if (!b || b.type !== 'channel') return;
    const ms = [...b.members.values()].sort((x, y) => (y.prefix ? 1 : 0) - (x.prefix ? 1 : 0) || fold(x.nick).localeCompare(fold(y.nick)));
    const seen = st.shownMembers.get(b.key) || new Set();
    let fresh = 0;
    $('members').replaceChildren(...ms.map((m) => {
      const li = el('li');
      li.append(avatar(m.nick, true), el('span', 'who ' + nickClass(m.nick), m.nick));
      if (m.prefix) li.append(el('span', 'crown', '★ admin'));
      if (!seen.has(fold(m.nick))) { li.classList.add('enter'); li.style.setProperty('--i', String(Math.min(fresh++, 20))); }
      return li;
    }));
    st.shownMembers.set(b.key, new Set(ms.map((m) => fold(m.nick))));
    $('member-count').textContent = '(' + ms.length + ')';
  }

  function togglePanel(id, open) {
    const p = $(id);
    const btn = $(id === 'sidebar' ? 'toggle-sidebar' : 'toggle-members');
    const on = open ?? !p.classList.contains('open');
    p.classList.toggle('open', on);
    btn.setAttribute('aria-expanded', String(on));
  }

  function status(text, waiting) {
    $('status').textContent = text || '';
    $('status').hidden = !text;
    const conn = $('conn');
    conn.classList.toggle('wait', Boolean(waiting));
    conn.setAttribute('aria-label', waiting ? 'Reconnexion…' : 'Connecté');
  }

  // --- connection -------------------------------------------------------
  function send(line) {
    if (st.ws && st.ws.readyState === WebSocket.OPEN) st.ws.send(clean(line));
  }

  function connect() {
    clearTimeout(st.timer);
    st.registered = false;
    st.uploadToken = ''; // tokens die with their connection
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(proto + '//' + location.host + '/ws', 'text.ircv3.net');
    st.ws = ws;
    ws.addEventListener('open', () => {
      send('CAP LS 302');
      send('KEY ' + identityKey());
      if (st.serverPass) send('PASS :' + st.serverPass);
      send('NICK ' + st.wantNick);
      send('USER web 0 * :web');
    });
    ws.addEventListener('message', (ev) => {
      if (typeof ev.data !== 'string') return;
      const m = parse(ev.data);
      if (m) handle(m);
    });
    ws.addEventListener('close', () => {
      if (st.ws !== ws) return;
      st.ws = null;
      if (st.clearing) { finishClear(); return; }
      if (st.closing) { status('Déconnecté.'); return; }
      if (!st.everRegistered) { showLogin(st.loginError || 'Connexion impossible.'); return; }
      const delay = BACKOFF[Math.min(st.retry, BACKOFF.length - 1)] * (0.8 + Math.random() * 0.4);
      st.retry++;
      status('Connexion perdue. Nouvelle tentative dans ' + Math.round(delay) + ' s…', true);
      st.timer = setTimeout(connect, delay * 1000);
    });
  }

  function onWelcome(nick) {
    st.registered = true;
    st.everRegistered = true;
    st.retry = 0;
    st.nick = nick;
    st.loginError = '';
    status('');
    $('me-nick').textContent = nick;
    $('me-avatar').replaceWith(Object.assign(avatar(nick), { id: 'me-avatar' }));
    $('login').hidden = true;
    st.clearing = false;
    $('app').hidden = false;
    if (st.adminPass) send('OPER admin :' + st.adminPass);
    send('JOIN ' + [...st.channels].join(','));
    $('input').focus();
  }

  // --- protocol ---------------------------------------------------------
  function handle(m) {
    const p = m.params;
    const from = nickOf(m.source);
    const self = st.nick && fold(from) === fold(st.nick);
    const at = timeOf(m.tags);
    switch (m.command) {
      case 'PING': send('PONG :' + (p[0] || '')); return;
      case 'CAP': return onCap(p);
      case '001': return onWelcome(p[0]);
      case '433': case '432':
        if (!st.registered) {
          if (st.everRegistered && m.command === '433' && st.wantNick.length < 30) { st.wantNick += '_'; send('NICK ' + st.wantNick); return; }
          st.loginError = m.command === '433' ? 'Ce pseudo est déjà utilisé.' : 'Pseudo invalide.';
          st.closing = true;
          st.ws.close();
          showLogin(st.loginError);
          return;
        }
        return info(p.slice(1).join(' '), 'error');
      case '464':
        if (!st.registered) { st.loginError = 'Mot de passe du serveur incorrect ou manquant.'; st.needPass = true; return; }
        return info('Mot de passe admin incorrect.', 'error');
      case '381':
        st.admin = true;
        $('me-admin').hidden = false;
        return info('Vous êtes admin : /join #salon crée un salon, /delchan et /clearhistory le gèrent.');
      case 'ERROR': return info(p[0] || 'Fermeture', 'error');
      case 'JOIN': return onJoin(p[0], from, self, at);
      case 'PART': return onPart(p[0], from, self, p[1], at);
      case 'KICK': return onKick(p[0], p[1], p[2], from, at);
      case 'QUIT': return onQuit(from, p[0], at);
      case 'NICK': return onNick(from, p[0], self, at);
      case 'TOPIC': {
        const b = buffer(p[0], 'channel');
        b.topic = p[1] || '';
        if (b.key === st.active) setActive(b);
        return add(b, 'info', from + ' a changé le sujet : ' + b.topic, { at });
      }
      case '332': { const b = buffer(p[1], 'channel'); b.topic = p[2] || ''; if (b.key === st.active) setActive(b); return; }
      case '331': return;
      case '353': {
        const b = buffer(p[2], 'channel');
        for (const n of (p[3] || '').split(' ')) {
          if (!n) continue;
          const prefix = n[0] === '@' ? '@' : '';
          const nick = prefix ? n.slice(1) : n;
          b.members.set(fold(nick), { nick, prefix });
        }
        return;
      }
      case '366': renderMembers(); return;
      case 'MODE':
        if (p[0] && p[0].startsWith('#') && p[1] === '+o' && p[2]) {
          const b = buffer(p[0], 'channel');
          const mem = b.members.get(fold(p[2]));
          if (mem) { mem.prefix = '@'; renderMembers(); }
        }
        return;
      case 'PRIVMSG': case 'NOTICE': return onMessage(m, from, self, at);
      case 'REDACT': {
        const target = p[0] || '';
        let b;
        if (target.startsWith('#')) b = st.buffers.get(fold(target));
        else b = st.buffers.get(fold(st.nick && fold(target) === fold(st.nick) ? from : target));
        if (b) redact(b, p[1] || '');
        return;
      }
      case 'AVATAR': {
        const v = Number(p[0]) || 0;
        if (v > 0) st.avatars.set(fold(from), v); else st.avatars.delete(fold(from));
        if (self) $('avatar-remove').hidden = v === 0;
        refreshAvatars(from);
        return;
      }
      case 'FAIL':
        if (p[0] === 'REDACT') {
          for (const b of document.querySelectorAll('.del[disabled]')) b.disabled = false;
          return info(p[p.length - 1] || 'Suppression impossible.', 'error');
        }
        return info((p[0] || '') + ' : ' + (p[p.length - 1] || ''), 'error');
      case '005':
        for (const tok of p.slice(1, -1)) if (tok.startsWith(UPLOAD_TOKEN)) st.uploadToken = tok.slice(UPLOAD_TOKEN.length);
        if (st.uploadToken) restoreAvatar();
        return;
      case '002': case '003': case '004': case '251': case '254': case '255': case '422': return;
      default:
        if (/^[45]\d\d$/.test(m.command)) return info(p.slice(1).join(' '), 'error');
        if (/^\d\d\d$/.test(m.command)) return add(buffer(SERVER, 'server'), 'info', p.slice(1).join(' '), { at });
    }
  }

  function onCap(p) {
    const sub = (p[1] || '').toUpperCase();
    if (sub === 'LS') {
      const offered = (p[p.length - 1] || '').split(' ');
      const want = WANTED_CAPS.filter((c) => offered.includes(c));
      if (want.length) send('CAP REQ :' + want.join(' '));
      else send('CAP END');
    } else if (sub === 'ACK' || sub === 'NAK') {
      send('CAP END');
    }
  }

  function onJoin(ch, from, self, at) {
    const b = buffer(ch, 'channel');
    if (self) {
      // The server replays the channel history after each join: start fresh.
      b.lines = [];
      b.members.clear();
      b.joined = true;
      st.channels.add(b.name);
      if (st.focusNext && fold(st.focusNext) === b.key) { st.focusNext = ''; setActive(b); }
      else if (b.key === st.active) setActive(b);
      return;
    }
    b.members.set(fold(from), { nick: from, prefix: '' });
    renderMembers();
    add(b, 'info', '→ ' + from + ' a rejoint ' + b.name, { at });
  }

  function onPart(ch, from, self, reason, at) {
    const b = buffer(ch, 'channel');
    if (self) {
      st.channels.delete(b.name);
      dropBuffer(b);
      return;
    }
    b.members.delete(fold(from));
    renderMembers();
    add(b, 'info', '← ' + from + ' a quitté ' + b.name + (reason ? ' (' + reason + ')' : ''), { at });
  }

  function onKick(ch, target, reason, from, at) {
    const b = buffer(ch, 'channel');
    if (st.nick && fold(target) === fold(st.nick)) {
      st.channels.delete(b.name);
      dropBuffer(b);
      info('Le salon ' + b.name + ' a été fermé par ' + from + (reason ? ' : ' + reason : '') + '.');
      return;
    }
    b.members.delete(fold(target));
    renderMembers();
    add(b, 'info', target + ' a été retiré de ' + b.name, { at });
  }

  function onQuit(from, reason, at) {
    for (const b of st.buffers.values()) {
      if (b.members.delete(fold(from)) || (b.type === 'query' && b.key === fold(from))) {
        add(b, 'info', '← ' + from + ' est parti' + (reason ? ' (' + reason + ')' : ''), { at });
      }
    }
    renderMembers();
  }

  function onNick(from, to, self, at) {
    const pic = st.avatars.get(fold(from));
    if (pic) { st.avatars.delete(fold(from)); st.avatars.set(fold(to), pic); }
    if (self) {
      st.nick = to;
      st.wantNick = to;
      $('me-nick').textContent = to;
      $('me-avatar').replaceWith(Object.assign(avatar(to), { id: 'me-avatar' }));
    }
    for (const b of st.buffers.values()) {
      const mem = b.members.get(fold(from));
      if (mem) {
        b.members.delete(fold(from));
        b.members.set(fold(to), { nick: to, prefix: mem.prefix });
        add(b, 'info', from + ' s’appelle maintenant ' + to, { at });
      }
    }
    const q = st.buffers.get(fold(from));
    if (q && q.type === 'query') {
      st.buffers.delete(q.key);
      q.key = fold(to);
      q.name = to;
      st.buffers.set(q.key, q);
      if (st.active === fold(from)) st.active = q.key;
    }
    renderBuffers();
    renderMembers();
  }

  function onMessage(m, from, self, at) {
    const [target, raw = ''] = m.params;
    const notice = m.command === 'NOTICE';
    if (!m.source.includes('!')) return info(raw, 'notice-server');
    let text = raw;
    let kind = notice ? 'notice' : 'msg';
    if (text.startsWith('\x01')) {
      const inner = text.slice(1).replace(/\x01$/, '');
      if (!inner.startsWith('ACTION ')) return; // other CTCP: never answered (privacy)
      text = inner.slice(7);
      kind = 'action';
    }
    text = text.replace(FORMAT_RE, '');
    let b;
    if (target.startsWith('#')) b = buffer(target, 'channel');
    else b = buffer(self ? target : from, 'query');
    add(b, kind, text, { nick: from, at, msgid: m.tags.msgid, own: 'fugace/own' in m.tags });
  }

  // --- user input -------------------------------------------------------
  const HELP = [
    '/join #salon : rejoindre un salon',
    '/part [raison] : quitter le salon courant',
    '/msg pseudo texte : message privé',
    '/me texte : action',
    '/nick pseudo : changer de pseudo',
    '/topic texte : changer le sujet du salon',
    '/close : fermer la conversation privée courante',
    '/quit [message] : se déconnecter',
    'Fichiers : bouton 📎, glisser-déposer ou coller une image (1 h, puis effacés)',
    'Vos messages : survolez-les puis 🗑 pour les supprimer ; photo de profil : cliquez sur votre avatar en bas à gauche',
    '/clear-account : supprime vos messages, fichiers et photo, oublie ce navigateur, puis déconnecte',
    'Admin : /oper motdepasse, /join #nouveau (création), /delchan [#salon], /clearhistory [#salon]',
  ];

  function say(target, text) {
    for (const part of chunks(text, CHUNK_BYTES)) send('PRIVMSG ' + target + ' :' + part);
  }

  function activeTarget() {
    const b = current();
    return b.type === 'server' ? null : b;
  }

  function command(line) {
    const [word, ...args] = line.slice(1).split(' ');
    const rest = args.join(' ').trim();
    const b = activeTarget();
    const needChannel = () => {
      const ch = args[0] && args[0].startsWith('#') ? args.shift() : b && b.type === 'channel' ? b.name : null;
      if (!ch) info('Aucun salon sélectionné.', 'error');
      return ch;
    };
    switch (word.toLowerCase()) {
      case 'join': {
        let ch = args[0] || '';
        if (ch && !ch.startsWith('#')) ch = '#' + ch;
        if (!CHAN_RE.test(ch)) return info('Nom de salon invalide.', 'error');
        st.focusNext = ch;
        const existing = st.buffers.get(fold(ch));
        if (existing && existing.joined) return setActive(existing);
        return send('JOIN ' + ch);
      }
      case 'part': {
        const ch = needChannel();
        if (ch) send('PART ' + ch + (args.length ? ' :' + args.join(' ') : ''));
        return;
      }
      case 'msg': case 'query': {
        const nick = args.shift() || '';
        if (!NICK_RE.test(nick) && !CHAN_RE.test(nick)) return info('Destinataire invalide.', 'error');
        const q = buffer(nick, nick.startsWith('#') ? 'channel' : 'query');
        setActive(q);
        if (args.length) say(nick, args.join(' '));
        return;
      }
      case 'me':
        if (!b) return info('Aucune conversation sélectionnée.', 'error');
        return send('PRIVMSG ' + b.name + ' :\x01ACTION ' + rest + '\x01');
      case 'nick':
        if (!NICK_RE.test(args[0] || '')) return info('Pseudo invalide.', 'error');
        return send('NICK ' + args[0]);
      case 'topic': {
        const ch = needChannel();
        if (ch) send(args.length ? 'TOPIC ' + ch + ' :' + args.join(' ') : 'TOPIC ' + ch);
        return;
      }
      case 'close':
        if (b && b.type === 'query') dropBuffer(b);
        return;
      case 'quit':
        st.closing = true;
        send('QUIT :' + (rest || 'À bientôt'));
        return;
      case 'oper':
        if (!rest) return info('Usage : /oper motdepasse', 'error');
        return send('OPER admin :' + rest);
      case 'delchan': {
        const ch = needChannel();
        if (ch) send('DELCHAN ' + ch + (args.length ? ' :' + args.join(' ') : ''));
        return;
      }
      case 'clearhistory': {
        const ch = needChannel();
        if (ch) send('CLEARHISTORY ' + ch);
        return;
      }
      case 'clear-account':
        return clearAccount();
      case 'help':
        for (const h of HELP) info(h);
        return;
      default:
        info('Commande inconnue : /' + word + ' (voir /help)', 'error');
    }
  }

  function submitInput(ev) {
    ev.preventDefault();
    const input = $('input');
    const text = clean(input.value).trim();
    input.value = '';
    if (!text) return;
    if (text.startsWith('/') && !text.startsWith('//')) return command(text);
    const b = activeTarget();
    if (!b) return info('Choisissez un salon pour écrire.', 'error');
    say(b.name, text.startsWith('//') ? text.slice(1) : text);
    const send = $('send');
    send.classList.remove('ready', 'go');
    void send.offsetWidth; // restart the animation
    send.classList.add('go');
  }

  // --- uploads ----------------------------------------------------------
  function upload(file) {
    const b = activeTarget();
    if (!b) return info('Choisissez un salon ou une conversation avant d’envoyer un fichier.', 'error');
    if (!st.uploadToken) return info('Envoi de fichiers indisponible pour le moment.', 'error');
    const xhr = new XMLHttpRequest(); // fetch() has no upload progress
    xhr.open('POST', '/upload?name=' + encodeURIComponent(file.name || 'image'));
    xhr.setRequestHeader('Authorization', 'Bearer ' + st.uploadToken);
    const bar = $('upload-progress');
    $('upload-label').textContent = 'Envoi de ' + (file.name || 'l’image') + '…';
    bar.value = 0;
    $('upload').hidden = false;
    xhr.upload.addEventListener('progress', (e) => { if (e.lengthComputable) bar.value = Math.round((e.loaded / e.total) * 100); });
    xhr.addEventListener('loadend', () => {
      $('upload').hidden = true;
      if (xhr.status === 201) {
        try {
          const reply = JSON.parse(xhr.responseText);
          if (typeof reply.url === 'string') { say(b.name, reply.url); burst($('attach')); }
        } catch { info('Réponse d’envoi illisible.', 'error'); }
        return;
      }
      const reasons = { 401: 'connexion requise', 413: 'fichier trop gros', 415: 'image invalide', 429: 'trop d’envois, réessayez dans quelques secondes' };
      info('Échec de l’envoi : ' + (reasons[xhr.status] || 'erreur ' + xhr.status) + '.', 'error');
    });
    xhr.send(file);
  }

  function setupUploads() {
    $('attach').addEventListener('click', () => $('file').click());
    $('file').addEventListener('change', () => {
      for (const f of $('file').files) upload(f);
      $('file').value = '';
    });
    const chat = document.querySelector('.chat');
    chat.addEventListener('dragover', (e) => { e.preventDefault(); chat.classList.add('dragging'); });
    chat.addEventListener('dragleave', () => chat.classList.remove('dragging'));
    chat.addEventListener('drop', (e) => {
      e.preventDefault();
      chat.classList.remove('dragging');
      for (const f of e.dataTransfer.files) upload(f);
    });
    $('input').addEventListener('paste', (e) => {
      const files = [...(e.clipboardData ? e.clipboardData.files : [])];
      if (!files.length) return;
      e.preventDefault();
      for (const f of files) upload(f);
    });
  }

  // --- effects ----------------------------------------------------------
  const motion = () => document.documentElement.dataset.motion === 'full';
  const finePointer = matchMedia('(pointer: fine)');

  function setupEffects() {
    // Ripple from the pointer on every button.
    document.addEventListener('pointerdown', (e) => {
      const btn = e.target.closest('.btn, .buffers button');
      if (!btn || !motion()) return;
      const r = btn.getBoundingClientRect();
      const dot = el('span', 'ripple');
      dot.style.setProperty('--rx', (e.clientX - r.left) + 'px');
      dot.style.setProperty('--ry', (e.clientY - r.top) + 'px');
      dot.style.setProperty('--rs', Math.max(r.width, r.height) * 2.2 + 'px');
      dot.addEventListener('animationend', () => dot.remove());
      btn.append(dot);
    });
    // Magnetic primary buttons and a spotlight that follows the cursor.
    let frame = 0;
    document.addEventListener('pointermove', (e) => {
      if (!motion() || !finePointer.matches || frame) return;
      frame = requestAnimationFrame(() => {
        frame = 0;
        const root = document.documentElement;
        root.style.setProperty('--mx', e.clientX + 'px');
        root.style.setProperty('--my', e.clientY + 'px');
        document.querySelector('.spotlight').classList.add('on');
        for (const m of document.querySelectorAll('.magnetic')) {
          const r = m.getBoundingClientRect();
          const dx = e.clientX - (r.left + r.width / 2);
          const dy = e.clientY - (r.top + r.height / 2);
          // A small radius and a few pixels at most: a nudge, not a chase.
          const near = Math.hypot(dx, dy) < MAGNET_RADIUS;
          const clamp = (v) => Math.max(-MAGNET_MAX, Math.min(MAGNET_MAX, v * 0.15));
          m.style.setProperty('--tx', near ? clamp(dx) + 'px' : '0px');
          m.style.setProperty('--ty', near ? clamp(dy) + 'px' : '0px');
        }
      });
    });
    document.addEventListener('pointerleave', () => document.querySelector('.spotlight').classList.remove('on'));
    // The send button wakes up when there is something to send.
    $('input').addEventListener('input', () => {
      const ready = $('input').value.trim() !== '';
      if (ready !== $('send').classList.contains('ready')) $('send').classList.toggle('ready', ready);
    });
    // Keep the active pill aligned on resize.
    addEventListener('resize', moveIndicator);
    $('messages').addEventListener('scroll', () => {
      const list = $('messages');
      if (list.scrollHeight - list.scrollTop - list.clientHeight < 80) $('jump').hidden = true;
    });
    // A preview that finishes loading grows the list: stay at the bottom if
    // the reader was following the conversation.
    $('messages').addEventListener('load', (e) => {
      const list = $('messages');
      if (e.target.tagName === 'IMG' && list.scrollHeight - list.scrollTop - list.clientHeight < e.target.offsetHeight + 120) {
        list.scrollTop = list.scrollHeight;
      }
    }, true);
    $('jump').addEventListener('click', () => {
      const list = $('messages');
      list.scrollTop = list.scrollHeight;
      $('jump').hidden = true;
    });
  }

  // A small burst of particles from an element (full motion only).
  function burst(from) {
    if (!motion() || !from.animate) return;
    const r = from.getBoundingClientRect();
    for (let i = 0; i < SPARKS; i++) {
      const s = el('span', 'spark s' + (i % 4));
      s.style.setProperty('--x', r.left + r.width / 2 + 'px');
      s.style.setProperty('--y', r.top + r.height / 2 + 'px');
      $('fx').append(s);
      const angle = (Math.PI * 2 * i) / SPARKS + Math.random() * 0.5;
      const dist = 50 + Math.random() * 70;
      s.animate([
        { transform: 'translate(-50%, -50%) scale(1) rotate(0deg)', opacity: 1 },
        { transform: `translate(${Math.cos(angle) * dist}px, ${Math.sin(angle) * dist - 30}px) scale(0) rotate(${200 + Math.random() * 300}deg)`, opacity: 0 },
      ], { duration: 650 + Math.random() * 450, easing: 'cubic-bezier(.2,.7,.2,1)' }).finished.then(() => s.remove());
    }
  }

  function setupAppearance() {
    const prefs = window.fugacePrefs;
    if (!prefs) return;
    for (const kind of ['theme', 'motion']) {
      for (const input of document.querySelectorAll('input[name=' + kind + ']')) {
        input.checked = input.value === prefs.get(kind);
        input.addEventListener('change', () => {
          if (!input.checked) return;
          const change = () => prefs.set(kind, input.value);
          // Theme changes cross-fade when motion is on.
          if (kind === 'theme' && motion() && document.startViewTransition) document.startViewTransition(change);
          else change();
        });
      }
    }
  }

  // Same-origin image previews open in a lightbox; modified clicks keep
  // their usual behaviour (new tab).
  function setupLightbox() {
    const box = $('lightbox');
    $('messages').addEventListener('click', (e) => {
      const img = e.target.closest('img.preview');
      if (!img || e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey || !box.showModal) return;
      e.preventDefault();
      $('lightbox-img').src = img.getAttribute('src');
      $('lightbox-img').alt = img.alt;
      box.showModal();
    });
    box.addEventListener('click', (e) => { if (e.target === box) box.close(); });
  }

  // --- profile picture and account -------------------------------------
  function dataURLToBlob(url) {
    const [head, b64] = url.split(',');
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return new Blob([bytes], { type: head.slice(5, head.indexOf(';')) });
  }

  function sendAvatar(blob) {
    if (!st.uploadToken) return Promise.resolve(false);
    return new Promise((resolve) => {
      const xhr = new XMLHttpRequest();
      xhr.open('POST', '/avatar');
      xhr.setRequestHeader('Authorization', 'Bearer ' + st.uploadToken);
      xhr.addEventListener('loadend', () => resolve(xhr.status === 201));
      xhr.send(blob);
    });
  }

  // Square-crops and resizes in the browser, so that a big photo travels as
  // a few kilobytes; the server cleans and re-encodes it again.
  async function resizeImage(file) {
    const bitmap = await createImageBitmap(file);
    const side = Math.min(bitmap.width, bitmap.height);
    const canvas = document.createElement('canvas');
    canvas.width = canvas.height = AVATAR_PX;
    canvas.getContext('2d').drawImage(bitmap, (bitmap.width - side) / 2, (bitmap.height - side) / 2, side, side, 0, 0, AVATAR_PX, AVATAR_PX);
    bitmap.close();
    return new Promise((resolve) => canvas.toBlob(resolve, 'image/jpeg', 0.88));
  }

  async function chooseAvatar(file) {
    let blob;
    try { blob = await resizeImage(file); } catch { blob = null; }
    if (!blob) return info('Cette image ne peut pas servir de photo de profil.', 'error');
    if (!(await sendAvatar(blob))) return info('Échec de l’envoi de la photo.', 'error');
    burst($('me-photo'));
    const reader = new FileReader();
    reader.addEventListener('load', () => store(AVATAR_STORAGE, String(reader.result)));
    reader.readAsDataURL(blob); // remembered here, re-sent at each connection
  }

  function restoreAvatar() {
    const saved = stored(AVATAR_STORAGE);
    if (saved && saved.startsWith('data:image/')) {
      try { sendAvatar(dataURLToBlob(saved)); } catch { store(AVATAR_STORAGE, null); }
    }
  }

  function removeAvatar() {
    store(AVATAR_STORAGE, null);
    if (!st.uploadToken) return;
    const xhr = new XMLHttpRequest();
    xhr.open('DELETE', '/avatar');
    xhr.setRequestHeader('Authorization', 'Bearer ' + st.uploadToken);
    xhr.send();
  }

  function clearAccount() {
    st.clearing = true;
    st.closing = true;
    send('CLEAR-ACCOUNT');
  }

  // After CLEAR-ACCOUNT: forget everything this browser knows, back to login.
  function finishClear() {
    for (const k of [STORAGE_KEY, KEY_STORAGE, AVATAR_STORAGE]) store(k, null);
    st.buffers.clear();
    st.avatars.clear();
    st.channels = new Set(['#general']);
    st.focusNext = '#general';
    st.active = SERVER;
    $('nick').value = '';
    $('remember').checked = false;
    $('messages').replaceChildren();
    showLogin('Compte effacé : vos messages, fichiers et photo ont été supprimés, et ce navigateur a tout oublié.');
  }

  function setupAccount() {
    $('me-photo').addEventListener('click', () => $('avatar-file').click());
    $('avatar-file').addEventListener('change', () => {
      const f = $('avatar-file').files[0];
      $('avatar-file').value = '';
      if (f) chooseAvatar(f);
    });
    $('avatar-remove').addEventListener('click', removeAvatar);
    const clear = $('clear-account');
    let timer = 0;
    clear.addEventListener('click', () => {
      if (!clear.classList.contains('confirm')) {
        clear.classList.add('confirm');
        clear.textContent = 'Confirmer l’effacement';
        timer = setTimeout(() => { clear.classList.remove('confirm'); clear.textContent = 'Effacer mon compte'; }, CONFIRM_MS);
        return;
      }
      clearTimeout(timer);
      clear.classList.remove('confirm');
      clear.textContent = 'Effacer mon compte';
      clearAccount();
    });
  }

  // --- login ------------------------------------------------------------
  function showLogin(error) {
    $('app').hidden = true;
    $('login').hidden = false;
    $('login-error').textContent = error || '';
    $('login-error').hidden = !error;
    if (st.needPass) {
      // The server is password-protected: show the field instead of hiding it.
      st.needPass = false;
      document.querySelector('#login details').open = true;
      $('server-pass').focus();
      return;
    }
    $('nick').focus();
  }

  function submitLogin(ev) {
    ev.preventDefault();
    const nick = $('nick').value.trim();
    if (!NICK_RE.test(nick)) {
      $('nick').setAttribute('aria-invalid', 'true');
      return showLogin('Pseudo invalide.');
    }
    $('nick').removeAttribute('aria-invalid');
    try {
      if ($('remember').checked) localStorage.setItem(STORAGE_KEY, nick);
      else localStorage.removeItem(STORAGE_KEY);
    } catch { /* storage disabled: nothing remembered */ }
    st.wantNick = nick;
    st.serverPass = $('server-pass').value;
    st.adminPass = $('admin-pass').value;
    st.closing = false;
    st.everRegistered = false;
    $('login-error').hidden = true;
    buffer(SERVER, 'server');
    connect();
  }

  function init() {
    try {
      const saved = localStorage.getItem(STORAGE_KEY);
      if (saved) { $('nick').value = saved; $('remember').checked = true; }
    } catch { /* storage disabled */ }
    $('nick').setAttribute('aria-describedby', 'nick-hint');
    $('login-form').addEventListener('submit', submitLogin);
    $('input-form').addEventListener('submit', submitInput);
    setupUploads();
    setupAccount();
    setupEffects();
    setupAppearance();
    setupLightbox();
    $('toggle-sidebar').addEventListener('click', () => togglePanel('sidebar'));
    $('toggle-members').addEventListener('click', () => togglePanel('members-panel'));
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') { togglePanel('sidebar', false); togglePanel('members-panel', false); }
    });
    $('nick').focus();
  }

  init();
})();
