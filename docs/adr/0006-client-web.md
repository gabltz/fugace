# ADR 0006 — Client web (M2)

## D23. Choix du client web
- **Rendu** :
  - DOM construit nœud par nœud, texte via `textContent` ;
  - liens `http(s)` seulement, `rel="noopener noreferrer"`, jamais chargés automatiquement ;
  - codes de formatage IRC retirés ;
  - aucune ressource tierce.
- **Historique** : à chaque JOIN, le tampon du salon est vidé, puis le serveur rejoue l'historique (ADR 0003), avec l'heure d'origine (`server-time`). Une reconnexion ou un rechargement restituent ainsi la conversation sans doublon. Le client garde au plus 2000 lignes par tampon, en mémoire comme dans le DOM.
- **CTCP** : seul `ACTION` est affiché ; le client ne répond à aucune requête CTCP (VERSION, TIME, PING…), qui révéleraient navigateur et fuseau horaire.
- **Connexion** :
  - CAP `server-time` et `echo-message` : les messages envoyés s'affichent quand le serveur les renvoie ;
  - reconnexion avec backoff (1, 2, 5, 10, 30 s ± 20 %), avec `_` ajouté au pseudo s'il est pris pendant la reconnexion ;
  - sur l'écran d'accueil, pseudo pris ou mot de passe incorrect → message d'erreur, sans nouvelle tentative.
- **Admin** : champ « mot de passe admin » optionnel (`OPER`), commandes `/oper`, `/delchan`, `/clearhistory`.
- **Pseudo mémorisé** : `localStorage`, seulement si la case est cochée, et effacé si elle est décochée.
- **Cache** : `index.html` est revalidé à chaque chargement ; les ressources sont servies sous `?v=<empreinte>`, en `immutable`.
- **Vérification** : `scripts/browser/run.sh` lance Chromium headless (Playwright) et axe-core.
  - Pas de Lighthouse : axe couvre l'accessibilité, et le reste (CSP, requêtes tierces, erreurs console) est vérifié directement par le script.
  - Sur `http://` (avant M3), l'avertissement « COOP ignoré sur une origine non sûre » est toléré, et seulement celui-là.
