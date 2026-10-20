// Headless browser checks of the web client (ROADMAP M2, THREAT_MODEL T3):
// XSS payloads render as text, no CSP violation, no third-party request,
// web <-> IRC chat, history replay, axe accessibility, mobile layout.
import { chromium } from 'playwright';
import net from 'node:net';
import fs from 'node:fs';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const AXE = fs.readFileSync(require.resolve('axe-core/axe.min.js'), 'utf8');
const BASE = process.env.BASE || 'https://fugace-it:8443';
const [IRC_HOST, IRC_PORT] = (process.env.IRC || 'fugace-it:6667').split(':');
const ORIGIN = new URL(BASE).origin;
const failures = [];
const fail = (msg) => { failures.push(msg); console.log('FAIL: ' + msg); };
const ok = (msg) => console.log('ok: ' + msg);

// 48x32 JPEG whose EXIF carries a GPS-looking secret (must not survive).
const PHOTO = Buffer.from('/9j/4QA3RXhpZgAASUkqAAgAAAABAA4BAgAVAAAAGgAAAAAAAABHUFMtNDguODU4NE4tMi4yOTQ1RQD/2wCEAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDIBCQkJDAsMGA0NGDIhHCEyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMv/AABEIACAAMAMBIgACEQEDEQH/xAGiAAABBQEBAQEBAQAAAAAAAAAAAQIDBAUGBwgJCgsQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQygZGhCCNCscEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6g4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+gEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoLEQACAQIEBAMEBwUEBAABAncAAQIDEQQFITEGEkFRB2FxEyIygQgUQpGhscEJIzNS8BVictEKFiQ04SXxFxgZGiYnKCkqNTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqCg4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2dri4+Tl5ufo6ery8/T19vf4+fr/2gAMAwEAAhEDEQA/APJ4rP2q5FZe1asVl7Vcisvavu54k8/C4wyorP2q5FZe1asVl7VcisvauOeJPpMLjDKisvarkVn7VqxWftVyKz9q454k+jwuMMSKy9quRWftWrFZ+1XIrP2rmniT8PwuMMqKz9quRWftWrFZ+1XIrP2rjniT6PC4wyorP2q5FZ+1asVl7Vcis/auOeJPpMLjD//Z', 'base64');
const GPS = 'GPS-48.8584N';
const SVG = Buffer.from('<svg xmlns="http://www.w3.org/2000/svg" onload="alert(9)"><script>alert(10)</script></svg>');

const PAYLOADS = [
  '<img src=x onerror=alert(1)>',
  '<script>alert(2)</script>',
  'javascript:alert(3)',
  'http://example.com/"><img src=x onerror=alert(4)>',
  '<svg onload=alert(5)><a href="javascript:alert(6)">x</a>',
  '\x01ACTION <b onmouseover=alert(7)>waves</b>\x01',
];

function irc(nick) {
  const sock = net.connect(Number(IRC_PORT), IRC_HOST);
  const lines = [];
  let buf = '';
  const waiters = [];
  sock.setEncoding('utf8');
  sock.on('data', (d) => {
    buf += d;
    let i;
    while ((i = buf.indexOf('\r\n')) >= 0) {
      const l = buf.slice(0, i);
      buf = buf.slice(i + 2);
      lines.push(l);
      if (l.startsWith('PING')) sock.write('PONG' + l.slice(4) + '\r\n');
      for (const w of [...waiters]) if (w.re.test(l)) { waiters.splice(waiters.indexOf(w), 1); w.resolve(l); }
    }
  });
  const send = (l) => sock.write(l + '\r\n');
  const waitFor = (re, ms = 5000) => {
    const hit = lines.find((l) => re.test(l));
    if (hit) return Promise.resolve(hit);
    return new Promise((resolve, reject) => {
      const w = { re, resolve };
      waiters.push(w);
      setTimeout(() => reject(new Error(nick + ' timeout waiting for ' + re)), ms);
    });
  };
  send('NICK ' + nick);
  send('USER t 0 * :t');
  return { send, waitFor, lines, close: () => sock.destroy() };
}

