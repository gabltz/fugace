#!/usr/bin/env sh
# Real IRC client conformance (ROADMAP M1): WeeChat and irssi in containers
# against a fugace image. The observer client checks what goes over the wire.
# Usage: scripts/clients/run.sh [image] [weechat|irssi|all]
set -eu

IMAGE="${1:-fugace:local}"
WHICH="${2:-all}"
DIR=$(cd "$(dirname "$0")" && pwd)
NET="fugace-clients-$$"
SRV="fugace-clients-srv-$$"
CLI="fugace-clients-cli-$$"
PORT="${PORT:-26667}"
WEECHAT_IMAGE="${WEECHAT_IMAGE:-weechat/weechat:4.10.1-alpine@sha256:37c678750faab0e53bc742d8717c6869c6839815a6d3fe1132bf000c916e0ece}"
IRSSI_IMAGE="${IRSSI_IMAGE:-irssi:1.4.5@sha256:8106b7902ecdc54de2273e7d8645a4a99b83fcf110eb1ad3a3a9b898cba1e56f}"
OUT=$(mktemp -d)

cleanup() {
  docker rm -f "$SRV" "$CLI" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -rf "$OUT"
}
trap cleanup EXIT

expect() { # file, fixed string
  grep -qF -- "$2" "$1" || { echo "FAIL [$CLIENT]: missing: $2"; cat "$1"; exit 1; }
}

start_server() {
  docker rm -f "$SRV" "$CLI" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  docker network create "$NET" >/dev/null
  docker run -d --name "$SRV" --network "$NET" --network-alias fugace-it --read-only --cap-drop ALL \
    --security-opt no-new-privileges:true -p "127.0.0.1:$PORT:6667" "$IMAGE" >/dev/null
  i=0
  until printf 'QUIT\r\n' | nc -w 1 127.0.0.1 "$PORT" >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -gt 20 ] && { echo "server not ready"; exit 1; }
    sleep 0.5
  done
}

check_wire() { # observer output, peer nick, new nick
  f=$1; a=$2; b=$3
  expect "$f" ":$a!~user@fugace JOIN #general"
  expect "$f" ":$a!~user@fugace PRIVMSG #general :hello from $CLIENT"
  expect "$f" ":$a!~user@fugace PRIVMSG observer :private from $CLIENT"
  expect "$f" ":$a!~user@fugace NICK $b"
  expect "$f" ":$b!~user@fugace PRIVMSG observer :after nick change"
  expect "$f" ":$b!~user@fugace PRIVMSG #general :$(printf "\001")ACTION waves"
  expect "$f" ":$b!~user@fugace TOPIC #general :set by $CLIENT"
  expect "$f" ":$b!~user@fugace PART #general :bye"
  expect "$f" ":$b!~user@fugace JOIN #general"
  expect "$f" ":$b!~user@fugace QUIT :Quit: leaving"
  if grep -qE '([0-9]{1,3}\.){3}[0-9]{1,3}|secretlocaluser' "$f"; then
    echo "FAIL [$CLIENT]: address or local user name leaked"; exit 1
  fi
}

check_server() {
  [ -z "$(docker diff "$SRV")" ] || { echo "FAIL [$CLIENT]: docker diff not empty"; exit 1; }
  lines=$(docker logs "$SRV" 2>&1 | wc -l)
  [ "$lines" -le 1 ] || { echo "FAIL [$CLIENT]: server wrote more than the startup line"; exit 1; }
}

run_weechat() {
  CLIENT=weechat
  start_server
  docker run -d --name "$CLI" --network "$NET" "$WEECHAT_IMAGE" weechat-headless --dir /tmp/w -r \
    '/set logger.file.flush_delay 0; /server add f fugace-it/6667 -notls; /set irc.server.f.nicks wee; /set irc.server.f.username secretlocaluser; /set irc.server.f.anti_flood 0; /set irc.server.f.autojoin #general; /connect f; /wait 4 /msg -server f #general hello from weechat; /wait 5 /msg -server f observer private from weechat; /wait 6 /command -buffer irc.server.f * /nick wee2; /wait 7 /msg -server f observer after nick change; /wait 8 /command -buffer irc.f.#general * /me waves; /wait 9 /command -buffer irc.f.#general * /topic set by weechat; /wait 10 /command -buffer irc.f.#general * /part bye; /wait 11 /join -server f #general; /wait 14 /quit leaving' >/dev/null
  python3 "$DIR/observer.py" 127.0.0.1 "$PORT" wee wee2 18 >"$OUT/wire"
  check_wire "$OUT/wire" wee wee2
  docker cp -q "$CLI:/tmp/w/logs" "$OUT/weechat-logs"
  cat "$OUT"/weechat-logs/irc.f.*.weechatlog >"$OUT/client"
  expect "$OUT/client" "hello wee from observer"
  expect "$OUT/client" "private to wee"
  expect "$OUT/client" "reply to new nick"
  # history replayed on re-join (same line twice: live, then replay)
  [ "$(grep -c 'hello from weechat' "$OUT/client")" -ge 2 ] || { echo "FAIL [weechat]: no history replay"; exit 1; }
  check_server
  echo "ok: weechat ($(docker run --rm --entrypoint weechat-headless "$WEECHAT_IMAGE" --version))"
}

run_irssi() {
  CLIENT=irssi
  start_server
  docker run -d -t --name "$CLI" --network "$NET" \
    -v "$DIR/irssi.conf:/cfg/config:ro" -v "$DIR/steps.pl:/cfg/steps.pl:ro" --entrypoint sh "$IRSSI_IMAGE" \
    -c 'mkdir -p /tmp/i/scripts/autorun && cp /cfg/config /tmp/i/config && cp /cfg/steps.pl /tmp/i/scripts/autorun/ && irssi --home=/tmp/i; sleep 600' >/dev/null
  python3 "$DIR/observer.py" 127.0.0.1 "$PORT" irs irs2 75 >"$OUT/wire"
  check_wire "$OUT/wire" irs irs2
  i=0
  until docker exec "$CLI" sh -c 'test -s "/tmp/i/logs/#general.log" && test -s /tmp/i/logs/observer.log'; do
    i=$((i + 1)); [ "$i" -gt 20 ] && { echo "FAIL [irssi]: no client logs"; exit 1; }
    sleep 0.5
  done
  docker cp -q "$CLI:/tmp/i/logs" "$OUT/irssi-logs"
  cat "$OUT"/irssi-logs/*.log >"$OUT/client"
  expect "$OUT/client" "hello irs from observer"
  expect "$OUT/client" "private to irs"
  expect "$OUT/client" "reply to new nick"
  [ "$(grep -c 'hello from irssi' "$OUT/client")" -ge 2 ] || { echo "FAIL [irssi]: no history replay"; exit 1; }
  check_server
  echo "ok: irssi ($(docker run --rm "$IRSSI_IMAGE" irssi --version))"
}

case "$WHICH" in
  weechat) run_weechat ;;
  irssi) run_irssi ;;
  all) run_weechat; run_irssi ;;
  *) echo "usage: $0 [image] [weechat|irssi|all]"; exit 2 ;;
esac
