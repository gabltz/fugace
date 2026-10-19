#!/usr/bin/env sh
# Runs install.sh non-interactively against an image: install, reconfigure,
# a configuration refused by the server, reinstall, uninstall.
# Usage: scripts/check-installer.sh [image]
set -eu
IMAGE="${1:-fugace:local}"
ROOT=$(cd "$(dirname "$0")/.." && pwd)
export FUGACE_INSTALL_IMAGE="$IMAGE" FUGACE_HTTP_PORT=28080 FUGACE_HTTPS_PORT=28443 FUGACE_IRC_PORT=26667 FUGACE_IRCS_PORT=26697
fail() { echo "FAIL: $*"; docker rm -f fugace >/dev/null 2>&1 || true; exit 1; }
envs() { docker inspect fugace -f '{{range .Config.Env}}{{println .}}{{end}}' | grep '^FUGACE_' || true; }

FUGACE_ADMIN_PASSWORD=supersecret FUGACE_CHANNELS='#general,#off-topic' "$ROOT/install.sh" --install --yes >/dev/null 2>&1 || fail "install"
[ "$(docker inspect fugace -f '{{.HostConfig.ReadonlyRootfs}} {{.Config.User}} {{.HostConfig.CapDrop}} {{.HostConfig.LogConfig.Type}}')" = "true 65532:65532 [ALL] none" ] || fail "hardening flags"
curl -fsk -o /dev/null https://127.0.0.1:28443/ || fail "https"
printf 'NICK t\r\nUSER t 0 * :t\r\nLIST\r\nQUIT\r\n' | nc -w 3 127.0.0.1 26667 | grep -q ' 322 t #off-topic ' || fail "channels"
envs | grep -q '^FUGACE_TLS=' && fail "default values must not be written"
echo "ok: install (hardened, HTTPS, IRC, channels, defaults not pinned)"

FUGACE_HISTORY_MAX_MB=0 "$ROOT/install.sh" --install --yes >/dev/null 2>&1 || fail "reconfigure"
envs | grep -q '^FUGACE_HISTORY_MAX_MB=0$' || fail "new value"
envs | grep -q '^FUGACE_CHANNELS=#general,#off-topic$' || fail "previous values kept"
echo "ok: reconfigure keeps previous answers"

out=$(FUGACE_TRUSTED_PROXIES=10.0.0.0/99 "$ROOT/install.sh" --install --yes 2>&1) && fail "invalid configuration accepted"
echo "$out" | grep -q 'FUGACE_TRUSTED_PROXIES: entries must be CIDR' || fail "refusal not explained: $out"
docker inspect fugace >/dev/null 2>&1 && fail "container left after a refused configuration"
echo "ok: refused configuration is explained, nothing left behind"

FUGACE_ADMIN_PASSWORD=supersecret FUGACE_CHANNELS='#general,#off-topic' "$ROOT/install.sh" --reinstall --yes >/dev/null 2>&1 || fail "reinstall"
docker inspect fugace -f '{{.State.Health.Status}}' | grep -q healthy || fail "reinstall health"
echo "ok: reinstall"

"$ROOT/install.sh" --uninstall --yes >/dev/null 2>&1 || fail "uninstall"
docker inspect fugace >/dev/null 2>&1 && fail "container left after uninstall"
echo "ok: uninstall"
