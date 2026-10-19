#!/usr/bin/env sh
# Container smoke test. Extended in M5 (upload, docker logs).
set -eu

IMAGE="${1:-fugace:local}"
NAME="fugace-smoke-$$"

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d --name "$NAME" --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true --log-driver none \
  -p 18080:8080 -p 18443:8443 -p 16667:6667 -p 16697:6697 \
  "$IMAGE" >/dev/null

i=0
until curl -fsS http://127.0.0.1:18080/healthz >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -gt 30 ] && { echo "healthz timeout"; exit 1; }
  sleep 1
done
echo "ok: healthz"

# Built-in HTTPS (enabled from M3 onward): self-signed by default, plain HTTP redirects
if [ "${SMOKE_TLS:-0}" = "1" ]; then
  code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/)
  [ "$code" = "301" ] || { echo "http should redirect, got $code"; exit 1; }
  echo "ok: http -> https redirect"
  curl -fsSk https://127.0.0.1:18443/ >/dev/null || { echo "https unreachable"; exit 1; }
  echo "ok: https (self-signed)"
  docker exec "$NAME" /fugace status | grep -qi 'self-signed' || { echo "status: unexpected tls mode"; exit 1; }
  echo "ok: status"
  out=$(printf 'NICK smoketls\r\nUSER s 0 * :s\r\nQUIT\r\n' | timeout 5 openssl s_client -quiet -connect 127.0.0.1:16697 2>/dev/null || true)
  echo "$out" | grep -q ' 001 smoketls ' || { echo "IRC over TLS welcome missing"; exit 1; }
  echo "ok: irc over tls"
fi

# IRC registration (enabled from M1 onward)
if [ "${SMOKE_IRC:-0}" = "1" ]; then
  out=$(printf 'NICK smoke\r\nUSER smoke 0 * :smoke\r\nQUIT\r\n' | nc -w 3 127.0.0.1 16667 || true)
  echo "$out" | grep -q ' 001 smoke ' || { echo "IRC welcome missing"; exit 1; }
  echo "ok: irc welcome"
fi

# Nothing written to the container filesystem
if [ -n "$(docker diff "$NAME")" ]; then
  echo "docker diff not empty"; docker diff "$NAME"; exit 1
fi
echo "ok: docker diff empty"
