# fugace — Roadmap et suivi

Règles : un jalon = une branche `mX-...` = une PR. On ne commence un jalon que si le précédent est mergé, CI verte. Cocher les cases dans la PR qui les réalise.

## M0 — Socle du dépôt

- [x] `go mod init` (Go 1.27), `cmd/fugace/main.go` qui démarre un HTTP `/healthz` et gère `fugace healthcheck`
- [x] `internal/config` : lecture + validation des variables de SPEC §7, tests table-driven
- [x] Makefile fonctionnel (`build test lint vuln fuzz check docker smoke`)
- [x] Dockerfile et compose.yaml fournis : vérifier qu'ils buildent, image < 20 Mo
- [x] Épingler toutes les actions GitHub par SHA complet (commentaire `# vX.Y.Z` à côté) et les images de base par digest
- [x] CI verte de bout en bout
- **Acceptation** : `docker compose up -d --build` → `curl http://localhost/healthz` = `ok` ; `docker logs` vide ; `docker diff` vide.

## M1 — Cœur IRC (TCP)

- [x] `internal/irc` : parseur/sérialiseur (tags, préfixe, params, trailing), validation pseudos/salons, constantes numerics
- [x] Fuzz du parseur (`FuzzParse`) + round-trip parse/serialize
- [x] Enregistrement NICK/USER/CAP/PASS, 001–005, 422, timeout 30 s
- [x] PING/PONG bidirectionnel, timeout 180 s
- [x] JOIN/PART/PRIVMSG/NOTICE/QUIT/NAMES/TOPIC/LIST/WHO/WHOIS/MODE/MOTD/LUSERS/VERSION/TIME
- [x] Blocage CTCP DCC
- [x] Caps `echo-message`, `server-time`
- [x] Historique de salon en RAM (budget, rejeu au JOIN) et admin (`OPER`, salons admin seulement, `DELCHAN`, `CLEARHISTORY`) — ADR 0003
- [x] Files d'envoi bornées, SendQ exceeded
- [x] Limites anti-abus (ARCHITECTURE §6), rate-limit par IP hachée
- [x] Tests d'intégration en Go : N clients TCP simulés, scénario complet
- **Acceptation** : WeeChat et irssi se connectent, rejoignent `#general`, échangent, changent de pseudo, MP OK. Aucun log.

## M2 — WebSocket + client web

- [x] Transport WS (`text.ircv3.net`), vérification Origin, limite 4 Ko
- [x] Client web vanilla : écran pseudo, salons/MP, membres, commandes slash, reconnexion, thème, mobile
- [x] Auto-join `#general` côté client web
- [x] Affichage de l'historique rejoué, champ « mot de passe admin », commandes `/delchan` `/clearhistory`
- [x] Assets embarqués, ETag, CSP stricte, aucun inline
- [x] Tests : web ↔ IRC TCP dans le même salon
- **Acceptation** : un utilisateur web et un utilisateur WeeChat discutent dans `#general` ; lighthouse/axe sans erreur bloquante ; poids front < 60 Ko.

## M3 — HTTPS autonome (web servi directement sur l'hôte, y compris sur IP)

- [ ] ADR : choix et vérification du client ACME (identifiants IP + profil `shortlived` + `http-01`/`tls-alpn-01`, ARI)
- [ ] `internal/tlsmgr` : sources monté (`/certs`, rechargement) > ACME (`FUGACE_HOST`) > autosigné RAM ; `GetCertificate` partagé HTTPS + IRC 6697 ; SNI vide → cert par défaut
- [ ] Listener HTTP : `/healthz`, `/.well-known/acme-challenge/*`, 301 vers HTTPS pour le reste ; `FUGACE_TLS=off` sert l'appli en clair
- [ ] ACME : compte éphémère sans e-mail, profil `shortlived` pour les IP, renouvellement automatique, backoff ≥ 1 h / `Retry-After`, repli autosigné
- [ ] `FUGACE_CERT_CACHE` opt-in (fichiers 0600), aucune écriture sinon
- [ ] HSTS uniquement avec cert valide sur nom de domaine
- [ ] `internal/status` sur 127.0.0.1:9090 + sous-commande `fugace status` ; healthcheck dessus
- [ ] Tests d'intégration contre **pebble** (serveur ACME de test) en conteneur dans la CI : domaine, IP, échec, `rateLimited`
- **Acceptation** : sans variable → `https://<ip>` répond (autosigné) et `http://<ip>` redirige ; avec `FUGACE_HOST=<ip publique>` sur une machine joignable → certificat Let's Encrypt valide servi sur `https://<ip>` et sur 6697 ; `docker diff` vide ; `fugace status` indique le mode et l'expiration.

## M4 — Fichiers et images

- [ ] `internal/files` : store RAM (TTL, budget, éviction LRU par âge), IDs 128 bits
- [ ] Jeton d'upload par connexion annoncé en 005, invalidé au QUIT
- [ ] `POST /upload`, `GET|DELETE /f/{id}/{name}`
- [ ] Sniff magic bytes, plafond pixels, ré-encodage jpeg/png/gif (animé)/webp→png
- [ ] En-têtes de sécurité fichiers (ARCHITECTURE §7)
- [ ] Client web : bouton, drag & drop, coller, aperçu same-origin, barre de progression
- [ ] Fuzz du pipeline image (`FuzzSanitize`)
- **Acceptation** : une photo avec GPS uploadée ressort sans EXIF ; un SVG avec `<script>` est téléchargé et jamais rendu ; jeton invalide après QUIT ; fichiers expirés à TTL.

## M5 — Durcissement

- [ ] `FUGACE_PASSWORD` (IRC PASS + invite web), comparaison temps constant
- [ ] `FUGACE_TRUSTED_PROXIES` pour `X-Forwarded-For`
- [ ] Revue complète du THREAT_MODEL : chaque ligne a son test, cocher la colonne
- [ ] Test de charge (500 clients, 50 msg/s) : RAM, CPU, latence documentées dans `docs/BENCH.md`
- [ ] Test slowloris + flood
- [ ] `scripts/smoke.sh` étendu : upload, `docker diff` vide, `docker logs` vide
- [ ] Optionnel : faire passer un sous-ensemble pertinent de la suite `irctest` et documenter les écarts
- **Acceptation** : toutes les lignes du modèle de menace testées ; Trivy 0 HIGH/CRITICAL.

## M6 — Release v0.1.0

- [ ] README utilisateur complet (démarrage, compose, ports 80/443 et NAT, `FUGACE_HOST` sur IP ou domaine, quotas Let's Encrypt, mode `FUGACE_TLS=off`, limites assumées)
- [ ] CHANGELOG
- [ ] Tag `v0.1.0` → workflow release : image multi-arch amd64/arm64 sur GHCR, SBOM, provenance, signature cosign
- [ ] Vérification `cosign verify` documentée dans le README
- **Acceptation** : `docker run ghcr.io/gabltz/fugace:0.1.0` fonctionne sur amd64 et arm64.

## Backlog (post v0.1)

- Rendu des codes de formatage IRC (gras/italique/couleurs)
- Indicateur de saisie (`+typing`)
- Jeton ISUPPORT compatible avec les clients supportant un hébergement de fichiers standardisé