async function axe(page, label) {
  await page.evaluate(AXE); // via CDP: the page's CSP stays enforced for everything else
  const res = await page.evaluate(async () => {
    // eslint-disable-next-line no-undef
    const r = await axe.run(document, { resultTypes: ['violations'] });
    return r.violations.map((v) => ({ id: v.id, impact: v.impact, n: v.nodes.length, help: v.help }));
  });
  const blocking = res.filter((v) => v.impact === 'critical' || v.impact === 'serious');
  for (const v of res) console.log(`   axe ${label}: ${v.impact} ${v.id} (${v.n}) ${v.help}`);
  if (blocking.length) fail(`axe ${label}: ${blocking.length} blocking violation(s)`);
  else ok(`axe ${label}: no blocking violation (${res.length} minor)`);
}

// Over plain http (before built-in TLS) Chromium reports that it ignores
// Cross-Origin-Opener-Policy; that one message is expected, nothing else is.
const insecureNoise = (t) => ORIGIN.startsWith('http:') && t.startsWith('The Cross-Origin-Opener-Policy header has been ignored');

function watch(page, sink) {
  page.on('console', (m) => { if (m.type() === 'error' && !insecureNoise(m.text())) sink.errors.push(m.text()); });
  page.on('pageerror', (e) => sink.errors.push(String(e)));
  page.on('dialog', async (d) => { sink.dialogs.push(d.message()); await d.dismiss(); });
  page.on('request', (r) => { if (new URL(r.url()).origin !== ORIGIN && !r.url().startsWith('ws')) sink.external.push(r.url()); });
}

async function login(page, nick) {
  await page.goto(BASE);
  await page.fill('#nick', nick);
  await page.click('#login-form button[type=submit]');
  await page.waitForFunction(() => document.getElementById('buffer-name').textContent === '#general', null, { timeout: 5000 });
}

const texts = (page) => page.$$eval('#messages .line .text', (els) => els.map((e) => e.textContent));

