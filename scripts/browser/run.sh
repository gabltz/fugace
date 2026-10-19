#!/usr/bin/env sh
# Headless browser checks of the web client: scripts/browser/run.sh [image]
set -eu
IMAGE="${1:-fugace:local}"
DIR=$(cd "$(dirname "$0")" && pwd)
NET="fugace-browser-$$"
SRV="fugace-browser-srv-$$"
PW_IMAGE="${PW_IMAGE:-mcr.microsoft.com/playwright:v1.63.0-noble@sha256:eff16c30e6f3f4af0a03fa4b706120d5e9b0891c344a27d64559aff5900a4a27}"
cleanup() { docker rm -f "$SRV" >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; }
trap cleanup EXIT
docker network create "$NET" >/dev/null
docker run -d --name "$SRV" --network "$NET" --network-alias fugace-it --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true "$IMAGE" >/dev/null
docker run --rm --network "$NET" -v "$DIR:/test:ro" -e BASE -e IRC --ipc=host "$PW_IMAGE" sh -c \
  'mkdir -p /tmp/b && cd /tmp/b && cp /test/test.mjs . && npm init -y >/dev/null &&
   npm install --no-audit --no-fund --silent playwright@1.63.0 axe-core@4.13.0 >/dev/null && node test.mjs'
[ -z "$(docker diff "$SRV")" ] || { echo "FAIL: docker diff not empty"; exit 1; }
echo "ok: docker diff empty"
