# ADR 0008 — Fichiers et images (M4)

## D26. `golang.org/x/image` v0.46.0
- **Contexte** : la stdlib ne décode pas WebP (SPEC §6.3).
- **Décision** : `golang.org/x/image`, sous-paquet `webp` uniquement (décodage ; la sortie est en PNG).
  - Équipe Go, licence BSD-3-Clause, suivi Dependabot, version publiée depuis plus de deux semaines.
  - Alternative écartée : refuser WebP, alors que c'est le format courant des captures sur mobile.
- **Conséquences** : troisième et dernière dépendance directe autorisée (websocket, x/crypto, x/image).

## D27. Choix du pipeline
- **Bombes** (T5) :
  - `DecodeConfig` avant tout décodage, 25 MP au plus ;
  - pour un GIF, les blocs sont parcourus *avant* `DecodeAll` (au plus 1000 trames, 100 MP cumulés), car un GIF de 10 Mo peut décrire des centaines de trames de 25 MP ;
  - un seul décodage à la fois (`MaxDecodes = 1`) : une image de 25 MP occupe environ 100 Mo une fois décodée. Les envois suivants attendent leur tour.
- **Orientation** : seule la balise EXIF Orientation est lue, et appliquée avant le ré-encodage, sans copie intermédiaire. Sans cela, les photos de téléphone ressortiraient couchées une fois l'EXIF supprimé.
- **Rejets** : un contenu dont la signature annonce une image mais qui ne se décode pas (polyglotte, fichier corrompu) est refusé (`415`, ou `413` si les dimensions annoncées dépassent la limite). Il n'est pas servi en pièce jointe : on ne sert jamais un faux.
- **Autres fichiers** : servis tels quels en `application/octet-stream`, `Content-Disposition: attachment`, avec `nosniff` et la CSP `sandbox`. Le nom est assaini (ASCII `[A-Za-z0-9._-]`, 80 octets, sans chemin) ; l'extension d'une image ré-encodée suit son type réel.
- **Débit** : au-delà du burst (3, puis 1 toutes les 10 s), l'envoi est refusé (`429` + `Retry-After`), et non ralenti : une requête HTTP ne doit pas rester pendue.
- **Jeton** : émis à l'enregistrement IRC/WS (pas avant), annoncé en `fugace/UPLOAD=<jeton>` dans le `005`, révoqué à la déconnexion. Le registre ne garde qu'une empreinte SHA-256 du jeton ; le store connaît un identifiant d'auteur distinct du jeton.
- **URL renvoyée** : `FUGACE_PUBLIC_URL` sinon `https://<premier FUGACE_HOST>` (TLS actif), sinon schéma et `Host` de la requête.
- **Client web** : aperçu uniquement pour une URL de **sa propre origine** sous `/f/<id>/<nom>.(jpg|png|gif)`. Toute autre image reste un simple lien, jamais chargé.
