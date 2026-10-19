# ADR 0003 — Historique de salon et administration des salons

Remplace **D4** (pas d'historique) et **D8** (pas de modération) de l'ADR 0001, à la demande du porteur du projet.

## D15. Historique de salon en RAM
- **Contexte** : un arrivant doit voir ce qui a été dit dans un salon. L'invariant « zéro écriture disque » reste non négociable.
- **Décision** :
  - Tout message `PRIVMSG`/`NOTICE` envoyé à un salon est conservé **en RAM uniquement**, depuis le démarrage du conteneur, et rejoué à chaque `JOIN`, après `366`.
  - Pas d'historique des messages privés.
  - Seule borne : un budget global `FUGACE_HISTORY_MAX_MB` (défaut 64, `0` = historique désactivé). Au-delà, le message le plus ancien, tous salons confondus, est évincé. Sans budget, un flood suffirait à épuiser la RAM (T6/T7).
  - Rejeu : avec la cap `server-time`, chaque ligne porte son `@time` d'origine. Sans elle, le texte est préfixé par `[AAAA-MM-JJ HH:MM] ` (UTC).
  - L'entrée stocke uniquement l'horodatage, le masque `nick!user@fugace`, la commande, le salon et le texte. Aucune IP.
- **Conséquences** :
  - Redémarrer ou recréer le conteneur efface tout.
  - Tant qu'il tourne, la RAM contient l'historique : c'est une limite assumée (A5 avec accès root à l'hôte), ajoutée au modèle de menace.
  - L'admin peut vider l'historique d'un salon (D16).

## D16. Salons gérés par un admin
- **Contexte** : seule « gestion » demandée : créer/supprimer des salons et vider leur historique. Pas de kick, de ban, ni de topic verrouillé.
- **Décision** :
  - `FUGACE_ADMIN_PASSWORD` (défaut vide = aucun admin possible). IRC : `OPER <nom> <mot de passe>`, le nom est libre. Web : champ « mot de passe admin » optionnel. Comparaison en temps constant, échec → `464`, et cinq échecs par connexion → déconnexion.
  - `FUGACE_CHANNELS` (défaut `#general`) : salons créés au démarrage.
  - **Seul un admin crée un salon**, en le rejoignant. Un `JOIN` d'un utilisateur sur un salon inexistant → `403`.
  - Les salons **persistent même vides**, avec leur historique, jusqu'à suppression ou redémarrage.
  - `DELCHAN <#salon> [:raison]` : les membres reçoivent un `KICK` du serveur, puis le salon et son historique sont détruits.
  - `CLEARHISTORY <#salon>` : vide l'historique.
  - Un utilisateur non admin qui utilise ces commandes reçoit `481 ERR_NOPRIVILEGES`.
  - L'admin porte le mode utilisateur `+o` et apparaît `@` dans tous les salons (`PREFIX=(o)@`). Les modes restent non modifiables.
  - Le topic reste modifiable par tout membre.
- **Conséquences** : la limite « 200 salons au total » ne concerne plus que l'admin ; `FUGACE_PASSWORD` (mot de passe d'entrée) reste indépendant.
