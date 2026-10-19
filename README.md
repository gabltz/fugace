# fugace

Chat éphémère, sans compte, sans logs, compatible IRC, avec partage d'images et de fichiers.
Un binaire Go statique, une image `scratch`, zéro configuration. Supprimez le conteneur : tout a disparu.

> 🚧 En développement — voir [`docs/ROADMAP.md`](docs/ROADMAP.md).

## Démarrage

```sh
docker compose up -d --build
```

- Web : `https://<ip-ou-domaine>` (le port 80 redirige vers 443)
- IRC : `<ip>:6667` (clair) ou `<ip>:6697` (TLS, même certificat que le web)

Aucun reverse proxy nécessaire : l'image sert elle-même le web en HTTPS, même sur une IP nue.

### Certificat

| Situation | Ce qu'il faut faire | Résultat |
|-----------|---------------------|----------|
| Rien | rien | HTTPS autosigné (avertissement navigateur) |
| IP publique joignable sur 80/443 | `FUGACE_HOST=203.0.113.10` | Certificat Let's Encrypt pour l'IP (6 jours, renouvelé automatiquement) |
| Domaine pointant vers l'hôte | `FUGACE_HOST=chat.exemple.fr` | Certificat Let's Encrypt classique |
| Ton propre certificat | monter `./certs:/certs:ro` (`tls.crt`, `tls.key`) | Utilisé en priorité |
| Proxy devant quand même | `FUGACE_TLS=off` | HTTP simple sur 8080 |

Derrière une box/NAT, redirige 80 et 443 vers l'hôte. Let's Encrypt limite à 5 certificats par semaine pour le même ensemble d'identifiants : si tu recrées le conteneur plus souvent, il repasse en autosigné jusqu'à ce que le quota se libère (ou active `FUGACE_CERT_CACHE` avec un volume).

État TLS : `docker exec fugace /fugace status`.

Repartir de zéro :

```sh
docker compose up -d --build --force-recreate
```

## Principes

- Un pseudo suffit (`/nick`), pas de compte.
- Historique des salons en RAM depuis le démarrage, effacé au redémarrage ; aucun historique des messages privés.
- Rien n'est écrit sur disque ; les fichiers vivent 1 h en RAM puis disparaissent.
- Les images sont ré-encodées (métadonnées EXIF/GPS supprimées).
- Aucune IP n'est visible des autres participants ; DCC est bloqué.

## Limites à connaître

- Votre client IRC journalise probablement en local : c'est hors de portée du serveur.
- Le port 6667 est en clair ; préférez 6697 ou le web derrière HTTPS.
- Si vous ajoutez un reverse proxy devant (`FUGACE_TLS=off`), désactivez ses logs d'accès pour ce site.
- Sans `FUGACE_PASSWORD`, n'importe qui peut entrer.

## Configuration optionnelle

Voir [`docs/SPEC.md` §7](docs/SPEC.md). Rien n'est requis.

## Sécurité

Voir [`SECURITY.md`](SECURITY.md) et [`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md).
