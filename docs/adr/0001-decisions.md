# ADR 0001 — Décisions fondatrices

Format : contexte → décision → conséquences. Toute nouvelle décision structurante = nouvel ADR numéroté.

## D1. Go plutôt que Rust
- **Contexte** : serveur réseau I/O-bound, petite équipe, besoin d'un binaire statique et d'une image minimale.
- **Décision** : Go 1.27, `CGO_ENABLED=0`.
- **Conséquences** : stdlib couvre HTTP/TLS/images/fuzz ; GC acceptable à cette échelle ; Rust apporterait un gain mémoire marginal pour un coût de dev et de dépendances (tokio, axum, image…) plus élevé.

## D2. Le web parle IRC sur WebSocket
- **Contexte** : deux protocoles = deux parseurs, deux surfaces d'attaque, deux comportements à garder cohérents.
- **Décision** : spec IRCv3 WebSocket (`text.ircv3.net`), une ligne IRC par message WS.
- **Conséquences** : un seul cœur ; le client web gère lui-même le protocole IRC (simple en JS).

## D3. Tout en RAM, rien sur disque
- **Contexte** : exigence d'éphémérité et de no-log.
- **Décision** : état et fichiers en mémoire, rootfs RO, pas de swap.
- **Conséquences** : redémarrage = remise à zéro ; budget RAM fichiers borné ; pas de scrollback.

## D4. Pas d'historique — remplacée par ADR 0003 (D15)
- **Contexte** : tout historique, même en RAM, est une forme de journal.
- **Décision** : aucun scrollback en v1.
- **Conséquences** : un rechargement de page web perd la conversation. Réévaluable (ring buffer RAM opt-in) dans un ADR ultérieur.

## D5. Jeton d'upload lié à une connexion vivante
- **Contexte** : sans comptes, un endpoint d'upload ouvert devient un hébergeur anonyme.
- **Décision** : jeton aléatoire par connexion, annoncé dans un jeton ISUPPORT vendeur, invalidé à la déconnexion.
- **Conséquences** : les clients IRC natifs peuvent aussi uploader (curl + jeton) ; impossible sans être connecté.

## D6. Ré-encodage des images
- **Contexte** : EXIF/GPS, polyglottes.
- **Décision** : décodage + ré-encodage systématique ; tout le reste en pièce jointe.
- **Conséquences** : perte des métadonnées (voulu), WebP converti en PNG, coût CPU borné par le plafond de pixels.

## D7. Pas de framework ni de build front
- **Contexte** : npm = chaîne d'approvisionnement lourde pour une UI simple.
- **Décision** : JS vanilla embarqué.
- **Conséquences** : zéro dépendance front, CSP stricte facile.

## D8. Pas de modération en v1 — remplacée par ADR 0003 (D16)
- **Contexte** : usage privé / petit groupe.
- **Décision** : pas d'ops ; protection par mot de passe serveur optionnel.
- **Conséquences** : exposition publique sans mot de passe déconseillée (README).

## D9. HTTPS intégré, y compris sur IP
- **Contexte** : l'image doit tout faire seule, servir le web directement sur l'hôte, même accédée par une IP nue, sans reverse proxy. Let's Encrypt délivre des certificats pour adresses IP (IPv4/IPv6) depuis janvier 2026, uniquement via le profil `shortlived` (160 h).
- **Décision** : gestionnaire TLS interne avec priorité monté > ACME (`FUGACE_HOST`) > autosigné en RAM. HTTP = redirection + ACME. Un seul certificat partagé entre HTTPS et IRC TLS.
- **Client ACME** : la bibliothèque retenue doit gérer les identifiants IP, la sélection de profil ACME, `http-01` et `tls-alpn-01`, et idéalement ARI. Vérifier en M3 avant de coder : `golang.org/x/crypto/acme` gère les identifiants IP (`IPIDs`) mais la sélection de profil est à confirmer ; à défaut, `github.com/go-acme/lego/v4` (cœur seulement, aucun provider DNS importé). Consigner le choix et la vérification dans un ADR dédié.
- **Conséquences** : une 3e dépendance ; un trafic sortant vers l'API ACME (uniquement si `FUGACE_HOST`) ; racines CA embarquées ; quota de 5 certificats/semaine par ensemble d'identifiants → repli autosigné, backoff et cache optionnel (`FUGACE_CERT_CACHE`), seule écriture disque admise et uniquement sur opt-in.
