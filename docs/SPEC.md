# fugace — Spécification fonctionnelle

> Chat éphémère, sans compte, sans logs, compatible IRC, avec partage d'images/fichiers.
> Un seul binaire, un seul conteneur, zéro configuration. On supprime le conteneur : tout a disparu.

## 1. Objectifs

| # | Objectif | Mesure d'acceptation |
|---|----------|----------------------|
| O1 | Zéro configuration | `docker run -p 80:8080 -p 443:8443 -p 6667:6667 -p 6697:6697 image` suffit (HTTPS autosigné). Une seule variable optionnelle, `FUGACE_HOST`, pour un vrai certificat |
| O2 | Éphémère | Aucune écriture disque à l'exécution. Rootfs en lecture seule, aucun volume. Redémarrage = état vierge (seule exception opt-in : cache de certificat, §7) |
| O3 | No-log | Aucun contenu, IP, pseudo, user-agent n'est écrit sur stdout/stderr ni ailleurs |
| O4 | Compatible IRC | WeeChat, irssi, HexChat, Halloy, Textual se connectent, rejoignent un salon, discutent |
| O5 | Sans compte | Un pseudo suffit (`/nick`). Pas de mot de passe utilisateur, pas d'e-mail |
| O6 | Fichiers/images | Upload depuis le client web, aperçu des images, lien utilisable par les clients IRC |
| O7 | Léger | Image < 20 Mo, RAM au repos < 25 Mo, un seul binaire statique |
| O8 | Autonome | L'image sert elle-même le web en HTTPS sur 80/443, y compris sur une IP nue, sans reverse proxy |

## 2. Hors périmètre (v1)

- Persistance sur disque, historique des messages privés, comptes, NickServ/ChanServ, SASL.
- Fédération / multi-serveurs / clustering.
- Chiffrement de bout en bout.
- DCC (bloqué volontairement, voir §6).
- Opérateurs de salon, bans, kicks (seule gestion : un admin crée/supprime les salons et vide leur historique, voir ADR 0003).

## 3. Interfaces exposées

| Port | Protocole | Rôle |
|------|-----------|------|
L'image fait **tout** : elle sert le web directement sur l'hôte, en HTTPS, y compris quand on y accède par une simple IP. Aucun reverse proxy n'est requis.

| Port conteneur | Publié sur l'hôte (compose) | Protocole | Rôle |
|----------------|-----------------------------|-----------|------|
| 8080/tcp | 80 | HTTP | Redirection 301 vers HTTPS, challenge ACME `http-01`, `/healthz` |
| 8443/tcp | 443 | HTTPS | Client web, WebSocket IRC (`/ws`), upload, fichiers |
| 6667/tcp | 6667 | IRC clair | Clients IRC natifs |
| 6697/tcp | 6697 | IRC TLS | Même certificat que le HTTPS |
| 127.0.0.1:9090 | non publié | HTTP | Statut interne (`fugace status`, healthcheck) |

Les ports internes sont > 1024 : le processus tourne sans root et sans aucune capability ; c'est le mapping Docker qui expose 80/443.

### 3.1 Certificat TLS (ordre de priorité)

1. **Certificat monté** : `/certs/tls.crt` + `/certs/tls.key` présents → utilisés tels quels (rechargés si modifiés).
2. **ACME automatique** : si `FUGACE_HOST` est défini (domaine(s) et/ou IP publique(s), séparés par des virgules) → certificat Let's Encrypt obtenu au démarrage, en RAM.
   - Domaine : profil par défaut (ou `shortlived`).
   - **IP** : profil `shortlived` obligatoire (certificat de 160 h), challenges `http-01` ou `tls-alpn-01` uniquement (pas de DNS-01 pour une IP).
   - Renouvellement automatique tant que le conteneur tourne (ARI si disponible, sinon à mi-vie).
   - Compte ACME éphémère (clé générée en RAM), sans e-mail.
   - Seuls les identifiants listés dans `FUGACE_HOST` peuvent déclencher une émission : jamais d'émission à la demande d'après un en-tête `Host` ou un SNI.
3. **Autosigné** : sinon, ou si l'ACME échoue → certificat autosigné généré en RAM (SAN : `localhost`, `127.0.0.1`, identifiants de `FUGACE_HOST` s'il y en a). Le navigateur affichera un avertissement. En cas d'échec ACME, nouvelle tentative avec backoff (≥ 1 h, respect du `Retry-After`), sans boucle qui épuiserait les quotas.

Contraintes à documenter : pour l'ACME, les ports 80 et 443 de l'hôte doivent être joignables depuis Internet (redirections NAT si derrière une box) et `FUGACE_HOST` doit être l'IP **publique**. Let's Encrypt limite à 5 certificats par ensemble d'identifiants et par semaine : chaque recréation du conteneur consomme une émission, d'où le repli autosigné et le cache optionnel (§7).

Les clients qui se connectent par IP n'envoient pas de SNI : le `GetCertificate` doit renvoyer le certificat par défaut quand le SNI est vide.

