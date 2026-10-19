# ADR 0007 — HTTPS intégré : périmètre et client ACME

Précise **D9** (ADR 0001) et SPEC §3.1, à la demande du porteur du projet.

## D24. Pas de certificat Let's Encrypt pour une adresse IP
- **Contexte** : fugace ne sera pas exposé sur une IP publique. Accédé par IP, il l'est uniquement depuis le réseau local, où aucune autorité publique ne peut valider l'adresse.
- **Décision** :
  - Sans `FUGACE_HOST`, ou accès par IP : certificat **autosigné** en RAM ; l'avertissement du navigateur est accepté.
  - ACME n'est utilisé que pour les **noms de domaine** listés dans `FUGACE_HOST`. Les IP qui y figurent sont seulement ajoutées aux SAN du certificat autosigné, et jamais demandées à une autorité. Le profil `shortlived` n'est donc plus nécessaire.
- **Conséquences** : SPEC §3.1 et README mis à jour ; le critère « certificat Let's Encrypt sur une IP publique » de M3 est remplacé par « certificat ACME pour un domaine », vérifié contre pebble.

## D25. Client ACME : `golang.org/x/crypto/acme` v0.58.0
- **Vérification (M3)** : la version v0.58.0 fournit :
  - `AuthorizeOrder` avec `DomainIDs`/`IPIDs` ;
  - `HTTP01ChallengeResponse` et `TLSALPN01ChallengeCert` ;
  - `WithOrderIssuanceProfile`, la sélection de profil (non utilisée, D24) ;
  - `CreateCertFromOrder`, la finalisation conforme à la RFC 8555 (voir ci-dessous) ;
  - `RateLimit(err)`, qui expose `Retry-After`.
  ARI n'est pas fournie : le renouvellement se fait à mi-vie, comme le prévoit SPEC §3.1.
- **Pourquoi pas `lego`** : `x/crypto` couvre le besoin. C'est un module de l'équipe Go, déjà lié à la stdlib et sans dépendance transitive hors `x/sys`/`x/net`, alors que `lego` apporte un arbre de dépendances bien plus large. `autocert` n'est pas retenu non plus : il émet à la volée au premier handshake TLS et ne permet ni le repli autosigné, ni un backoff ≥ 1 h maîtrisé.
- **Licence** : BSD-3-Clause. **Maintenance** : équipe Go, suivi Dependabot.
- **Version récente assumée** : la v0.58.0 n'avait que 10 jours lors du choix ; d'ordinaire, on retient une version publiée depuis au moins deux semaines.
  - En v0.57.0, `CreateOrderCert` lit l'URL de la commande dans l'en-tête `Location` de la réponse de finalisation. La RFC 8555 ne l'exige pas, et pebble ne l'envoie pas.
  - Une émission asynchrone (`processing`) échouait donc sur `Post ""`. Le défaut a été constaté contre pebble.
  - La v0.58.0 le corrige (« standards-compliant order finalization »).
- **Mise en œuvre** (`internal/tlsmgr`) :
  - Compte éphémère sans e-mail, clé ECDSA P-256 en RAM.
  - Émission au démarrage, uniquement pour les domaines de `FUGACE_HOST`. `GetCertificate` renvoie le certificat courant quel que soit le SNI (vide ou inconnu compris) et ne déclenche jamais d'émission (T15).
  - Challenges `http-01` (port HTTP, jetons en cours uniquement, T18) et `tls-alpn-01` (ALPN `acme-tls/1`, port HTTPS seulement).
  - En cas d'échec : repli autosigné immédiat, puis nouvel essai après `max(1 h × 2ⁿ plafonné à 24 h, Retry-After)` (T17).
  - L'état est consultable via `fugace status`, sans message d'erreur brut : on n'expose qu'une catégorie (`rate limited`, `authorization failed`, `network error`, `order failed`).
- **Tests** :
  - Un faux serveur ACME (`httptest`) vérifie `rateLimited`/`Retry-After`, les échecs et l'absence d'appel déclenché par un SNI.
  - Pebble (serveur ACME de test de Let's Encrypt), en conteneur, vérifie l'émission réelle pour un domaine, servie sur 443 et 6697.
  - Pour joindre pebble, sa racine est montée à la place du bundle CA de l'image : aucune variable fugace supplémentaire, et aucun point de montage créé, ce qui garde `docker diff` significatif.
