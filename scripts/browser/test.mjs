// Headless browser checks of the web client (ROADMAP M2, THREAT_MODEL T3):
// XSS payloads render as text, no CSP violation, no third-party request,
// web <-> IRC chat, history replay, axe accessibility, mobile layout.
import { chromium } from 'playwright';
import net from 'node:net';
import fs from 'node:fs';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const AXE = fs.readFileSync(require.resolve('axe-core/axe.min.js'), 'utf8');
const BASE = process.env.BASE || 'http://fugace-it:8080';
const [IRC_HOST, IRC_PORT] = (process.env.IRC || 'fugace-it:6667').split(':');
const ORIGIN = new URL(BASE).origin;
const failures = [];
const fail = (msg) => { failures.push(msg); console.log('FAIL: ' + msg); };
const ok = (msg) => console.log('ok: ' + msg);

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
  const desk = await browser.newContext({ viewport: { width: 1280, height: 800 } });
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
  await axe(page, 'chat');

  // Mobile
  const mob = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  const mpage = await mob.newPage();
  watch(mpage, sink);
  await login(mpage, 'mobileuser');
  const hiddenBefore = await mpage.$eval('#sidebar', (e) => e.getBoundingClientRect().right <= 0);
  await mpage.click('#toggle-sidebar');
  await mpage.waitForTimeout(300);
  const shownAfter = await mpage.$eval('#sidebar', (e) => e.getBoundingClientRect().left >= 0);
  if (hiddenBefore && shownAfter) ok('mobile: sidebar off-canvas and toggled'); else fail('mobile sidebar toggle');
  await mpage.keyboard.press('Escape');
  await axe(mpage, 'mobile');

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
