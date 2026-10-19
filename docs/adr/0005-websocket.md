# ADR 0005 — Bibliothèque WebSocket

## D22. `github.com/coder/websocket` v1.8.15
- **Contexte** : le client web parle IRC sur WebSocket (D2) ; la stdlib n'a pas de serveur WebSocket.
- **Décision** : `github.com/coder/websocket` (déjà prévue par ARCHITECTURE §1), version v1.8.15.
  - **Pourquoi** : API minimale (`Accept`/`Read`/`Write`), aucune dépendance transitive, vérification d'`Origin` par défaut (CSWSH, T14), limite de lecture (`SetReadLimit`), compression désactivée par défaut.
  - **Alternatives** :
    - `gorilla/websocket` : API plus large, pas de vérification d'`Origin` par défaut.
    - `golang.org/x/net/websocket` : déconseillée par ses auteurs.
    - Implémentation maison de la RFC 6455 : surface d'attaque et maintenance injustifiées.
  - **Maintenance** : maintenue par Coder, dernière version publiée en juin 2026, suivie par Dependabot.
  - **Licence** : ISC (permissive).
- **Règles d'emploi** :
  - sous-protocole `text.ircv3.net` exigé, sinon fermeture `1008` ;
  - messages texte uniquement, 4 Kio maximum (`1009` au-delà) ;
  - aucune origine supplémentaire autorisée : seule la même origine passe ;
  - les délais du serveur HTTP sont levés avant le hijack (sinon la connexion tomberait après `ReadTimeout`), l'inactivité restant gérée par le PING IRC et l'enregistrement par son délai de 30 s.
- **Conséquences** : une dépendance directe sur les trois autorisées.
