# fugace

Chat éphémère, sans compte, sans logs, compatible IRC, avec partage d'images et de fichiers.
Un binaire Go statique, une image `scratch` d'environ 13 Mo, zéro configuration. Supprimez le conteneur : tout a disparu.

- Un pseudo suffit : pas de compte, pas d'e-mail.
- Client web intégré (HTTPS) et clients IRC natifs (WeeChat, irssi, HexChat…) sur le même serveur.
- Rien n'est écrit sur disque, rien n'est journalisé. L'historique des salons et les fichiers vivent en RAM.
- Les images sont ré-encodées (EXIF/GPS supprimés) ; aucune IP n'est visible des autres participants ; DCC est bloqué.

## Démarrage

```sh
docker run -d --name fugace --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  --log-driver none -p 80:8080 -p 443:8443 -p 6667:6667 -p 6697:6697 \
  ghcr.io/gabltz/fugace:0.1.0
```

Ou, depuis ce dépôt, avec toutes les protections préréglées (mémoire sans swap, pids, ulimits) :

```sh
docker compose up -d --build
```

Ensuite :

- Web : `https://<ip-ou-domaine>`. Le port 80 redirige vers 443. Aucun reverse proxy n'est nécessaire.
- IRC : `<hôte>:6697` (TLS, même certificat que le web) ou `<hôte>:6667` (clair).
- Le salon `#general` existe d'emblée ; le client web le rejoint automatiquement.

Repartir de zéro (historique, fichiers, certificat autosigné) : `docker compose up -d --force-recreate`.

## Certificat HTTPS

| Situation | Ce qu'il faut faire | Résultat |
|-----------|---------------------|----------|
| Réseau local, accès par IP | rien | HTTPS autosigné, généré en RAM : le navigateur affiche un avertissement, à accepter une fois |
| Idem, en ajoutant l'IP au certificat | `FUGACE_HOST=192.168.1.20` | autosigné, avec l'IP dans ses noms |
| Domaine qui pointe vers l'hôte | `FUGACE_HOST=chat.exemple.fr` | certificat Let's Encrypt obtenu au démarrage, renouvelé à mi-vie |
| Votre propre certificat | monter `./certs:/certs:ro` (`tls.crt`, `tls.key`) | utilisé en priorité, rechargé s'il change |
| Un proxy devant malgré tout | `FUGACE_TLS=off` | l'application est servie en HTTP sur le port 8080 |

Aucun certificat public n'est demandé pour une adresse IP : l'accès par IP est prévu pour le réseau local.

Pour un domaine :

- **Ports** : les ports 80 et 443 de l'hôte doivent être joignables depuis Internet. Derrière une box, redirigez-les (NAT) vers la machine.
- **Quotas** : Let's Encrypt limite à 5 certificats par semaine pour le même ensemble de noms.
  - À chaque recréation du conteneur, un nouveau certificat est demandé.
  - En cas d'échec ou de quota atteint, fugace repasse aussitôt en autosigné et réessaie au plus tôt une heure plus tard, en respectant le `Retry-After`.
  - Pour éviter de consommer le quota, `FUGACE_CERT_CACHE=/cache` avec un volume conserve le compte et le certificat entre deux recréations. Les fichiers sont en 0600 et ne contiennent aucune donnée utilisateur. C'est la seule écriture disque possible, et seulement si cette variable est définie.

L'état se consulte à la demande, puisque rien n'est journalisé :

```sh
docker exec fugace /fugace status
# tls:         acme
# certificate: valid until 2027-01-17T09:12:44Z (publicly trusted)
# connections: 12
```

## Salons, historique et administration

- **Salons** : seul l'admin crée des salons. `FUGACE_CHANNELS` (défaut `#general`) fixe ceux qui existent au démarrage. Ils persistent même vides.
- **Historique** : chaque salon garde en RAM tout ce qui y a été dit depuis le démarrage, dans un budget global (`FUGACE_HISTORY_MAX_MB`, 64 par défaut, le plus ancien part en premier). L'historique est rejoué à chaque arrivée avec l'heure d'origine. Rien pour les messages privés. Redémarrer le conteneur efface tout.
- **Admin** : définissez `FUGACE_ADMIN_PASSWORD` (8 caractères au moins), puis :
  - **web** : « Options » → « Mot de passe admin » ;
  - **IRC** : `/oper admin <mot de passe>` ;
  - l'admin apparaît `@` dans les salons ;
  - `/join #nouveau` crée un salon ;
  - `/delchan #salon [raison]` le supprime, avec son historique ;
  - `/clearhistory #salon` vide son historique.
- **Accès** : `FUGACE_PASSWORD` protège l'entrée de tout le serveur (`PASS` IRC, champ « Mot de passe du serveur » côté web).

