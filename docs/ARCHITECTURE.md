# fugace — Architecture

## 1. Choix du langage : Go

Retenu face à Rust pour ce projet précis (détail dans `adr/0001-decisions.md`) :

- Binaire statique `CGO_ENABLED=0` → image `scratch`, aucune libc, aucune surface OS.
- Stdlib suffisante pour 95 % du besoin : `net`, `net/http`, `crypto/tls`, `image/*`, `embed`, fuzzing natif.
- Concurrence goroutines/channels naturelle pour un serveur de chat (1 lecteur + 1 écrivain par client).
- Dépendances tierces : **3 au maximum**, et exactement 3 : `github.com/coder/websocket` (ADR 0005), `golang.org/x/crypto/acme` (ADR 0007), `golang.org/x/image/webp` (ADR 0008).

## 2. Vue d'ensemble

```
                ┌──────────────── binaire fugace ─────────────────┐
 hôte:80  → 8080 ┤ http : 301→https, ACME http-01, /healthz        │
 hôte:443 → 8443 ┤ https ─┬─ web (statique embarqué)               │
                 │        ├─ transport/ws ─┐                       │
 hôte:6667→ 6667 ┤ transport/tcp ──────────┼─► server (cœur IRC)   │
 hôte:6697→ 6697 ┤ tcp + TLS ──────────────┘   clients, salons     │
                 │        └─ files (upload/serve, jetons)          │
                 │ tlsmgr : monté > ACME (domaines) > autosigné    │
 127.0.0.1:9090  ┤ status interne (non publié)                     │
                 └─────────────────────────────────────────────────┘
                         tout en RAM, rien sur disque
```

**Décision clé** : le client web parle IRC sur WebSocket. Il n'y a donc **qu'un seul protocole et un seul parseur** côté serveur. Le WebSocket n'est qu'un transport de plus, au même titre que TCP et TLS. Bénéfices : surface d'attaque réduite, un seul jeu de tests de conformité, et compatibilité avec d'autres clients web IRC (gamja, Kiwi) en bonus.

## 3. Arborescence

```
cmd/fugace/                 point d'entrée : configuration, écoutes, sous-commandes healthcheck/status/version
internal/config/            lecture et validation des variables d'environnement
internal/irc/               parseur/sérialiseur de messages, numerics, validation pseudo/salon
internal/server/            état (clients, salons, historique), dispatch des commandes, diffusion, admin
internal/transport/         IRC sur TCP/TLS (tcp.go) et sur WebSocket (ws.go), X-Forwarded-For
internal/tlsmgr/            certificat unique (monté / ACME / autosigné), renouvellement, challenges, cache opt-in
internal/status/            statut interne sur 127.0.0.1:9090 (mode TLS, expiration, dernier échec ACME, connexions)
internal/files/             store RAM (TTL, budget, éviction), pipeline image, jetons d'envoi, handlers HTTP
internal/web/               serveur HTTP, en-têtes de sécurité, redirection, HSTS, statique embarqué
internal/limits/            token buckets, compteur de connexions par source hachée, listener limitant
web/                        client web embarqué (go:embed) : index.html, app.js, prefs.js, style.css, police
scripts/                    smoke, clients IRC réels, navigateur, pebble (ACME), charge, contrôles CI
```

## 4. Modèle de concurrence

- Chaque client : une goroutine lectrice (parse → dispatch) et une goroutine écrivaine (draine une file bornée de 256 lignes).
- L'état global (pseudos, salons, membres) est protégé par un `sync.RWMutex` unique ; les sections critiques ne font **jamais** d'I/O : on calcule la liste des destinataires sous verrou, on relâche, puis on pousse dans leurs files.
- Envoi non bloquant : file pleine → client marqué pour coupure (`SendQ exceeded`). Un client lent ne peut jamais bloquer les autres.
- Arrêt propre sur SIGTERM : NOTICE de fermeture, fermeture des listeners, délai max 5 s.
- `-race` obligatoire en CI.

## 5. Règles d'implémentation non négociables

