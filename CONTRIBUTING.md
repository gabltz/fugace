# Contributing to fugace

Thanks for your interest! fugace is small on purpose. The bar for changes is high: every feature must keep it ephemeral, log-free and simple to audit.

## Before you start

- **Issues first.** For anything beyond a small fix, open an issue to discuss it. A change that adds state, storage, logging, accounts or telemetry will not be accepted: these are explicit non-goals.
- **Read the design documents** in [`docs/`](docs/README.md), written in French. Precedence: [SPEC](docs/SPEC.md) > [ARCHITECTURE](docs/ARCHITECTURE.md) > the rest. Then read the [threat model](docs/THREAT_MODEL.md) and the [decision records](docs/adr/).
- **Record new decisions.** A structural choice the specification does not settle gets a short ADR (`docs/adr/00NN-*.md`) in the same pull request.
- **[`AGENTS.md`](AGENTS.md) is the binding rule set**, in French. Here are the essentials.

## Invariants (a pull request that breaks one is declined)

- **No disk writes** at runtime. The only exception is the opt-in ACME cache in `internal/tlsmgr/cache.go`.
- **No logs.** Only `cmd/fugace` may print: one startup line and fatal startup errors, without user data. No `fmt.Print*`, `log.*` or `slog.*` anywhere else; `forbidigo` enforces it.
- **No IP address** shown to other clients or kept in clear.
- **No `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `document.write` or `eval`** in `web/static`, no inline script or style, and no third-party resource (CDN, fonts, images).
- **Dependencies.** Standard library first. At most 3 direct dependencies; a new one needs an ADR (why, alternatives, maintenance, license).
- **Zero configuration required.** Every environment variable has a working default.

## Code style

- `gofumpt` + `goimports`, `golangci-lint` with no unjustified exception. An exception reads `//nolint:linter // reason`.
- **Language.** Code, identifiers, comments and commit messages are in English; design documents in `docs/` are in French.
- **Errors** are wrapped with `%w` and never ignored without a comment.
- **Globals.** No mutable global variables; inject dependencies through structs.
- **Limits** are named constants, never magic numbers.

## Tests

- **Coverage.** All code has tests, table-driven by default.
- **Race detector.** `go test -race -count=1 ./...` must pass.
- **Fuzzing.** Every function that parses external input has a fuzz target (`FuzzXxx`) with a seed corpus in `testdata/fuzz/`.
- **Threat model.** Every line of the threat model names at least one test. Keep that true when you change a defence.
- **Local runs.** Run `make check` before every commit. If you touch the container, the web client, TLS or IRC behaviour, also run the matching suites: `make smoke`, `make browser`, `make acme`, `make clients`.

## Commits and pull requests

- **Branches** are named after the work, e.g. `m8-public` for a roadmap milestone or `fix-upload-name` for a fix.
- **Commits** follow [Conventional Commits](https://www.conventionalcommits.org/): `feat(irc): …`, `fix(files): …`, `test: …`, `ci: …`, `docs: …`, `chore: …`. Keep them atomic: each commit builds.
- **No attribution metadata.** Commits, pull requests, code and docs carry no co-author trailers, no tool signatures and no mention of code-generation tools. The CI `guard` job checks it.
- **Done means:** code, tests and fuzz targets where relevant; `make check` green and CI green; docs updated when behaviour changes; the matching box ticked in [`docs/ROADMAP.md`](docs/ROADMAP.md).

## Security issues

Please do not open public issues for vulnerabilities; see [SECURITY.md](SECURITY.md).

## License

By contributing, you agree that your contributions are licensed under the [AGPL-3.0](LICENSE).
