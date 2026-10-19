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
  const WANTED_CAPS = ['server-time', 'echo-message'];
  const UPLOAD_TOKEN = 'fugace/UPLOAD=';
  // Only our own re-encoded images get an inline preview (anti-tracking).
  const PREVIEW_RE = /^\/f\/[A-Za-z0-9_-]{22}\/[^/]+\.(jpg|png|gif)$/;

  const st = {
    ws: null, nick: '', wantNick: '', serverPass: '', adminPass: '',
    registered: false, everRegistered: false, admin: false, closing: false,
    retry: 0, timer: null, buffers: new Map(), active: SERVER,
    channels: new Set(['#general']), focusNext: '#general', uploadToken: '',
  };

  const fold = (s) => s.replace(/[A-Z]/g, (c) => c.toLowerCase());
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
    const l = { kind, text, nick: opts.nick || '', at: opts.at || new Date() };
    if (opts.nick && st.nick && fold(opts.nick) !== fold(st.nick) && mentions(text)) l.kind += ' mention';
    b.lines.push(l);
    if (b.lines.length > MAX_LINES) b.lines.shift();
    if (b.key === st.active) {
      const list = $('messages');
      const atBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 40;
      list.append(renderLine(l));
      while (list.childElementCount > MAX_LINES) list.firstElementChild.remove();
      if (atBottom) list.scrollTop = list.scrollHeight;
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

  function nickClass(nick) {
    let h = 0;
    for (const c of fold(nick)) h = (h * 31 + c.charCodeAt(0)) >>> 0;
    return 'nick n' + (h % 8);
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

  function renderLine(l) {
    const li = el('li', 'line ' + l.kind);
    const t = el('time', '', formatTime(l.at));
    t.dateTime = l.at.toISOString();
    li.append(t);
    const text = el('span', 'text');
    if (l.kind.startsWith('action')) {
      text.append('* ', l.nick, ' ');
    } else if (l.nick) {
      li.append(el('span', nickClass(l.nick), l.kind.startsWith('notice') ? '-' + l.nick + '-' : l.nick));
    }
    linkify(text, l.text);
    li.append(text);
    return li;
  }

  function setActive(b) {
    st.active = b.key;
    b.unread = 0;
    $('buffer-name').textContent = b.type === 'server' ? 'fugace' : b.name;
    $('topic').textContent = b.topic;
    $('topic').title = b.topic;
    const list = $('messages');
    list.replaceChildren(...b.lines.map(renderLine));
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
        btn.append(el('span', '', b.name));
        if (b.unread) btn.append(el('span', 'unread', String(b.unread)));
        btn.setAttribute('aria-current', String(b.key === st.active));
        btn.addEventListener('click', () => { setActive(b); $('input').focus(); });
        const li = el('li');
        li.append(btn);
        items.push(li);
      }
      $(id).replaceChildren(...items);
    }
  }

  function renderMembers() {
    const b = st.buffers.get(st.active);
    if (!b || b.type !== 'channel') return;
    const ms = [...b.members.values()].sort((x, y) => (y.prefix ? 1 : 0) - (x.prefix ? 1 : 0) || fold(x.nick).localeCompare(fold(y.nick)));
    $('members').replaceChildren(...ms.map((m) => el('li', nickClass(m.nick), m.prefix + m.nick)));
    $('member-count').textContent = '(' + ms.length + ')';
  }

  function togglePanel(id, open) {
    const p = $(id);
    const btn = $(id === 'sidebar' ? 'toggle-sidebar' : 'toggle-members');
    const on = open ?? !p.classList.contains('open');
    p.classList.toggle('open', on);
    btn.setAttribute('aria-expanded', String(on));
  }

  function status(text) {
    $('status').textContent = text || '';
    $('status').hidden = !text;
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
      if (st.closing) { status('Déconnecté.'); return; }
      if (!st.everRegistered) { showLogin(st.loginError || 'Connexion impossible.'); return; }
      const delay = BACKOFF[Math.min(st.retry, BACKOFF.length - 1)] * (0.8 + Math.random() * 0.4);
      st.retry++;
      status('Connexion perdue. Nouvelle tentative dans ' + Math.round(delay) + ' s…');
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
    $('login').hidden = true;
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
        if (!st.registered) { st.loginError = 'Mot de passe du serveur incorrect.'; return; }
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
      case '005':
        for (const tok of p.slice(1, -1)) if (tok.startsWith(UPLOAD_TOKEN)) st.uploadToken = tok.slice(UPLOAD_TOKEN.length);
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
    if (self) {
      st.nick = to;
      st.wantNick = to;
      $('me-nick').textContent = to;
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
    add(b, kind, text, { nick: from, at });
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
          if (typeof reply.url === 'string') say(b.name, reply.url);
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

  // --- login ------------------------------------------------------------
  function showLogin(error) {
    $('app').hidden = true;
    $('login').hidden = false;
    $('login-error').textContent = error || '';
    $('login-error').hidden = !error;
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
    $('toggle-sidebar').addEventListener('click', () => togglePanel('sidebar'));
    $('toggle-members').addEventListener('click', () => togglePanel('members-panel'));
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') { togglePanel('sidebar', false); togglePanel('members-panel', false); }
    });
    $('nick').focus();
  }

  init();
})();