1. **Aucune écriture disque** : pas de `os.Create`, pas de `os.TempDir`, pas de `ParseMultipartForm` (qui déverse sur disque au-delà du seuil mémoire). L'upload lit un corps brut via `http.MaxBytesReader` dans un `bytes.Buffer` borné.
2. **Aucun log** : seuls `cmd/fugace` peut écrire sur stdout/stderr, et uniquement : une ligne de démarrage (version, ports, mode TLS) et les erreurs fatales de démarrage sans donnée utilisateur. Imposé par `forbidigo` dans `.golangci.yml`. `http.Server.ErrorLog` redirigé vers `io.Discard`, logger du client ACME neutralisé. L'état opérationnel (TLS, ACME) se consulte via `docker exec fugace /fugace status`, qui interroge `127.0.0.1:9090` : rien n'est journalisé, tout est interrogeable à la demande.
3. **Aucune IP stockée** : le rate-limit par source utilise `HMAC-SHA256(clé_aléatoire_au_boot, ip)` tronqué, en RAM, avec expiration. La clé n'existe qu'en mémoire.
4. **Panics** : `recover` par connexion et par requête HTTP, sans imprimer la valeur ni la stack (pourrait contenir des données utilisateur) ; on ferme la connexion.
5. **Timeouts HTTP** : `ReadHeaderTimeout 5s`, `ReadTimeout 30s` (upload), `WriteTimeout 30s`, `IdleTimeout 60s`, `MaxHeaderBytes 16 Ko`.
6. **Aléa** : `crypto/rand` uniquement pour IDs, jetons et clés.
7. **Comparaisons de secrets** : `crypto/subtle.ConstantTimeCompare`.
8. **Clés TLS** : en RAM uniquement (sauf `FUGACE_CERT_CACHE` explicitement défini, fichiers en 0600). Clés ECDSA P-256. TLS 1.2 minimum, suites Go par défaut.
9. **ACME** : identifiants limités à `FUGACE_HOST`, jamais déduits d'une requête. Échec → repli autosigné + backoff ≥ 1 h (ou `Retry-After`). Le handler `http-01` ne répond qu'aux jetons en cours, et uniquement sur le port HTTP.

## 6. Limites anti-abus (valeurs par défaut, constantes nommées dans `internal/limits`)

| Limite | Valeur |
|--------|--------|
| Connexions simultanées totales | 1000 |
| Connexions par source (IP hachée) | 10 (IRC/WS) ; 32 en HTTP/HTTPS, 1024 au total (ADR 0009) |
| Messages par client | burst 10, puis 2/s |
| Commandes JOIN par client | burst 5, puis 1/5 s |
| Changements de pseudo | burst 3, puis 1/10 s |
| Salons par client | 20 |
| Membres par salon | 500 |
| Salons au total | 200 |
| Uploads par client | burst 3, puis 1/10 s |
| Taille ligne IRC | 512 octets |
| Message WebSocket | 4 Ko |

## 7. HTTP

| Route | Méthode | Rôle |
|-------|---------|------|
| `/` | GET | Client web |
| `/static/*` | GET | Assets embarqués (cache long, ETag) |
| `/ws` | GET (upgrade) | IRC sur WebSocket, sous-protocole `text.ircv3.net`, vérification `Origin` = même origine |
| `/upload` | POST | Upload (jeton Bearer) |
| `/f/{id}/{name}` | GET, DELETE | Lecture / suppression |
| `/healthz` | GET | `200 ok`, aucune info (servi aussi sur HTTP sans redirection) |
| `/.well-known/acme-challenge/{token}` | GET | Port HTTP uniquement, challenges ACME en cours |
| tout le reste sur HTTP | * | `301` vers `https://<host><path>` (sauf `FUGACE_TLS=off`) |

En-têtes globaux : `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob:; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Permissions-Policy: camera=(), microphone=(), geolocation=()`, `Cross-Origin-Opener-Policy: same-origin`, `Cross-Origin-Resource-Policy: same-origin`. Pas de `Server` ni d'en-tête de version. `Strict-Transport-Security: max-age=31536000` uniquement quand le certificat servi est valide (monté ou ACME) et l'hôte un nom de domaine (HSTS est ignoré sur IP), jamais en autosigné.

Fichiers servis : en plus, `Content-Security-Policy: default-src 'none'; img-src 'self'; sandbox`, `Cache-Control: private, max-age=<ttl restant>`, et `Content-Disposition: attachment` pour tout ce qui n'est pas une image ré-encodée.

## 8. Conteneur

- Build multi-étapes, `golang:1.27-alpine` → `scratch`, image épinglée par digest. Les racines CA (`ca-certificates.crt`) sont copiées depuis l'étape de build : nécessaires pour joindre l'API ACME en HTTPS.
- Ports internes non privilégiés (8080/8443/6667/6697), publiés en 80/443/6667/6697 par compose : aucune capability nécessaire.
- Seul trafic sortant : l'API ACME, et seulement si `FUGACE_HOST` est défini.
- Binaire : `-trimpath -buildvcs=false -ldflags "-s -w -buildid= -X main.version=..."` (build reproductible).
- Utilisateur 65532, rootfs en lecture seule, `cap_drop: ALL`, `no-new-privileges`, `logging: none`, `ulimit core=0`, `memswap_limit = mem_limit` (la RAM n'est jamais swappée sur disque).
- Healthcheck via `/fugace healthcheck` (pas de curl dans `scratch`).
