# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.3.0] - 2026-10-20

Delete your messages, pick a profile picture, clear your account. Plus a one-line installer.

### Added
- `install.sh`, a standalone interactive installer: `curl -fsSL https://github.com/gabltz/fugace/releases/latest/download/install.sh | bash`.
  - It installs, reinstalls from scratch or uninstalls fugace, offering every setting with its default (an empty host means local-only).
  - It pulls the latest signed image and runs it hardened.
  - Unattended use: `--install/--reinstall/--uninstall --yes`.
  - Published with each release, along with its SHA-256.
- **Delete your messages**: a delete button on your own messages in the web client (two clicks), `REDACT` from IRC (IRCv3 `draft/message-redaction`). The message leaves the channel history and disappears for every client that supports redaction. Only the author can delete, the admin cannot.
- **Profile picture**: choose it at the bottom left of the web client. It is cropped to 128 × 128, kept in RAM while you are connected, shown to other connected users only, and sent again by your browser when you come back.
- **Clear account**: the "Effacer mon compte" button, or `/clear-account` (`CLEAR-ACCOUNT`, alias `CLEARACCOUNT`) from IRC, deletes your messages, files and picture, then disconnects you. The web client also forgets your nickname, key and picture.
- **Author identity without accounts**: the web client sends a random key kept in the browser (`KEY`), and the server keeps only a keyed hash of it until it restarts. IRC clients without a key get an identity scoped to their connection.
- Each message carries a `msgid`; the `fugace/own` tag marks your own messages; the `fugace/avatar` capability announces picture changes.
- The image is also tagged `latest`.
- The image ships an empty `/cache` mount point owned by the fugace user, so the opt-in certificate cache volume needs no preparation and `docker diff` stays empty.

### Changed
- Files you upload now belong to your identity rather than to a single connection.
- The web client budget is now 160 KB including the font.

### Fixed
- The login button no longer drifts around under the pointer.

## [0.2.0] - 2026-10-20

A redesign of the web client. The server and the protocol are unchanged.

### Added
- **Themes**: a dark "aurora" theme and a light frosted-glass theme. They follow the system, with an Auto/Light/Dark setting applied before the first paint.
- **Look and feel**:
  - glass panels, an animated background, a halo that follows the pointer, animated borders;
  - messages grouped by author with avatars, day separators and a "new messages" button;
  - spring-animated arrivals, view transitions between conversations, a sliding channel indicator;
  - magnetic buttons and ripples, an image lightbox, particles when a file is sent.
- **Motion setting**: Auto/Full/Reduced. Auto honours the system "reduce motion" preference.
- **Font**: the Inter typeface, embedded and served by fugace itself (no CDN).
- **Project**: AGPL-3.0 license, contribution guide, code of conduct, issue and pull request templates, English README.

### Changed
- CSP: adds `font-src 'self'` for the embedded font.
- The web client budget is now 128 KB including the font (it was 60 KB without a font).

### Fixed
- With animated transitions, a message or file sent right after switching conversations could go to the previous one.
- Your own messages now always scroll the conversation to the bottom. A preview that finishes loading no longer leaves the view above the latest messages.

## [0.1.0] - 2026-10-19

First release.

### Added
- **IRC server**:
  - registration with IRCv3 `CAP` 302 (`echo-message`, `server-time`, `message-tags`);
  - channels and private messages;
  - `WHO`/`WHOIS` without any IP address or idle time, inert `MODE` replies, DCC blocked;
  - fake-lag flood control, bounded send queues, registration (30 s) and ping (180 s) timeouts.
- **Channel history** kept in RAM since startup (global budget, replayed on join with the original timestamps) and **channel administration** through `OPER`: create channels, `DELCHAN`, `CLEARHISTORY`.
- **Web client** in vanilla JS over IRC-over-WebSocket (`text.ircv3.net`):
  - channels, private messages, members, commands, reconnection;
  - strict CSP without inline code, accessibility checked with axe-core.
- **Built-in HTTPS** without a reverse proxy:
  - mounted certificate > Let's Encrypt for the domains in `FUGACE_HOST` > self-signed in RAM, the same certificate for IRC over TLS;
  - HTTP redirects to HTTPS; HSTS only with a trusted certificate on a domain;
  - self-signed fallback and a ≥ 1 h back-off on ACME failures, opt-in cache;
  - `fugace status` to check the state.
- **Files and images** in RAM (10 MB each, 256 MB in total, 1 hour):
  - JPEG/PNG/animated GIF/WebP re-encoded, which strips EXIF and GPS while keeping orientation;
  - decompression bombs refused before decoding, other files served as downloads;
  - uploads bound to a live connection (`fugace/UPLOAD` token in `005`).
- **Hardening**:
  - `FUGACE_PASSWORD`, `FUGACE_TRUSTED_PROXIES`;
  - per-source connection limits (IRC/WS and HTTP);
  - no disk writes, no output besides the startup line.
- **Container**: `scratch` image of about 13 MB, multi-arch amd64/arm64, non-root, read-only rootfs, no capabilities, SBOM, provenance and keyless cosign signature.
- **Verification**:
  - fuzzing of every parser;
  - real WeeChat and irssi clients;
  - headless Chromium (XSS, CSP, axe);
  - ACME against pebble;
  - load, flood and slowloris runs.

[Unreleased]: https://github.com/gabltz/fugace/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/gabltz/fugace/releases/tag/v0.3.0
[0.2.0]: https://github.com/gabltz/fugace/releases/tag/v0.2.0
[0.1.0]: https://github.com/gabltz/fugace/releases/tag/v0.1.0
