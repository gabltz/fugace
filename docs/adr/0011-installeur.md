# ADR 0011 — Installeur autonome (`install.sh`)

## D36. Installation en une ligne
- **Contexte** : installer fugace sans cloner le dépôt ni connaître les options Docker de durcissement, avec une configuration guidée.
- **Décision** :
  - `install.sh` (bash, à la racine) est publié comme asset de chaque release avec son empreinte SHA-256. Il est servi par `https://github.com/gabltz/fugace/releases/latest/download/install.sh`, et la forme recommandée est `curl -fsSL … | bash`.
  - **Menu** : installer (ou reconfigurer), réinstaller de zéro, désinstaller. Toutes les variables `FUGACE_*` sont proposées avec leur défaut. Entrée garde la valeur, `-` vide le champ. Un hôte vide signifie un usage local, en HTTPS autosigné.
  - **Entrée** : les questions sont lues sur `/dev/tty`, puisque le script lui-même arrive par l'entrée standard avec `curl | bash`. Sans terminal, seul le mode `--yes` est possible ; il prend les réponses dans l'environnement.
  - **Lancement** : le conteneur reçoit le durcissement de `compose.yaml` (lecture seule, `cap_drop ALL`, `no-new-privileges`, utilisateur 65532, 512 Mo sans swap, pids, `core=0`, `logging: none`). Seules les valeurs différentes des défauts sont passées, pour qu'une version future garde ses propres défauts.
  - **Validation** : les saisies sont validées avant le lancement. Si le serveur refuse quand même la configuration (code 2), le script rejoue le contrôle de démarrage dans un conteneur jetable, sans ports, pour afficher le message, puisque le conteneur principal n'a aucun log.
  - **Persistance** : aucun fichier de configuration n'est écrit sur l'hôte. Une reconfiguration relit les réglages du conteneur en place (`docker inspect`).
- **Risques de `curl | bash` et parades** :
  - le script est versionné avec la release et accompagné de son SHA-256, et le README montre comment le lire et le vérifier avant de l'exécuter ;
  - la signature cosign de l'image est vérifiée si `cosign` est présent ;
  - la seule image tierce (`busybox`, pour préparer le volume de cache sur les images antérieures à 0.3) est épinglée par digest ;
  - `shellcheck` et un test non interactif (installation, reconfiguration, refus, réinstallation, désinstallation) tournent en CI.
- **Conséquences** :
  - l'image reçoit aussi le tag `latest` ;
  - elle contient un point de montage `/cache` vide (65532, mode 0700), qui donne au volume de cache le bon propriétaire et garde `docker diff` vide.
