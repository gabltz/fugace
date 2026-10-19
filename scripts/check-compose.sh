#!/usr/bin/env sh
# Guards the container hardening of compose.yaml (THREAT_MODEL T1, T13):
# fails if any protection is removed or weakened.
set -eu
F="${1:-compose.yaml}"
fail() { echo "compose hardening: $*"; exit 1; }
need() { grep -Eq "$1" "$F" || fail "missing: $2"; }
need '^[[:space:]]+read_only:[[:space:]]+true' "read_only: true"
need '^[[:space:]]+cap_drop:[[:space:]]+\[ALL\]' "cap_drop: [ALL]"
need 'no-new-privileges:true' "no-new-privileges"
need '^[[:space:]]+user:[[:space:]]+"65532:65532"' "non-root user"
need '^[[:space:]]+driver:[[:space:]]+none' "logging driver none"
need '^[[:space:]]+core:[[:space:]]+0' "ulimit core=0"
need '^[[:space:]]+pids_limit:' "pids_limit"
mem=$(sed -n 's/^[[:space:]]*mem_limit:[[:space:]]*\([^ #]*\).*/\1/p' "$F")
swap=$(sed -n 's/^[[:space:]]*memswap_limit:[[:space:]]*\([^ #]*\).*/\1/p' "$F")
[ -n "$mem" ] && [ "$mem" = "$swap" ] || fail "memswap_limit must equal mem_limit (no swap)"
! grep -Eq '^[[:space:]]+(privileged|cap_add|network_mode:[[:space:]]+host)' "$F" || fail "privileged, cap_add or host network"
! grep -Eq '^[[:space:]]+-[[:space:]]+"?[^#]*:/(var/run/docker.sock|proc|sys)' "$F" || fail "dangerous mount"
echo "ok: compose hardening"