(async () => {
  const browser = await chromium.launch();
  const sink = { errors: [], dialogs: [], external: [] };
  const tcp = irc('tcpuser');
  await tcp.waitFor(/ 001 tcpuser /);
  tcp.send('JOIN #general');
  await tcp.waitFor(/ 366 tcpuser #general /);
  tcp.send('PRIVMSG #general :said before the web user arrived');

  // Desktop
  const desk = await browser.newContext({ viewport: { width: 1280, height: 800 }, ignoreHTTPSErrors: true }); // self-signed
  const page = await desk.newPage();
  watch(page, sink);
  await page.goto(BASE);
  await axe(page, 'login');
  await login(page, 'webuser');
  ok('web login and auto-join #general');
  await page.waitForFunction(() => [...document.querySelectorAll('#messages .text')].some((e) => e.textContent.includes('said before')), null, { timeout: 5000 })
    .then(() => ok('history replayed on join'), () => fail('history not replayed'));

  for (const p of PAYLOADS) tcp.send('PRIVMSG #general :' + p);
  tcp.send('TOPIC #general :<svg onload=alert(8)>');
  await page.waitForFunction(() => document.getElementById('topic').textContent.includes('<svg'), null, { timeout: 5000 });
  await page.waitForTimeout(500);
  const shown = await texts(page);
  for (const p of PAYLOADS) {
    const visible = p.startsWith('\x01ACTION ') ? p.slice(8, -1) : p;
    if (!shown.some((t) => t.includes(visible))) fail('payload not shown literally: ' + JSON.stringify(p));
  }
  const injected = await page.$$eval('#messages img, #messages script, #messages svg, #messages iframe, #messages b, #topic *', (e) => e.length);
  const hrefs = await page.$$eval('#messages a', (as) => as.map((a) => a.getAttribute('href')));
  if (injected) fail(`${injected} injected element(s) in the DOM`);
  if (hrefs.some((h) => !/^https?:\/\//.test(h))) fail('non-http link: ' + hrefs.join(' '));
  const rels = await page.$$eval('#messages a', (as) => as.every((a) => a.rel === 'noopener noreferrer'));
  if (!rels) fail('link without rel=noopener noreferrer');
  if (!injected && hrefs.every((h) => /^https?:\/\//.test(h))) ok(`XSS payloads rendered as text (${PAYLOADS.length + 1} payloads, links: ${hrefs.length})`);

  await page.fill('#input', 'hello from the browser');
  await page.press('#input', 'Enter');
  await tcp.waitFor(/:webuser!web@fugace PRIVMSG #general :hello from the browser/).then(() => ok('web -> irc message'), (e) => fail(e.message));
  tcp.send('PRIVMSG #general :hello from irc');
  await page.waitForFunction(() => [...document.querySelectorAll('#messages .text')].some((e) => e.textContent === 'hello from irc'), null, { timeout: 5000 })
    .then(() => ok('irc -> web message'), () => fail('irc message not shown'));
  await page.fill('#input', '/msg tcpuser a private word');
  await page.press('#input', 'Enter');
  await tcp.waitFor(/:webuser!web@fugace PRIVMSG tcpuser :a private word/).then(() => ok('web private message'), (e) => fail(e.message));
  await page.fill('#input', '/nick webuser2');
  await page.press('#input', 'Enter');
  await tcp.waitFor(/:webuser!web@fugace NICK webuser2/).then(() => ok('web /nick'), (e) => fail(e.message));
  await page.click('#channels button');

  // Files (M4): photo with GPS through the attach button, then an SVG.
  await page.setInputFiles('#file', { name: 'holiday photo.jpg', mimeType: 'image/jpeg', buffer: PHOTO });
  const photoLine = await tcp.waitFor(/:webuser2!web@fugace PRIVMSG #general :https:\/\/fugace-it:8443\/f\/[A-Za-z0-9_-]{22}\/holiday_photo\.jpg$/, 10000)
    .catch((e) => { fail(e.message); return ''; });
  const photoURL = photoLine.split(' :').pop();
  if (photoURL) {
    await page.waitForFunction(() => [...document.querySelectorAll('#messages img.preview')].some((i) => i.complete && i.naturalWidth === 48), null, { timeout: 5000 })
      .then(() => ok('upload: link posted, same-origin preview displayed'), () => fail('preview not displayed'));
    const res = await page.request.get(photoURL);
    const body = await res.body();
    const h = res.headers();
    if (h['content-type'] !== 'image/jpeg' || body.includes(GPS) || body.includes('Exif') || !h['content-security-policy'].includes('sandbox') || !h['content-disposition'].startsWith('inline')) {
      fail('served photo: ' + JSON.stringify(h));
    } else ok('photo served re-encoded without EXIF/GPS, CSP sandbox');
  }
  await page.setInputFiles('#file', { name: 'evil.svg', mimeType: 'image/svg+xml', buffer: SVG });
  const svgLine = await tcp.waitFor(/PRIVMSG #general :https:\/\/fugace-it:8443\/f\/[A-Za-z0-9_-]{22}\/evil\.svg$/, 10000).catch((e) => { fail(e.message); return ''; });
  if (svgLine) {
    const res = await page.request.get(svgLine.split(' :').pop());
    const h = res.headers();
    const previews = await page.$$eval('#messages img.preview', (is) => is.filter((i) => i.src.endsWith('.svg')).length);
    if (h['content-type'] !== 'application/octet-stream' || !h['content-disposition'].startsWith('attachment') || h['x-content-type-options'] !== 'nosniff' || previews) {
      fail('svg served as ' + JSON.stringify(h));
    } else ok('svg with script: attachment, octet-stream, nosniff, no preview');
  }
  // v0.2: lightbox on same-origin previews.
  if (photoURL) {
    await page.click('#messages img.preview');
    const open = await page.waitForFunction(() => document.getElementById('lightbox').open, null, { timeout: 3000 }).then(() => true, () => false);
    await page.keyboard.press('Escape');
    const closed = await page.waitForFunction(() => !document.getElementById('lightbox').open, null, { timeout: 3000 }).then(() => true, () => false);
    if (open && closed) ok('lightbox opens on a preview and closes with Escape'); else fail('lightbox');
  }
  // v0.2 regression: a line sent right after switching conversation goes to
  // the new one, even while the view transition is still running.
  await page.fill('#input', '/msg tcpuser side note');
  await page.press('#input', 'Enter');
  await tcp.waitFor(/PRIVMSG tcpuser :side note/);
  await page.click('#channels button');
  await page.fill('#input', 'back in general');
  await page.press('#input', 'Enter');
  await tcp.waitFor(/:webuser2!web@fugace PRIVMSG #general :back in general/).then(() => ok('message right after a switch goes to the new conversation'), (e) => fail(e.message));

  tcp.send('PRIVMSG #general :look http://example.com/tracker.png');
  await page.waitForFunction(() => [...document.querySelectorAll('#messages .text')].some((e) => e.textContent.includes('tracker.png')), null, { timeout: 5000 });
  if (await page.$$eval('#messages img.preview', (is) => is.some((i) => !i.src.startsWith(location.origin)))) fail('external image previewed');
  else ok('external image link not loaded');

  await axe(page, 'chat');

  // Mobile
  const mob = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, ignoreHTTPSErrors: true });
  const mpage = await mob.newPage();
  watch(mpage, sink);
  await login(mpage, 'mobileuser');
  const hiddenBefore = await mpage.$eval('#sidebar', (e) => e.getBoundingClientRect().right <= 0);
  await mpage.click('#toggle-sidebar');
  await mpage.waitForTimeout(900); // the drawer slides in with a spring (0.5 s)
  const shownAfter = await mpage.$eval('#sidebar', (e) => e.getBoundingClientRect().left >= 0);
  if (hiddenBefore && shownAfter) ok('mobile: sidebar off-canvas and toggled'); else fail('mobile sidebar toggle');
  await mpage.keyboard.press('Escape');
  await axe(mpage, 'mobile');

  // v0.2: appearance. A reduced-motion system starts calm; the user's choices
  // apply at once and survive a reload.
  const calm = await browser.newContext({ viewport: { width: 1280, height: 800 }, ignoreHTTPSErrors: true, reducedMotion: 'reduce', colorScheme: 'dark' });
  const cpage = await calm.newPage();
  watch(cpage, sink);
  await cpage.goto(BASE);
  const attr = (name) => cpage.evaluate((n) => document.documentElement.dataset[n], name);
  const startMotion = await attr('motion');
  const startTheme = await attr('theme');
  await cpage.click('#appearance-btn');
  await cpage.click('#appearance label:has(input[value=light])');
  await cpage.click('#appearance label:has(input[value=full])');
  const afterTheme = await attr('theme');
  const afterMotion = await attr('motion');
  await cpage.reload();
  const keptTheme = await attr('theme');
  const keptMotion = await attr('motion');
  if (startMotion === 'reduced' && startTheme === 'dark' && afterTheme === 'light' && afterMotion === 'full' && keptTheme === 'light' && keptMotion === 'full') {
    ok('appearance: system defaults (dark, reduced motion), choices applied and kept after reload');
  } else fail(`appearance: ${startTheme}/${startMotion} -> ${afterTheme}/${afterMotion} -> ${keptTheme}/${keptMotion}`);
  await axe(cpage, 'login (light)');
  await calm.close();

  // v0.3: delete your own message; another web user sees it disappear.
  await page.fill('#input', 'please delete me');
  await page.press('#input', 'Enter');
  await mpage.waitForFunction(() => [...document.querySelectorAll('#messages .text')].some((e) => e.textContent === 'please delete me'), null, { timeout: 5000 });
  const mineLine = page.locator('#messages li.line', { hasText: 'please delete me' });
  await mineLine.hover();
  await mineLine.locator('.del').click();
  await mineLine.locator('.del').click();
  await mpage.waitForFunction(() => ![...document.querySelectorAll('#messages .text')].some((e) => e.textContent === 'please delete me'), null, { timeout: 5000 })
    .then(() => ok('own message deleted, gone for the other web user'), () => fail('redaction not applied'));
  const othersDel = await page.$$eval('#messages li.line', (ls) => ls.filter((l) => l.textContent.includes('hello from irc') && l.querySelector('.del')).length);
  if (othersDel) fail('delete button offered on someone else\'s message');
  else ok('no delete button on other people\'s messages');

  // v0.3: profile picture, seen by the other user.
  await page.setInputFiles('#avatar-file', { name: 'me.jpg', mimeType: 'image/jpeg', buffer: PHOTO });
  await mpage.waitForFunction(() => [...document.querySelectorAll('#members img.avatar')].some((i) => i.complete && i.naturalWidth === 128), null, { timeout: 8000 })
    .then(() => ok('profile picture shown to the other user'), () => fail('profile picture not shown'));

  // v0.3: the login button does not chase the pointer any more.
  const fresh = await browser.newContext({ viewport: { width: 1280, height: 800 }, ignoreHTTPSErrors: true });
  const lpage = await fresh.newPage();
  watch(lpage, sink);
  await lpage.goto(BASE);
  await lpage.waitForTimeout(1200);
  const btn = lpage.locator('#login-form button[type=submit]');
  const box0 = await btn.boundingBox();
  for (const [dx, dy] of [[-200, -60], [120, 40], [0, -90], [210, 70]]) {
    await lpage.mouse.move(box0.x + box0.width / 2 + dx, box0.y + box0.height / 2 + dy, { steps: 6 });
  }
  await lpage.waitForTimeout(300);
  const box1 = await btn.boundingBox();
  if (Math.abs(box1.x - box0.x) < 1 && Math.abs(box1.y - box0.y) < 1) ok('login button stays put under pointer moves');
  else fail(`login button moved by ${box1.x - box0.x},${box1.y - box0.y}`);

  // v0.3: clear account wipes messages for others and this browser's memory.
  await lpage.fill('#nick', 'eraser');
  await lpage.click('label.check'); // the styled switch, as a user would
  await lpage.click('#login-form button[type=submit]');
  await lpage.waitForFunction(() => document.getElementById('buffer-name').textContent === '#general', null, { timeout: 5000 });
  await lpage.fill('#input', 'trace to erase');
  await lpage.press('#input', 'Enter');
  await page.waitForFunction(() => [...document.querySelectorAll('#messages .text')].some((e) => e.textContent === 'trace to erase'), null, { timeout: 5000 });
  await lpage.click('#clear-account');
  await lpage.click('#clear-account');
  await lpage.waitForFunction(() => !document.getElementById('login').hidden, null, { timeout: 5000 });
  const storage = await lpage.evaluate(() => ['fugace.nick', 'fugace.key', 'fugace.avatar'].filter((k) => localStorage.getItem(k) !== null));
  await page.waitForFunction(() => ![...document.querySelectorAll('#messages .text')].some((e) => e.textContent === 'trace to erase'), null, { timeout: 5000 })
    .then(() => (storage.length ? fail('browser still remembers ' + storage.join(', ')) : ok('clear account: messages gone for others, browser memory wiped, back to login')),
      () => fail('cleared messages still visible to others'));
  await fresh.close();

  // Reload: same conversation comes back from the server history.
  await page.reload();
  await login(page, 'webuser3');
  await page.waitForFunction(() => [...document.querySelectorAll('#messages .text')].some((e) => e.textContent === 'hello from the browser'), null, { timeout: 5000 })
    .then(() => ok('history after reload'), () => fail('history missing after reload'));

  if (sink.dialogs.length) fail('script executed (dialog): ' + sink.dialogs.join(', '));
  if (sink.errors.length) fail('console errors: ' + sink.errors.join(' | '));
  if (sink.external.length) fail('third-party requests: ' + sink.external.join(' '));
  if (!sink.dialogs.length && !sink.errors.length && !sink.external.length) ok('no dialog, no console error or CSP violation, no third-party request');
  tcp.close();
  await browser.close();
  process.exit(failures.length ? 1 : 0);
})().catch((e) => { console.log('FAIL: ' + e.message); process.exit(1); });
