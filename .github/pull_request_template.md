## What and why

<!-- Short description; link the issue or ADR. -->

## Checklist

- [ ] Tests (table-driven), plus a fuzz target if new external input is parsed
- [ ] `make check` passes; matching suites run if relevant (`make smoke`, `make browser`, `make acme`, `make clients`)
- [ ] No invariant broken (no disk writes, no logs, no IP exposure, no DOM sinks, no new dependency without an ADR)
- [ ] Docs updated if behaviour changes (`docs/`, README, CHANGELOG)
- [ ] Threat model still maps every threat to a test
