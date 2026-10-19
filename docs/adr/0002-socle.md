# ADR 0002 — Choix du socle (M0)

Contexte → décision → conséquences, une entrée par point que la SPEC ne tranchait pas.

## D10. Cible du healthcheck avant M3
- **Contexte** : le statut interne `127.0.0.1:9090` n'arrive qu'en M3, mais l'image doit avoir un healthcheck dès M0.
- **Décision** : `fugace healthcheck` interroge `http://127.0.0.1:<port de FUGACE_HTTP_ADDR>/healthz` (adresse d'écoute si elle n'est pas « non spécifiée »), délai 2 s, aucune sortie : seul le code retour compte.
- **Conséquences** : rien n'est écrit dans l'état Docker du conteneur ; bascule vers le statut interne en M3.

## D11. Outils et images épinglés
- **Contexte** : `govulncheck@latest`, `docker/dockerfile:1` et `apk add ca-certificates` récupéraient du code non figé à chaque build (T12).
- **Décision** : `govulncheck` épinglé (`GOVULNCHECK_VERSION` dans le Makefile, réutilisé par la CI), frontend Dockerfile et image de build épinglés par digest, `apk add` supprimé (l'image `golang:*-alpine` contient déjà les racines CA ; le build échoue si elles manquent). Versions retenues : la dernière stable publiée depuis au moins deux semaines.
- **Conséquences** : builds reproductibles ; Dependabot propose les mises à jour (docker, actions, gomod).

## D12. Validation de la configuration
- **Contexte** : SPEC §7 impose une validation stricte sans détailler les règles.
- **Décision** :
  - `FUGACE_HOST` : 10 identifiants max, IP sans zone (ni non spécifiée, ni multicast) ou nom DNS LDH en minuscules, sans joker ; doublons fusionnés.
  - Adresses d'écoute : `hôte:port`, port 1–65535, hôte IP, `localhost` ou vide. Une variable vide réactive le défaut, sauf `FUGACE_IRC_ADDR`/`FUGACE_IRCS_ADDR` où vide = désactivé.
  - `FUGACE_ACME_DIRECTORY` : `https` uniquement. `FUGACE_PUBLIC_URL` : `http(s)` sans identifiants, requête ni fragment.
  - `FUGACE_PASSWORD` : 128 octets max, aucun caractère de contrôle.
  - `FUGACE_MAX_UPLOAD_MB` 1–100, `FUGACE_FILES_MAX_MB` 1–4096 et ≥ taille max, `FUGACE_FILE_TTL` 1 min–24 h.
  - Toutes les erreurs sont rapportées ensemble, sortie code 2. Les messages nomment la variable et la règle, **jamais la valeur** (qui peut être un secret).
- **Conséquences** : un démarrage réussi garantit des valeurs bornées pour la suite du code.

## D13. « `docker logs` vide » avec `logging: none`
- **Contexte** : le critère M0 demande `docker logs` vide alors que compose désactive le pilote de logs.
- **Décision** : avec `logging: none`, `docker logs` répond « logging driver does not support reading » : rien n'est conservé, ce qui satisfait le critère. Le binaire n'écrit de toute façon qu'une ligne de démarrage sans donnée utilisateur.
- **Conséquences** : les tests (smoke M5) vérifient la sortie du processus avec le pilote par défaut pour prouver qu'elle se limite à cette ligne.

## D14. Traceback désactivé
- **Contexte** : une panique non récupérée imprime les piles de toutes les goroutines, qui peuvent contenir des données utilisateur.
- **Décision** : `debug.SetTraceback("none")` au démarrage, en plus du `recover` par connexion et par requête.
- **Conséquences** : un crash inattendu est moins diagnostiquable en production ; en développement, `GOTRACEBACK` reprend la main.
