# Changelog

Format : [Keep a Changelog](https://keepachangelog.com/fr/1.1.0/), versions [SemVer](https://semver.org/lang/fr/).

## [0.1.0] - 2026-10-19

Première version.

### Ajouté
- **Serveur IRC** :
  - enregistrement (`CAP` 302 avec `echo-message`, `server-time`, `message-tags`) ;
  - salons, messages privés, `WHO`/`WHOIS` sans aucune IP ni temps d'inactivité, `MODE` inertes ;
  - DCC bloqué ;
  - anti-flood par ralentissement, files d'envoi bornées, délais d'enregistrement (30 s) et de PING (180 s).
- **Historique** de salon en RAM depuis le démarrage (budget global, rejeu au JOIN avec l'heure d'origine) et **administration** des salons par `OPER` : création, `DELCHAN`, `CLEARHISTORY` (ADR 0003).
- **Client web** vanilla (36 Ko, sans build ni dépendance) via IRC sur WebSocket (`text.ircv3.net`) :
  - salons, MP, membres, commandes, reconnexion ;
  - thème clair/sombre, mobile ;
  - CSP stricte sans inline ; accessibilité vérifiée par axe-core.
- **HTTPS intégré** sans reverse proxy :
  - priorité certificat monté > Let's Encrypt pour les domaines de `FUGACE_HOST` > autosigné en RAM ;
  - accès par IP : autosigné (réseau local, ADR 0007) ;
  - même certificat pour IRC TLS (6697) ;
  - HTTP redirige vers HTTPS ;
  - HSTS seulement avec un certificat de confiance sur un domaine ;
  - repli et backoff ≥ 1 h en cas d'échec ACME, cache opt-in ;
  - `fugace status` pour consulter l'état.
- **Fichiers et images** en RAM (10 Mo, 256 Mo au total, 1 h) :
  - ré-encodage JPEG/PNG/GIF animé/WebP, qui supprime EXIF et GPS en conservant l'orientation ;
  - bombes de décompression refusées avant décodage ;
  - tout le reste servi en téléchargement ;
  - envoi lié à une connexion vivante (jeton `fugace/UPLOAD` dans le `005`).
- **Durcissement** :
  - `FUGACE_PASSWORD`, `FUGACE_TRUSTED_PROXIES` ;
  - limites de connexions par source hachée (IRC/WS et HTTP) ;
  - aucune écriture disque, aucune sortie hors ligne de démarrage.
- **Conteneur** :
  - image `scratch` d'environ 13 Mo, multi-arch amd64/arm64 ;
  - non-root, rootfs en lecture seule, aucune capability ;
  - SBOM, provenance, signature cosign sans clé.
- **Vérifications** :
  - fuzzing des parseurs (lignes IRC, configuration, images, noms de fichiers, `X-Forwarded-For`) ;
  - clients WeeChat et irssi réels ;
  - Chromium headless (XSS, CSP, axe) ;
  - ACME contre pebble ;
  - charge, flood et slowloris (`docs/BENCH.md`).

[0.1.0]: https://github.com/gabltz/fugace/releases/tag/v0.1.0
