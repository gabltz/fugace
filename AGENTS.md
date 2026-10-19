# Conventions de développement — fugace

Ce fichier fait foi pour toute contribution, humaine ou automatisée.

## Documents de référence (à lire avant toute modification)

1. `docs/SPEC.md` — quoi
2. `docs/ARCHITECTURE.md` — comment
3. `docs/THREAT_MODEL.md` — contre quoi
4. `docs/adr/` — pourquoi
5. `docs/ROADMAP.md` — dans quel ordre

En cas de contradiction : SPEC > ARCHITECTURE > le reste. Une ambiguïté se tranche par un nouvel ADR court, pas par une supposition silencieuse.

## Invariants (une PR qui en viole un est refusée)

- **Aucune écriture disque** à l'exécution.
- **Aucun log** hors `cmd/fugace` (ligne de démarrage + erreurs fatales de démarrage, sans donnée utilisateur). Pas de `fmt.Print*`, `log.*`, `slog.*`, `println` ailleurs.
- **Aucune IP** exposée à un autre client ni conservée en clair.
- **Aucun `innerHTML` / `outerHTML` / `insertAdjacentHTML` / `document.write` / `eval`** dans `web/static`.
- **Dépendances** : stdlib d'abord. Toute nouvelle dépendance exige un ADR (pourquoi, alternatives, maintenance, licence). Plafond : 3 directes (websocket, x/image, client ACME).
- **Zéro configuration requise** : toute nouvelle variable d'environnement doit avoir un défaut fonctionnel.

## Style

- `gofumpt` + `goimports`, `golangci-lint` sans exception non justifiée (`//nolint:linter // raison`).
- Code, identifiants, commentaires et messages de commit en **anglais**. Documentation dans `docs/` en français.
- Erreurs enveloppées avec `%w`, jamais ignorées sans commentaire.
- Pas de variables globales mutables ; injection via structs.
- Constantes nommées pour toutes les limites (pas de nombres magiques).

## Tests

- Tout code a des tests. Table-driven par défaut.
- `go test -race -count=1 ./...` doit passer.
- Toute fonction qui parse une entrée externe a une cible de fuzz (`FuzzXxx`) et un corpus de départ dans `testdata/fuzz/`.
- Chaque ligne de `docs/THREAT_MODEL.md` est couverte par au moins un test nommé dans la colonne Test.

## Git

- Branches : `m0-bootstrap`, `m1-irc-core`, etc. Une PR par jalon (ou sous-jalon si > 1500 lignes).
- Conventional Commits : `feat(irc): ...`, `fix(files): ...`, `test: ...`, `ci: ...`, `docs: ...`, `chore: ...`.
- Commits atomiques, chaque commit compile.
- **Aucun trailer de co-auteur, aucune signature d'outil, aucune mention d'assistant ou d'outil de génération** dans les commits, PR, code, commentaires ou docs. La CI le vérifie.
- `make check` doit passer avant chaque commit.

## Definition of Done (par tâche)

- [ ] Code + tests + fuzz si applicable
- [ ] `make check` vert en local, CI verte
- [ ] Aucun invariant violé
- [ ] Docs mises à jour si comportement visible modifié
- [ ] Case cochée dans `docs/ROADMAP.md`