## Fichiers et images

- **Client web** : bouton 📎, glisser-déposer, ou coller une image (Ctrl+V). Le lien est posté dans la conversation, avec un aperçu pour les images.
- **Limites** : 10 Mo par fichier, 256 Mo au total en RAM (le plus ancien est évincé), une heure de vie.
- **Traitement** : les images JPEG, PNG, GIF (animé) et WebP sont ré-encodées, ce qui supprime EXIF, GPS et commentaires. Tout le reste (SVG compris) est servi en téléchargement, jamais affiché.

Depuis un client IRC, le serveur annonce dans le `005` un jeton d'envoi propre à votre connexion : `fugace/UPLOAD=<jeton>`, visible dans le tampon serveur. Il reste valable tant que vous êtes connecté :

```sh
curl -k -H "Authorization: Bearer <jeton>" --data-binary @photo.jpg "https://<hôte>/upload?name=photo.jpg"
```

La réponse JSON contient l'URL à coller dans le salon.

## Configuration (toute optionnelle)

| Variable | Défaut | Rôle |
|----------|--------|------|
| `FUGACE_HOST` | vide | Domaine(s) pour Let's Encrypt ; IP ajoutées au certificat autosigné ; base des URLs de fichiers |
| `FUGACE_TLS` | `auto` | `auto` (monté > ACME > autosigné) ou `off` (derrière un proxy) |
| `FUGACE_CERT_CACHE` | vide | Volume pour garder le certificat ACME entre deux recréations |
| `FUGACE_PASSWORD` | vide | Mot de passe d'entrée du serveur |
| `FUGACE_ADMIN_PASSWORD` | vide | Mot de passe admin (vide = pas d'admin) |
| `FUGACE_CHANNELS` | `#general` | Salons créés au démarrage |
| `FUGACE_HISTORY_MAX_MB` | `64` | Budget RAM de l'historique (`0` = désactivé) |
| `FUGACE_MAX_UPLOAD_MB` / `FUGACE_FILES_MAX_MB` / `FUGACE_FILE_TTL` | `10` / `256` / `1h` | Fichiers |
| `FUGACE_PUBLIC_URL` | vide | Base des URLs de fichiers si l'hôte vu par les clients diffère |
| `FUGACE_TRUSTED_PROXIES` | vide | CIDR des proxys dont on lit `X-Forwarded-For` (limites par source uniquement) |

Liste complète, ports internes et règles de validation : [`docs/SPEC.md` §7](docs/SPEC.md). Toute valeur invalide arrête le démarrage avec un message clair (code 2), qui ne recopie jamais la valeur.

## Limites à connaître

- Votre client IRC journalise probablement en local : c'est hors de portée du serveur.
- Le port 6667 est en clair ; préférez 6697 ou le web.
- **En RAM pendant que le conteneur tourne** : l'historique des salons depuis le démarrage et les fichiers de l'heure écoulée. Un administrateur root de l'hôte peut les lire.
- **Certificat autosigné** : quelqu'un en position d'intermédiaire pourrait s'interposer si l'avertissement du navigateur est accepté sans vérification.
- Les certificats Let's Encrypt apparaissent dans les journaux publics Certificate Transparency : votre domaine y est associé à un service TLS.
- **Reverse proxy** (`FUGACE_TLS=off`) : désactivez ses logs d'accès pour ce site, sinon les IP et les URLs de fichiers y sont journalisées. Déclarez-le aussi dans `FUGACE_TRUSTED_PROXIES`, sinon toutes les connexions semblent venir de lui.
- Sans `FUGACE_PASSWORD`, n'importe qui peut entrer.
- Limites anti-abus (connexions par source, débits, tailles) : [`docs/ARCHITECTURE.md` §6](docs/ARCHITECTURE.md) ; mesures de charge : [`docs/BENCH.md`](docs/BENCH.md).

## Vérifier l'image

Chaque version publiée est multi-architecture (amd64, arm64), signée sans clé avec cosign, et accompagnée d'un SBOM et d'une attestation de provenance :

```sh
cosign verify ghcr.io/gabltz/fugace:0.1.0 \
  --certificate-identity-regexp 'https://github.com/gabltz/fugace/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

docker buildx imagetools inspect ghcr.io/gabltz/fugace:0.1.0 --format '{{json .SBOM}}'
```

## Développement

`make check` (lint, tests `-race`, govulncheck), `make fuzz`, `make smoke`, `make clients` (WeeChat et irssi réels), `make browser` (Chromium headless), `make acme` (pebble). Conventions : [`AGENTS.md`](AGENTS.md) ; décisions : [`docs/adr/`](docs/adr/).

## Sécurité

Voir [`SECURITY.md`](SECURITY.md) et [`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md).
