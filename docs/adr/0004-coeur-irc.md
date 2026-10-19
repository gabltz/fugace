# ADR 0004 — Choix du cœur IRC (M1)

Points que la SPEC ne tranchait pas, résolus vers l'option la plus sûre et la plus simple.

## D17. Identité exposée
- **Contexte** : beaucoup de clients envoient le compte système en `USER` (irssi : `$USER`) et le nom complet en realname.
- **Décision** : l'ident et le realname fournis sont **ignorés**. Le masque est fixe : `nick!~user@fugace` (TCP) ou `nick!web@fugace` (web). Le realname affiché (`311`, `352`) est `*`. Pas de `317` (idle), `338` ou `378` dans WHOIS. `319` ne liste que les salons **partagés** avec le demandeur.
- **Conséquences** : rien d'identifiant ne transite hors du pseudo choisi (T2).

## D18. Anti-flood par « fake lag »
- **Contexte** : ARCHITECTURE §6 fixe les débits sans dire quoi faire au-delà.
- **Décision** : au-delà du burst, la lecture du client est **retardée** (token bucket), jamais rejetée. Le client ne déborde pas, il est simplement ralenti par la contre-pression TCP.
  - PRIVMSG/NOTICE comptent par cible, JOIN par salon, NICK seulement après l'enregistrement.
  - Une limite générale (burst 20, 5 lignes/s) couvre aussi les requêtes (WHO, LIST…).
  - `OPER` raté : délai de 2 s, puis déconnexion au 5ᵉ échec.
- **Conséquences** : un collage de 15 lignes passe, plus lentement ; un flood ne coûte que la connexion fautive.

## D19. Files d'envoi
- **Contexte** : une réponse volumineuse (WHO de 500 membres, rejeu d'historique) dépasserait la SendQ de 256 lignes.
- **Décision** : deux files par client.
  - Les réponses à **ses propres commandes** vont dans une file bloquante (64 lignes), ce qui fait contre-pression sur ce seul client.
  - Les messages **des autres** vont dans la SendQ non bloquante de 256 lignes ; si elle est pleine → `SendQ exceeded`.
- **Conséquences** : un client lent ne bloque jamais les autres, et une grosse réponse ne le déconnecte pas.

## D20. Format et limites protocolaires
- Lignes : 512 octets hors tags ; tags clients acceptés jusqu'à 4096 octets puis ignorés (y compris `+typing`), `TAGMSG` ignoré. Ligne trop longue → `417` et la connexion continue.
- UTF-8 invalide remplacé par U+FFFD à la lecture (le web exige de l'UTF-8 valide) ; en sortie, le dernier paramètre est tronqué sur une frontière de caractère pour tenir dans 512 octets.
- Le dernier paramètre est toujours émis en forme `:trailing` dès qu'il y a au moins deux paramètres (comportement des ircd courants, attendu par certains clients).
- Noms de salons : ASCII imprimable sans `,`, comme les pseudos (pas d'homoglyphes, T10).
- `PASS` sans mot de passe serveur configuré : accepté et ignoré.
- Modes : `324` → `+n` (seuls les membres écrivent dans un salon), `221` → `+` ou `+o` ; toute tentative de changement de mode de salon → `482`.
- Historique : seuls le texte et `ACTION` sont conservés ; les autres CTCP (VERSION…) ne le sont pas, pour que le rejeu ne déclenche pas de réponses automatiques. Sans `server-time`, le texte rejoué est préfixé `[AAAA-MM-JJ HH:MM] `.

## D21. Limite par source
- **Décision** : clé HMAC-SHA256 aléatoire au démarrage, identifiant tronqué à 128 bits. IPv6 regroupée par /64 (un hôte en possède généralement un entier). L'entrée disparaît avec la dernière connexion de la source.
