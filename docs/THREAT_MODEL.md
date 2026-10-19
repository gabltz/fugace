# fugace — Modèle de menace

## Actifs

1. Contenu des messages et fichiers.
2. Adresses IP et métadonnées des participants (qui parle à qui, quand).
3. Disponibilité du service.
4. L'hôte qui exécute le conteneur.

## Acteurs

| Acteur | Capacités |
|--------|-----------|
| A1 Visiteur anonyme | Accès réseau aux ports exposés, sans pseudo |
| A2 Participant malveillant | Pseudo valide, client IRC modifié, scripts |
| A3 Uploader malveillant | Fichiers forgés (polyglottes, bombes, SVG piégés) |
| A4 Observateur réseau | Écoute passive entre client et serveur |
| A5 Saisie a posteriori | Accès à l'hôte ou au conteneur **après** les faits |

## Menaces et parades

| ID | Menace | Parade | Test |
|----|--------|--------|------|
| T1 | A5 retrouve des messages/IP sur disque ou dans les logs Docker | Aucune écriture disque, rootfs RO, `logging: none`, forbidigo, pas de swap | Test d'intégration : exécution + upload puis `docker diff` vide, `docker logs` vide |
| T2 | A2 récupère l'IP d'un autre participant | Hostmask masqué, WHOIS/WHO sans IP, DCC bloqué, aucun préchargement de lien externe dans le client web | Tests unitaires WHOIS/WHO/DCC |
| T3 | A2 injecte du HTML/JS dans le client web | Rendu `textContent` uniquement, CSP stricte sans inline, pas d'`innerHTML` (règle de revue + grep CI) | Test navigateur headless avec charges XSS classiques |
| T4 | A3 sert du HTML/SVG exécutable depuis notre origine | Sniff du contenu, ré-encodage des images, tout le reste en `attachment` + `octet-stream` + `nosniff` + CSP `sandbox` | Tests avec polyglotte GIF/HTML, SVG avec script |
| T5 | A3 bombe de décompression (PNG 50000×50000) | `DecodeConfig` + plafond 25 MP avant décodage | Test unitaire avec fichier forgé |
| T6 | A3 épuise la RAM via uploads | Taille max/fichier, budget global, éviction, rate-limit, jeton lié à une connexion | Test de charge |
| T7 | A1/A2 déni de service (flood, slowloris, connexions massives) | Timeouts, limites de §6 ARCHITECTURE, SendQ borné, limite par IP hachée | Test de charge + slowloris |
| T8 | A2 fait planter le parseur | Parseur fuzzé (go fuzz), recover par connexion | Corpus de fuzz en CI |
| T9 | A1 utilise fugace comme hébergeur de fichiers public | Upload impossible sans connexion IRC/WS vivante, TTL court | Test : jeton invalide après QUIT |
| T10 | A2 usurpe un pseudo via variantes de casse/Unicode | Pseudos ASCII stricts, CASEMAPPING=ascii | Tests unitaires |
| T11 | A4 lit le trafic | HTTPS intégré par défaut (HTTP = redirection), IRC TLS 6697 ; documenter que 6667 est en clair | Test : toute requête HTTP hors ACME/healthz → 301 |
| T12 | Chaîne d'approvisionnement | 3 dépendances max, `go.sum`, govulncheck, actions épinglées par SHA, image de base par digest, SBOM + provenance + signature cosign | CI |
| T13 | Évasion conteneur | `scratch`, non-root, cap_drop ALL, no-new-privileges, rootfs RO | Trivy + revue compose |
| T14 | CSWSH (WebSocket cross-site) | Vérification stricte de l'en-tête `Origin` | Test unitaire |
| T15 | A1 force des émissions ACME (Host/SNI arbitraires) pour épuiser les quotas ou obtenir un cert | Identifiants fixés par `FUGACE_HOST`, aucune émission à la demande | Test : SNI/Host inconnu → cert par défaut, aucun appel ACME |
| T16 | A5 récupère la clé privée TLS | Clé en RAM uniquement (cache opt-in en 0600) | `docker diff` vide sans cache |
| T17 | Quotas Let's Encrypt épuisés (recréations répétées) → service indisponible | Repli autosigné immédiat, backoff, cache opt-in, statut consultable | Test avec directory ACME simulé (pebble) renvoyant `rateLimited` |
| T18 | A1 abuse du handler `http-01` | Ne répond qu'aux jetons en cours, réponse de taille fixe | Test unitaire |

## Limites assumées (à écrire dans le README)

- « No-log » côté serveur ne contrôle pas les clients : la plupart des clients IRC journalisent localement par défaut.
- Le port 6667 est en clair.
- Tant que le conteneur tourne, la RAM contient les fichiers de l'heure écoulée ; un root sur l'hôte peut la lire.
- Aucun reverse proxy n'est nécessaire. Si on en met un quand même (`FUGACE_TLS=off`), il doit avoir `access_log off`, sinon les IP et URLs de fichiers y sont journalisées.
- Sans `FUGACE_HOST`, le certificat est autosigné : avertissement navigateur, et un attaquant en position de MITM pourrait s'interposer si l'utilisateur accepte aveuglément l'avertissement.
- Les certificats émis apparaissent dans les journaux Certificate Transparency publics (le domaine ou l'IP est donc publiquement associé à un service TLS).
- Sans `FUGACE_PASSWORD`, n'importe qui peut entrer : exposer publiquement en connaissance de cause.
