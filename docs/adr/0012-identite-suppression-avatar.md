# ADR 0012 — Identité d'auteur, suppression de messages, photo de profil, « Clear account »

## D37. Identité d'auteur sans compte
- **Contexte** : pour supprimer ses messages (y compris après un rechargement de page) et pour « Clear account », le serveur doit reconnaître l'auteur d'un message, sans créer de compte ni conserver d'identifiant durable.
- **Décision** :
  - Le client web génère une **clé secrète aléatoire** (32 octets, base64url) gardée dans le navigateur (`localStorage`), et l'envoie avant l'enregistrement : `KEY <clé>`.
  - Le serveur ne garde que `HMAC-SHA256(clé de démarrage, clé)` tronqué. Cette « identité » change à chaque redémarrage, puisque la clé de démarrage n'existe qu'en RAM.
  - Sans `KEY` (clients IRC natifs), l'identité est aléatoire et propre à la connexion.
  - `KEY` après l'enregistrement est refusé (`462`) : une connexion ne change jamais d'identité.
- **Conséquences** : depuis le même navigateur, vos messages restent les vôtres d'une connexion à l'autre. L'identité n'est jamais montrée aux autres et ne contient ni IP ni pseudo.

## D38. Suppression de messages : IRCv3 `draft/message-redaction`
- **Décision** :
  - Chaque message de salon et chaque message privé reçoit un `msgid`, transmis aux clients qui ont négocié `message-tags`.
  - Le tag serveur `fugace/own` n'est ajouté que pour les connexions de l'auteur, en direct comme au rejeu de l'historique ; il signale au client qu'il peut supprimer ce message.
  - `REDACT <cible> <msgid> [:raison]` est accepté **uniquement de l'auteur** (même identité). Sinon : `FAIL REDACT REDACT_FORBIDDEN`, ou `UNKNOWN_MSGID` pour un message inconnu ou expiré. L'admin n'a pas ce pouvoir : c'est un choix du porteur du projet.
  - **Salon** : le message est retiré de l'historique, et `REDACT` est relayé aux membres qui ont la cap `draft/message-redaction`.
  - **Message privé** : un index borné (4096 entrées) garde l'id, l'identité de l'auteur et le destinataire, **jamais le texte**. `REDACT` va au destinataire et aux connexions de l'auteur.
  - Les clients IRC sans la cap ne reçoivent rien : ce qu'ils ont affiché reste affiché chez eux, mais disparaît de l'historique pour tout le monde.
- **Débit** : `REDACT` compte comme un message (anti-flood).

## D39. Photo de profil
- **Décision** :
  - **Envoi** : `POST /avatar` (jeton d'envoi de la connexion). L'image passe par le même pipeline que les fichiers (signature, plafond de pixels, ré-encodage), puis elle est recadrée au centre et réduite à 128 × 128 en JPEG. Une seule photo par identité, en RAM, au plus 2048 au total.
  - **Durée de vie** : la photo est supprimée quand la dernière connexion de l'identité se termine. Le client web la garde dans le navigateur (après réduction à 256 px) et la renvoie à chaque connexion.
  - **Diffusion** : la cap vendeur `fugace/avatar` permet de recevoir `:nick!user@host AVATAR <version>` (0 = plus de photo) à l'arrivée dans un salon et à chaque changement.
  - **Accès** : `GET /avatar/{nick}?t=<jeton>&v=<version>` est réservé aux **personnes connectées**. Le jeton est exigé, si bien que les photos ne sont pas énumérables depuis l'extérieur. Une version connue rend la photo cachable (`immutable`).
  - `DELETE /avatar` retire la photo.

## D40. « Clear account »
- **Décision** : `CLEAR-ACCOUNT` (alias `CLEARACCOUNT`, bouton « Effacer mon compte » côté web) :
  1. retire de l'historique tous les messages de l'identité (avec `REDACT` relayé) et oublie ses messages privés (avec `REDACT` relayé) ;
  2. supprime ses fichiers envoyés (le propriétaire d'un fichier est désormais l'identité, et non plus la connexion) et sa photo ;
  3. ferme la connexion (`Account cleared`).
  Le client web efface ensuite pseudo, clé et photo mémorisés, et revient à l'accueil.

## D41. Divers
- Les noms de commande acceptent un tiret intérieur (`CLEAR-ACCOUNT`) ; le parseur reste fuzzé.
- **Bouton d'accueil qui bougeait** : l'effet magnétique suivait le pointeur sur toute la largeur de la carte, d'où un bouton qui « fuyait ». Il est retiré du bouton d'accueil et borné ailleurs (rayon de 70 px, 6 px de déplacement au plus). Un test navigateur vérifie que le bouton reste immobile.
- **Budget du client web** : 160 Ko police comprise (au lieu de 128), pour ces fonctions.
