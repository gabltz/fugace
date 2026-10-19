#!/usr/bin/env sh
# Supply-chain rules (THREAT_MODEL T12, AGENTS.md): actions pinned by full
# commit SHA (local ./ workflows excepted), base images by digest, at most 3 direct Go dependencies,
# go.sum verified in CI.
set -eu
fail() { echo "supply chain: $*"; exit 1; }
bad=$(grep -hE '^[[:space:]]*-?[[:space:]]*uses:' .github/workflows/*.yml | grep -vE 'uses:[[:space:]]+[^@[:space:]]+@[0-9a-f]{40}([[:space:]]+#[[:space:]]*v[0-9]|$)' | grep -vE 'uses:[[:space:]]+\./' || true)
[ -z "$bad" ] || fail "actions not pinned by SHA (with a version comment): $bad"
bad=$(grep -E '^(# syntax=|FROM )' Dockerfile | grep -v '@sha256:[0-9a-f]\{64\}' | grep -v '^FROM scratch' || true)
[ -z "$bad" ] || fail "images not pinned by digest: $bad"
n=$(awk '/^require \(/{b=1;next} b&&/^\)/{b=0} b&&!/\/\/ indirect/{n++} /^require [^(]/&&!/\/\/ indirect/{n++} END{print n+0}' go.mod)
[ "$n" -le 3 ] || fail "$n direct dependencies (max 3, ADR required)"
grep -q 'go mod verify' .github/workflows/ci.yml || fail "go mod verify missing from CI"
echo "ok: supply chain ($n direct dependencies)"
