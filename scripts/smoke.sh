#!/usr/bin/env sh
# Container smoke test: health, HTTPS, IRC, upload, and nothing written to
# the filesystem nor to the process output beyond the startup line.
set -eu

IMAGE="${1:-fugace:local}"
NAME="fugace-smoke-$$"

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

# Default log driver on purpose: the output is inspected at the end (T1).
# SMOKE_PLATFORM runs another architecture (e.g. linux/arm64 under QEMU).
docker run -d --name "$NAME" ${SMOKE_PLATFORM:+--platform "$SMOKE_PLATFORM"} --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true \
  -p 18080:8080 -p 18443:8443 -p 16667:6667 -p 16697:6697 \
  "$IMAGE" >/dev/null

i=0
until curl -fsS http://127.0.0.1:18080/healthz >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -gt 90 ] && { echo "healthz timeout"; exit 1; }
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

# Upload with a token bound to a live IRC connection (T9).
if [ "${SMOKE_TLS:-0}" = "1" ] && [ "${SMOKE_IRC:-0}" = "1" ]; then
  tmp=$(mktemp -d)
  mkfifo "$tmp/in"
  nc 127.0.0.1 16667 <"$tmp/in" >"$tmp/out" &
  ncpid=$!
  exec 3>"$tmp/in"
  printf 'NICK smokeup\r\nUSER s 0 * :s\r\n' >&3
  i=0
  until grep -q 'fugace/UPLOAD=' "$tmp/out" 2>/dev/null; do
    i=$((i + 1)); [ "$i" -gt 50 ] && { echo "no upload token in 005"; exit 1; }
    sleep 0.1
  done
  token=$(grep -o 'fugace/UPLOAD=[A-Za-z0-9_-]*' "$tmp/out" | head -1 | cut -d= -f2)
  printf 'smoke file\n' >"$tmp/note.txt"
  url=$(curl -fsSk -H "Authorization: Bearer $token" --data-binary @"$tmp/note.txt" \
    "https://127.0.0.1:18443/upload?name=note.txt" | sed -n 's/.*"url":"\([^"]*\)".*/\1/p')
  path=${url#https://*/}
  hdrs=$(curl -fsSk -D - -o "$tmp/got" "https://127.0.0.1:18443/$path")
  cmp -s "$tmp/note.txt" "$tmp/got" || { echo "downloaded file differs"; exit 1; }
  echo "$hdrs" | grep -qi '^content-disposition: attachment' || { echo "file not served as attachment"; exit 1; }
  echo "$hdrs" | grep -qi "^content-security-policy: default-src 'none'; img-src 'self'; sandbox" || { echo "file CSP missing"; exit 1; }
  echo "ok: upload and download"
  printf 'QUIT\r\n' >&3
  exec 3>&-
  wait "$ncpid" 2>/dev/null || true
  code=$(curl -sk -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $token" --data-binary @"$tmp/note.txt" \
    "https://127.0.0.1:18443/upload?name=late.txt")
  [ "$code" = "401" ] || { echo "token still valid after QUIT: $code"; exit 1; }
  echo "ok: token revoked after QUIT"
  rm -rf "$tmp"
fi

# Nothing written to the container filesystem
if [ -n "$(docker diff "$NAME")" ]; then
  echo "docker diff not empty"; docker diff "$NAME"; exit 1
fi
echo "ok: docker diff empty"

# Nothing on the process output but the startup line, no user data (T1).
out=$(docker logs "$NAME" 2>/dev/null)
err=$(docker logs "$NAME" 2>&1 >/dev/null)
lines=$(printf '%s\n' "$out" | grep -c . || true)
[ "$lines" = "1" ] && [ -z "$err" ] || { echo "unexpected output:"; docker logs "$NAME"; exit 1; }
printf '%s\n' "$out" | grep -Eq '^fugace [^ ]+ ' || { echo "unexpected startup line: $out"; exit 1; }
if printf '%s\n' "$out" | grep -Eq 'smoke|127\.0\.0\.1|172\.[0-9]+\.'; then
  echo "user data in output: $out"; exit 1
fi
echo "ok: output is the startup line only"
