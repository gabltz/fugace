#!/usr/bin/env sh
# ACME integration against pebble (ROADMAP M3): domain issuance served on
# 443 and 6697, no IP ever requested, failure fallback with back-off, cache.
# Usage: scripts/acme/run.sh [image]
set -eu

IMAGE="${1:-fugace:local}"
DIR=$(cd "$(dirname "$0")" && pwd)
ID=$$
NET="fugace-acme-$ID"
SUBNET="172.30.77"
PEBBLE_IMAGE="${PEBBLE_IMAGE:-ghcr.io/letsencrypt/pebble:2.10.1@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199}"
CHALL_IMAGE="${CHALL_IMAGE:-ghcr.io/letsencrypt/pebble-challtestsrv:2.10.1@sha256:12ce21884def456bcf9786542113949e1f19dc7738d2c70e156c2d0c38a1405b}"
GO_IMAGE="${GO_IMAGE:-golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d}"
WORK=$(mktemp -d)
NAMES="pebble-$ID chall-$ID ok-$ID bad-$ID cache-$ID"

cleanup() {
  for n in $NAMES; do docker rm -f "$n" >/dev/null 2>&1 || true; done
  docker volume rm "cache-$ID" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT
fail() {
  echo "FAIL: $*"
  echo "--- pebble (test CA) log tail:"; docker logs "pebble-$ID" 2>&1 | tail -n 25 || true
  exit 1
}

check() { # check.go in a Go container on the test network
  docker run --rm --network "$NET" -e GOTOOLCHAIN=local -e GOFLAGS=-mod=mod \
    -v "$DIR/check.go:/check/check.go:ro" -v "$WORK:/work" -w /check "$GO_IMAGE" go run check.go "$@"
}

wait_status() { # container, pattern
  i=0
  until docker exec "$1" /fugace status 2>/dev/null | grep -q "$2"; do
    i=$((i + 1)); [ "$i" -gt 90 ] && { docker exec "$1" /fugace status || true; fail "$1: status never matched '$2'"; }
    sleep 1
  done
}

# The pebble root replaces the image's CA bundle: mounting over an existing
# file creates no mount point, so `docker diff` stays meaningful.
fugace() { # name, ip, host, extra docker args...
  name=$1; ip=$2; host=$3; shift 3
  docker run -d --name "$name" --network "$NET" --ip "$SUBNET.$ip" --read-only --cap-drop ALL \
    --security-opt no-new-privileges:true \
    -e FUGACE_HOST="$host" -e FUGACE_ACME_DIRECTORY="https://pebble:14000/dir" \
    -v "$WORK/minica.pem:/etc/ssl/certs/ca-certificates.crt:ro" \
    "$@" "$IMAGE" >/dev/null
}

docker network create --subnet "$SUBNET.0/24" "$NET" >/dev/null
docker run -d --name "chall-$ID" --network "$NET" --ip "$SUBNET.3" --network-alias challtestsrv \
  "$CHALL_IMAGE" -defaultIPv6 "" -defaultIPv4 "$SUBNET.10" -http01 "" -https01 "" -tlsalpn01 "" >/dev/null
docker run -d --name "pebble-$ID" --network "$NET" --ip "$SUBNET.2" --network-alias pebble \
  -e PEBBLE_VA_NOSLEEP=1 -v "$DIR/pebble-config.json:/config.json:ro" \
  "$PEBBLE_IMAGE" -config /config.json -dnsserver "$SUBNET.3:8053" >/dev/null
docker cp -q "pebble-$ID:/test/certs/pebble.minica.pem" "$WORK/minica.pem"
chmod 644 "$WORK/minica.pem"
# fugace waits >= 1 h after any failure: start it only once pebble answers.
i=0
until check root https://pebble:15000 /work/minica.pem /work/root.pem >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -gt 30 ] && fail "pebble not ready"
  sleep 1
done

# 1. Domain issuance. The LAN IP stays a self-signed SAN only (ADR 0007).
fugace "ok-$ID" 10 "chat.fugace.test,192.168.1.20"
wait_status "ok-$ID" "tls:         acme"
echo "ok: certificate issued for chat.fugace.test"
s1=$(check tls "$SUBNET.10:8443" chat.fugace.test chat.fugace.test /work/root.pem)
s2=$(check tls "$SUBNET.10:8443" - chat.fugace.test /work/root.pem)
s3=$(check tls "$SUBNET.10:6697" chat.fugace.test chat.fugace.test /work/root.pem)
[ "$s1" = "$s2" ] && [ "$s2" = "$s3" ] || fail "different certificates: $s1 $s2 $s3"
echo "ok: same trusted certificate on 443 (with and without SNI) and 6697, no IP in it"
check http "https://$SUBNET.10:8443/" chat.fugace.test 200 Strict-Transport-Security
check http "http://$SUBNET.10:8080/" chat.fugace.test 301 Location
echo "ok: HSTS on the domain, http redirects"
docker exec "ok-$ID" /fugace status | grep -q "publicly trusted" || fail "status does not report a trusted certificate"
d=$(docker diff "ok-$ID"); [ -z "$d" ] || fail "docker diff not empty without FUGACE_CERT_CACHE: $d"
echo "ok: docker diff empty (no cache configured)"

# 2. Failure: the name resolves to an address where nothing answers.
docker run --rm --network "$NET" "$GO_IMAGE" wget -q -O- --post-data '{"host":"bad.fugace.test","addresses":["127.0.0.1"]}' \
  http://challtestsrv:8055/add-a >/dev/null
fugace "bad-$ID" 11 "bad.fugace.test"
wait_status "bad-$ID" "last attempt failed"
st=$(docker exec "bad-$ID" /fugace status)
echo "$st" | grep -q "tls:         self-signed" || fail "no self-signed fallback: $st"
next=$(echo "$st" | sed -n 's/.*next attempt \([0-9T:Z-]*\).*/\1/p')
now=$(date -u +%s)
[ -n "$next" ] && [ $(( $(date -u -d "$next" +%s) - now )) -ge 3500 ] || fail "retry sooner than 1 h: $st"
check http "https://$SUBNET.11:8443/" bad.fugace.test 200 >/dev/null
echo "ok: failed issuance -> self-signed, next attempt >= 1 h ($(echo "$st" | grep acme: | sed 's/^acme: *//'))"

# 3. Cache: survives a re-creation without a new issuance; files in 0600.
docker volume create "cache-$ID" >/dev/null
docker run --rm -v "cache-$ID:/cache" "$GO_IMAGE" chown 65532:65532 /cache
docker rm -f "ok-$ID" >/dev/null
fugace "cache-$ID" 10 "chat.fugace.test" -e FUGACE_CERT_CACHE=/cache -v "cache-$ID:/cache"
wait_status "cache-$ID" "tls:         acme"
c1=$(check tls "$SUBNET.10:8443" chat.fugace.test chat.fugace.test /work/root.pem)
docker rm -f "cache-$ID" >/dev/null
fugace "cache-$ID" 10 "chat.fugace.test" -e FUGACE_CERT_CACHE=/cache -v "cache-$ID:/cache"
wait_status "cache-$ID" "tls:         acme"
c2=$(check tls "$SUBNET.10:8443" chat.fugace.test chat.fugace.test /work/root.pem)
[ "$c1" = "$c2" ] || fail "cached certificate not reused ($c1 vs $c2)"
perms=$(docker run --rm -v "cache-$ID:/cache" "$GO_IMAGE" sh -c 'stat -c "%a %n" /cache/*')
echo "$perms" | grep -vq '^600 ' && fail "cache permissions: $perms"
echo "ok: cache reused after re-creation, files 0600"
