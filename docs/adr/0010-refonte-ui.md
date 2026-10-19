# ADR 0010 — Refonte de l'interface web (v0.2.0)

## D33. Direction visuelle
- **Contexte** : v0.2.0 est une refonte de l'interface web seule, voulue « très design », fluide et très animée (verre dépoli, animations partout). Le protocole et le serveur sont inchangés.
- **Décision** :
  - **Deux thèmes** choisis par le système (`prefers-color-scheme`), avec un réglage « Auto / Clair / Sombre » :
    - sombre : « Aurora », taches de couleur animées violet/bleu/cyan/rose sur fond profond ;
    - clair : verre givré pastel.
  - **Surfaces** : panneaux en verre (`backdrop-filter`), reflets, grain, halo qui suit le pointeur, bordures coniques animées (`@property`).
  - **Mouvement** :
    - arrivée des messages en ressort (`linear()`) ;
    - transitions entre conversations (View Transitions) ;
    - indicateur coulissant, boutons magnétiques et ondulations ;
    - visionneuse d'images (`<dialog>`) et gerbe de particules à l'envoi d'un fichier ;
    - réglage « Animations : Auto / Pleines / Réduites ».
  - Les messages consécutifs d'une même personne sont regroupés, avec avatar à initiale et séparateurs de jour.
- **Garde-fous** :
  - **Performances** : seules `transform` et `opacity` sont animées, et le flou est réservé aux grands panneaux, jamais à chaque message. L'historique rejoué s'affiche d'un coup ; seuls les messages en direct sont animés.
  - **Accessibilité** :
    - « Auto » suit `prefers-reduced-motion`, et le mode réduit coupe toute animation décorative ;
    - `prefers-reduced-transparency` remplace le verre par un fond opaque ;
    - axe-core reste à 0 violation (accueil, conversation, mobile, thème clair).
  - **Sécurité** :
    - aucun style inline : CSS dans `style.css`, variables posées par le CSSOM, Web Animations API ;
    - `prefs.js` (bloquant, même origine) applique le thème avant le premier rendu, sans flash ;
    - la CSP reste sans `unsafe-inline`.
  - **Données** : thème et animations sont gardés en `localStorage` (préférences d'affichage, rien d'identifiant), comme le pseudo opt-in.

## D34. Police embarquée et budget
- **Décision** :
  - Inter 4.1 (SIL OFL 1.1), réduite au latin et à l'axe `wght`, en woff2 (36 Ko), servie depuis notre origine ;
  - la licence est conservée dans les métadonnées du fichier et reproduite dans `THIRD_PARTY_NOTICES.md` ;
  - préchargée, et les URLs des CSS sont versionnées comme celles de l'index, donc cache `immutable` et aucun double téléchargement.
- **CSP** : ajout de `font-src 'self'`. C'est le seul assouplissement, limité à notre propre origine.
- **Budget** : le client passe de « < 60 Ko » à « ≤ 128 Ko police comprise » (environ 115 Ko à la livraison). `TestClientBudgetAndPolicy` l'impose.

## D35. Changement de conversation et View Transitions
- **Contexte** : pendant la refonte, un défaut a été trouvé par la suite navigateur. Avec une View Transition, le changement de conversation n'était appliqué qu'au rappel de la transition, quelques images plus tard : un fichier déposé juste après un clic partait dans l'**ancienne** conversation.
- **Décision** : l'état (`conversation active`) change de façon synchrone ; seul le rendu passe dans la transition. Un test navigateur dédié vérifie qu'un message envoyé juste après un changement va à la nouvelle conversation.
