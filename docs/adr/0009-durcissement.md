# ADR 0009 — Durcissement (M5)

## D28. Plafond de connexions HTTP par source
- **Contexte** : seules les connexions IRC/WS passaient par le compteur par source. Une attaque slowloris sur 80/443 pouvait occuper les 4096 descripteurs (`nofile`) malgré `ReadHeaderTimeout` (5 s).
- **Décision** :
  - Un `net.Listener` limitant entoure les écoutes HTTP et HTTPS, *avant* TLS, pour qu'un handshake bloqué compte aussi.
  - 32 connexions simultanées par source (même clé HMAC que pour l'IRC, IPv6 par /64) et 1024 au total. Au-delà, la connexion est fermée aussitôt.
  - Les proxys de `FUGACE_TRUSTED_PROXIES` ne comptent que dans le plafond global, puisque tout arrive par eux.
- **Conséquences** :
  - Un navigateur ouvre au plus 6 connexions par page, plus le WebSocket, lui-même compté par le serveur de chat : 32 laisse de la marge, y compris derrière un NAT familial.
  - Mesures : `docs/BENCH.md`.

## D29. `X-Forwarded-For`
- **Décision** : l'en-tête n'est lu que si le pair TCP appartient à `FUGACE_TRUSTED_PROXIES`.
  - On parcourt la chaîne depuis la droite ; la première adresse non fiable est la source, au plus 16 entrées.
  - Une entrée invalide fait revenir au pair TCP : forger l'en-tête ne permet jamais d'usurper une autre source.
  - Seule la limite par source l'utilise ; l'adresse est hachée, jamais conservée.

## D30. Mot de passe serveur
- **Décision** : un `PASS` erroné attend 2 s avant le `464`, puis la connexion est fermée. Combiné à la limite de 10 connexions par source, cela borne la devinette à environ 5 essais/s par source. Comparaison en temps constant, comme pour `OPER`.

## D31. Vérifications automatiques de la CI
- `scripts/check-supply-chain.sh` vérifie :
  - les actions épinglées par SHA complet avec leur version en commentaire ;
  - les images épinglées par digest ;
  - au plus 3 dépendances directes.
  `go mod tidy -diff` s'y ajoute : en M4, un `go.mod` non rangé marquait `x/image` comme indirecte, ce qui aurait faussé ce décompte.
- `scripts/check-compose.sh` échoue si une protection du conteneur disparaît : `read_only`, `cap_drop`, `no-new-privileges`, utilisateur non-root, `logging: none`, `core: 0`, `pids_limit`, et `memswap_limit = mem_limit`. Il échoue aussi en présence de `privileged`, `cap_add`, du réseau hôte ou d'un montage sensible.
- `scripts/smoke.sh` tourne avec le pilote de logs par défaut, pour **inspecter** la sortie : une seule ligne, celle du démarrage, sans IP ni pseudo, et rien sur stderr.

## D32. `irctest` non intégré
- **Contexte** : jalon optionnel de M5.
- **Décision** : non intégré en v0.1.0. La suite suppose des fonctions absentes par choix (opérateurs de salon, modes modifiables, KICK/INVITE, historique de MP, SASL…), et l'essentiel de ses échecs documenterait des écarts voulus plutôt que des défauts.
  - La conformité est vérifiée autrement : WeeChat et irssi réels (`scripts/clients/run.sh`), en CI à chaque PR.
  - À réévaluer après v0.1, sur un sous-ensemble ciblé (enregistrement, CAP, PING, PRIVMSG).
