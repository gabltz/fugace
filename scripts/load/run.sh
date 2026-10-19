#!/usr/bin/env sh
# Load, flood and slowloris runs for docs/BENCH.md: scripts/load/run.sh [image]
# Clients use ~60 source addresses (aliases inside the load container only),
# so that fugace's per-source limits stay untouched.
set -eu
IMAGE="${1:-fugace:local}"
DIR=$(cd "$(dirname "$0")" && pwd)
ID=$$
NET="fugace-load-$ID"
SUBNET="172.31.77"
GO_IMAGE="${GO_IMAGE:-golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d}"
CLIENTS="${CLIENTS:-500}"
RATE="${RATE:-50}"
DURATION="${DURATION:-60s}"
STATS=$(mktemp)
cleanup() { docker rm -f "srv-$ID" >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; rm -f "$STATS"; }
trap cleanup EXIT

docker network create --subnet "$SUBNET.0/24" "$NET" >/dev/null
# Same limits as compose.yaml.
docker run -d --name "srv-$ID" --network "$NET" --ip "$SUBNET.10" --network-alias fugace --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true --memory 512m --memory-swap 512m --pids-limit 128 \
  --ulimit nofile=4096:4096 --log-driver none "$IMAGE" >/dev/null
sleep 2
idle=$(docker stats --no-stream --format '{{.MemUsage}}' "srv-$ID" | cut -d/ -f1)
echo "idle: RAM $idle"

run() { # mode, locals, extra args
  docker run --rm --network "$NET" --ip "$SUBNET.99" --cap-add NET_ADMIN -e GOTOOLCHAIN=local \
    -v "$DIR/load.go:/load/load.go:ro" -w /load "$GO_IMAGE" sh -c "
      for i in \$(seq $(echo "$2" | sed 's/.*\.\([0-9]*\)-\([0-9]*\)/\1 \2/')); do ip addr add $SUBNET.\$i/24 dev eth0; done
      go build -o /tmp/load load.go && /tmp/load -mode $1 -irc fugace:6667 -https fugace:8443 -locals $2 $3"
}

# Sample CPU/RAM of the server during the chat run.
( while docker inspect "srv-$ID" >/dev/null 2>&1; do
    docker stats --no-stream --format '{{.CPUPerc}} {{.MemUsage}}' "srv-$ID" 2>/dev/null; sleep 2
  done ) >"$STATS" 2>/dev/null &
sampler=$!
run chat "$SUBNET.100-159" "-clients $CLIENTS -rate $RATE -duration $DURATION"
kill "$sampler" 2>/dev/null || true
awk '{gsub("%","",$1); gsub(",",".",$1); v=$1+0; cpu+=v; if (v>cmax) cmax=v; n++; m=$2} END {printf "chat: server CPU avg %.1f%% max %.1f%%, RAM at end %s\n", cpu/n, cmax, m}' "$STATS"
run flood "$SUBNET.160-162" ""
run slowloris "$SUBNET.170-171" ""
echo "after: RAM $(docker stats --no-stream --format '{{.MemUsage}}' "srv-$ID" | cut -d/ -f1), docker diff: $(docker diff "srv-$ID" | wc -l) changes"
