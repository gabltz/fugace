# fugace — Mesures de charge

Commande : `scripts/load/run.sh fugace:local` (ROADMAP M5). Le serveur tourne avec les limites de `compose.yaml` : 512 Mio sans swap, 128 pids, `nofile` 4096, rootfs en lecture seule, `cap_drop: ALL`.

Les clients de charge sont dans un conteneur séparé et répartis sur 60 adresses source, des alias ajoutés dans **ce** conteneur uniquement. Les limites par source de fugace restent inchangées (10 connexions IRC/WS, 32 HTTP).

Machine : 4 vCPU QEMU, Docker 29.9.1, image `fugace` M5 (Go 1.27.2).

## Chat : 500 clients, 50 messages/s, 60 s

| Mesure | Valeur |
|--------|--------|
| Connexion + JOIN de 500 clients | 0,9 s |
| Messages envoyés | 3 000 (50/s), chacun relayé aux 499 autres membres de `#general` |
| Livraisons attendues / reçues | 1 497 000 / 1 497 000 (0 perte) |
| Latence émission → réception | p50 1,9 ms · p95 2,5 ms · p99 2,9 ms · max 5,6 ms |
| CPU serveur | 22,6 % en moyenne, 30 % au pic (pourcentage d'un cœur, sur 4 vCPU) |
| RAM serveur | 4,9 Mio au repos → 34 Mio sous charge (dont l'historique RAM des 3 000 messages) |

## Flood : 2 000 lignes d'un coup par un seul client

| Mesure | Valeur |
|--------|--------|
| Débit relayé après le burst de 10 | 2,00 lignes/s (limite ARCHITECTURE §6 : 2/s) |
| Latence d'un autre client pendant le flood | p50 0,25 ms · max 19 ms |

Le flooder n'est pas déconnecté : sa lecture est ralentie (fake lag, ADR 0004 D18), et rien ne s'accumule côté serveur au-delà des tampons TCP.

## Slowloris

| Scénario | Résultat |
|----------|----------|
| 200 connexions HTTPS inactives (début d'enregistrement TLS puis silence) depuis une source | 32 gardées ouvertes (plafond par source), les autres fermées aussitôt ; toutes fermées par le serveur après 4,3 s (délai d'en-tête/handshake 5 s) |
| Requête HTTPS d'une autre source pendant l'attaque | servie en 2 ms |
| 30 connexions IRC jamais enregistrées depuis une source | 10 gardées (limite par source), 20 refusées avec `ERROR :Closing Link: Too many connections` ; fermées après 30 s (délai d'enregistrement) |

`docker diff` du serveur après l'ensemble des scénarios : vide.