### 3.2 Mode derrière proxy

`FUGACE_TLS=off` : pas de TLS, le port 8080 sert directement l'application (pour qui veut quand même mettre nginx/Traefik devant). Ce n'est pas le mode par défaut.

## 4. Modèle de chat

- **Pseudos** : regex `^[A-Za-z\[\]\\`_^{|}][A-Za-z0-9\[\]\\`_^{|}-]{0,29}$`, insensible à la casse (CASEMAPPING=ascii). Espace de noms unique web + IRC. Collision → `433 ERR_NICKNAMEINUSE`.
- **Hostmask** : `nick!~user@fugace` (TCP) / `nick!web@fugace` (web). L'IP n'apparaît **jamais**, ni dans les masques, ni dans WHOIS, ni dans WHO.
- **Salons** : préfixe `#`, longueur max 32, ASCII imprimable. Créés uniquement par l'admin (en les rejoignant) ou au démarrage (`FUGACE_CHANNELS`), persistants même vides. Topic modifiable par tout membre. L'admin (`OPER`) apparaît `@` partout (`PREFIX=(o)@`) ; aucun autre opérateur.
- **Salon par défaut** : `#general`, rejoint automatiquement par les clients web.
- **Messages privés** : `PRIVMSG nick :...` supportés.
- **Historique** : chaque salon garde en RAM tous ses messages depuis le démarrage (budget `FUGACE_HISTORY_MAX_MB`, éviction du plus ancien), rejoués à chaque JOIN. Rien pour les messages privés. L'admin peut vider l'historique (`CLEARHISTORY`) ou supprimer le salon (`DELCHAN`).

## 5. Compatibilité IRC (RFC 1459 / 2812, sous-ensemble + IRCv3 léger)

### 5.1 Commandes à implémenter

| Commande | Comportement attendu |
|----------|----------------------|
| `CAP LS [302]`, `CAP REQ`, `CAP END`, `CAP LIST` | Caps annoncées : `echo-message`, `server-time`, `message-tags` (minimal). Enregistrement suspendu tant que `CAP END` n'est pas reçu si `CAP LS` a été envoyé |
| `PASS` | Uniquement si un mot de passe serveur optionnel est défini (§7) |
| `NICK`, `USER` | Enregistrement → `001`–`004`, `005` (ISUPPORT), `422` (pas de MOTD) ou MOTD court |
| `PING` / `PONG` | Les deux sens. Ping serveur toutes les 90 s, coupure à 180 s sans réponse |
| `JOIN`, `PART` | Multi-salons séparés par virgules, `JOIN 0` = quitter tous les salons |
| `PRIVMSG`, `NOTICE` | Salons et pseudos. CTCP ACTION (`/me`) relayé. CTCP DCC **bloqué** |
| `QUIT` | Diffusion aux salons communs |
| `NAMES`, `TOPIC`, `LIST` | Standard |
| `WHO`, `WHOIS` | Réponses minimales, aucune IP, aucun idle time précis (arrondi ou omis) |
| `MODE` | Réponses inertes cohérentes (`324`, `221`) pour ne pas casser les clients ; aucun mode modifiable |
| `MOTD`, `LUSERS`, `VERSION`, `TIME` | Réponses minimales |
| `OPER` | Authentification admin par `FUGACE_ADMIN_PASSWORD` |
| `DELCHAN`, `CLEARHISTORY` | Admin uniquement : supprimer un salon, vider son historique |
| Inconnue | `421 ERR_UNKNOWNCOMMAND` |

### 5.2 ISUPPORT (005) minimal

`NETWORK=fugace CASEMAPPING=ascii CHANTYPES=# CHANLIMIT=#:20 CHANNELLEN=32 NICKLEN=30 TOPICLEN=300 PREFIX=(o)@ MAXTARGETS=4`
plus le jeton vendeur d'upload (§6.2).

### 5.3 Limites protocolaires

- Ligne IRC max 512 octets (tags inclus jusqu'à 4096 si `message-tags` négocié, côté client uniquement ; le serveur n'accepte pas de tags clients en v1 hors `+typing` ignoré).
- Enregistrement obligatoire en moins de 30 s.
- File d'envoi par client bornée (256 lignes). Client trop lent → déconnexion `SendQ exceeded`.

## 6. Fichiers et images

### 6.1 Stockage

- **RAM uniquement**. Aucun fichier temporaire, jamais (voir ARCHITECTURE §5 : pas de `ParseMultipartForm`).
- Taille max par fichier : 10 Mo. Total max en RAM : 256 Mo, éviction du plus ancien.
- TTL : 1 h, puis suppression. L'uploader peut supprimer son fichier avant.
- Identifiant : 128 bits aléatoires (crypto/rand), base64url. URL : `/f/<id>/<nom-assaini>`.

### 6.2 Upload

- `POST /upload?name=<nom>` avec le fichier en corps brut (`Content-Length` obligatoire).
- Authentification : `Authorization: Bearer <jeton>` où le jeton est propre à **une connexion IRC/WS vivante**, communiqué au client dans le 005 via un jeton ISUPPORT vendeur (ex. `fugace/UPLOAD=<jeton>`). Connexion fermée → jeton invalide.
- Réponse : JSON `{"url": "...", "id": "...", "expires": "RFC3339"}`.
- `DELETE /f/<id>` avec le même jeton → suppression.

### 6.3 Traitement

- Images `jpeg`, `png`, `gif`, `webp` : `DecodeConfig` d'abord, refus au-delà de 25 mégapixels (anti-bombe de décompression), puis décodage et **ré-encodage** (supprime EXIF/GPS/commentaires). WebP ré-encodé en PNG. GIF animé conservé via `DecodeAll`/`EncodeAll`.
- SVG et tout le reste : servis en `application/octet-stream` + `Content-Disposition: attachment`.
- Le type est déterminé par le contenu (magic bytes), jamais par l'extension ou le header client.

### 6.4 Côté client

- Le client web envoie l'URL complète dans un `PRIVMSG` après upload.
- Il affiche un aperçu **uniquement** pour les URLs de sa propre origine sous `/f/`. Aucun chargement automatique de ressource externe (anti-tracking).
- Les clients IRC voient un lien cliquable.
- URL absolue : construite à partir de `FUGACE_PUBLIC_URL` si défini, sinon du `Host` de la requête d'upload.

### 6.5 DCC

Les messages CTCP `DCC` sont rejetés avec une NOTICE explicative : DCC révèle l'IP des participants, contraire à l'objectif de confidentialité.

## 7. Configuration (toute optionnelle)

Tout fonctionne sans aucune variable. Surcharges possibles :

| Variable | Défaut | Rôle |
|----------|--------|------|
| `FUGACE_HOST` | vide | Domaine(s)/IP publique(s) pour le certificat Let's Encrypt et les URLs. Vide = autosigné |
| `FUGACE_TLS` | `auto` | `auto` (monté > ACME > autosigné) ou `off` (derrière un proxy) |
| `FUGACE_ACME_DIRECTORY` | Let's Encrypt prod | URL du directory ACME (staging pour les tests) |
| `FUGACE_CERT_CACHE` | vide | Chemin d'un volume pour garder compte + certificat ACME entre recréations. Seule écriture disque autorisée, et seulement si défini. Ne contient aucune donnée utilisateur |
| `FUGACE_HTTP_ADDR` | `:8080` | Écoute HTTP |
| `FUGACE_HTTPS_ADDR` | `:8443` | Écoute HTTPS |
| `FUGACE_IRC_ADDR` | `:6667` | IRC clair (vide = désactivé) |
| `FUGACE_IRCS_ADDR` | `:6697` | IRC TLS (vide = désactivé) |
| `FUGACE_PUBLIC_URL` | vide | Base des URLs de fichiers (par défaut `https://<premier FUGACE_HOST>` ou `Host` de la requête) |
| `FUGACE_PASSWORD` | vide | Mot de passe serveur partagé (IRC `PASS` + invite web). Vide = ouvert |
| `FUGACE_TRUSTED_PROXIES` | vide | CIDR dont on accepte `X-Forwarded-For` (pour le rate-limit seulement) |
| `FUGACE_MAX_UPLOAD_MB` | `10` | Taille max par fichier |
| `FUGACE_FILES_MAX_MB` | `256` | Budget RAM fichiers |
| `FUGACE_FILE_TTL` | `1h` | Durée de vie d'un fichier |
| `FUGACE_ADMIN_PASSWORD` | vide | Mot de passe admin (`OPER`, champ web). Vide = pas d'admin |
| `FUGACE_CHANNELS` | `#general` | Salons créés au démarrage, séparés par des virgules |
| `FUGACE_HISTORY_MAX_MB` | `64` | Budget RAM de l'historique des salons, `0` = désactivé |

Validation stricte au démarrage : valeur invalide → message d'erreur clair et sortie code 2.

## 8. Client web

- HTML/CSS/JS **vanilla**, sans framework, sans étape de build, sans npm, embarqué via `go:embed`. Poids cible < 60 Ko.
- Parle IRC sur WebSocket (spec IRCv3 WebSocket, sous-protocole `text.ircv3.net`).
- Écran d'accueil : un champ pseudo (+ mot de passe serveur si exigé). Le pseudo peut être mémorisé en `localStorage` côté navigateur (opt-in, case à cocher).
- Interface : liste des salons et MP, zone de messages, liste des membres, champ de saisie avec commandes `/nick /join /part /msg /me /topic /quit /help`.
- Upload par bouton, glisser-déposer et coller (Ctrl+V d'image).
- Reconnexion automatique avec backoff.
- Thème sombre/clair (prefers-color-scheme), responsive mobile.
- Rendu texte exclusivement via `textContent` ; liens détectés et créés via DOM avec `rel="noopener noreferrer"`. Codes couleur IRC retirés en v1.
