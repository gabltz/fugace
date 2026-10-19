#!/usr/bin/env bash
# fugace installer: install, reinstall from scratch, or uninstall fugace as a
# hardened Docker container, using the latest official image. Standalone, no
# clone needed:
#
#   curl -fsSL https://github.com/gabltz/fugace/releases/latest/download/install.sh | bash
#
# Questions are read from the terminal, so piping the script into bash works.
# Non-interactive use: install.sh --install|--reinstall|--uninstall [--yes]
# with FUGACE_* variables in the environment as answers; host ports can be set
# with FUGACE_HTTP_PORT, FUGACE_HTTPS_PORT, FUGACE_IRC_PORT, FUGACE_IRCS_PORT,
# and FUGACE_INSTALL_IMAGE replaces the official image (tests).
#
# SPDX-License-Identifier: AGPL-3.0-only
set -euo pipefail
# Byte semantics for regexes and lengths, as fugace validates bytes too.
export LC_ALL=C

readonly REPO="gabltz/fugace"
readonly IMAGE_REPO="ghcr.io/gabltz/fugace"
readonly NAME="fugace"
readonly CACHE_VOLUME="fugace-cache"
readonly CACHE_PATH="/cache"
readonly CERTS_PATH="/certs"
readonly HEALTH_TIMEOUT=90
# Only used to give the certificate cache volume to the image's user (65532).
readonly HELPER_IMAGE="busybox:1.37@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

ACTION=""
ASSUME_YES=0
IMAGE=""
# Answers, filled by configure (via printf -v).
C_HOST="" C_TLS="" C_PUBLIC="" C_PROXIES="" C_PASSWORD="" C_ADMIN="" C_CHANNELS="" C_HISTORY=""
C_MAXUP="" C_FILESMAX="" C_TTL="" C_CACHE="" C_CERTS="" C_ACME="" C_HTTPADDR="" C_HTTPSADDR=""
C_IRCADDR="" C_IRCSADDR="" P_HTTP="" P_HTTPS="" P_IRC="" P_IRCS=""
VERSION=""
DOCKER=(docker)

# ---------------------------------------------------------------- output --
if [ -t 2 ]; then
  B=$'\e[1m'; DIM=$'\e[2m'; RED=$'\e[31m'; GRN=$'\e[32m'; YEL=$'\e[33m'; CYN=$'\e[36m'; RST=$'\e[0m'
else
  B=""; DIM=""; RED=""; GRN=""; YEL=""; CYN=""; RST=""
fi
say()  { printf '%s\n' "$*" >&2; }
info() { printf '%s›%s %s\n' "$CYN" "$RST" "$*" >&2; }
ok()   { printf '%s✓%s %s\n' "$GRN" "$RST" "$*" >&2; }
warn() { printf '%s!%s %s\n' "$YEL" "$RST" "$*" >&2; }
die()  { printf '%s✗ %s%s\n' "$RED" "$*" "$RST" >&2; exit 1; }
section() { printf '\n%s%s%s\n' "$B" "$*" "$RST" >&2; }

usage() {
  cat >&2 <<'EOF'
Usage: install.sh [--install | --reinstall | --uninstall] [--yes] [--version vX.Y.Z]

  --install     install fugace (or reconfigure it if already installed)
  --reinstall   remove everything (container, certificate cache, image), then install
  --uninstall   remove fugace
  --yes         do not ask: take answers from FUGACE_* variables or defaults
  --version     use this release instead of the latest one

Without an action, an interactive menu is shown.
EOF
}

# ------------------------------------------------------------- arguments --
while [ $# -gt 0 ]; do
  case "$1" in
    --install) ACTION=install ;;
    --reinstall) ACTION=reinstall ;;
    --uninstall) ACTION=uninstall ;;
    -y|--yes) ASSUME_YES=1 ;;
    --version) [ $# -ge 2 ] || die "--version needs a value"; VERSION=$2; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; die "unknown option: $1" ;;
  esac
  shift
done

# Answers come from the terminal even when the script itself is piped in.
if [ "$ASSUME_YES" = 0 ]; then
  if { exec 3</dev/tty; } 2>/dev/null; then :; else
    die "no terminal to ask questions; use --install/--reinstall/--uninstall with --yes"
  fi
fi

# ask VAR "Label" "default" — Enter keeps the default, "-" clears it.
ask() {
  local __var=$1 label=$2 def=$3 ans
  if [ "$ASSUME_YES" = 1 ]; then printf -v "$__var" '%s' "$def"; return; fi
  printf '  %s %s[%s]%s: ' "$label" "$DIM" "${def:-empty}" "$RST" >&2
  IFS= read -r ans <&3 || ans=""
  case "$ans" in
    "") ans=$def ;;
    -) ans="" ;;
  esac
  printf -v "$__var" '%s' "$ans"
}

# ask_secret VAR "Label" "current" — input is not echoed.
ask_secret() {
  local __var=$1 label=$2 cur=$3 ans hint="empty"
  if [ "$ASSUME_YES" = 1 ]; then printf -v "$__var" '%s' "$cur"; return; fi
  [ -n "$cur" ] && hint="keep current"
  printf '  %s %s[%s, "-" to clear]%s: ' "$label" "$DIM" "$hint" "$RST" >&2
  IFS= read -rs ans <&3 || ans=""
  printf '\n' >&2
  case "$ans" in
    "") ans=$cur ;;
    -) ans="" ;;
  esac
  printf -v "$__var" '%s' "$ans"
}

# confirm "Question" default(y|n)
confirm() {
  local q=$1 def=${2:-n} ans
  if [ "$ASSUME_YES" = 1 ]; then [ "$def" = y ]; return; fi
  local hint="y/N"; [ "$def" = y ] && hint="Y/n"
  printf '%s [%s]: ' "$q" "$hint" >&2
  IFS= read -r ans <&3 || ans=""
  ans=${ans:-$def}
  case "$ans" in [yYoO]*) return 0 ;; *) return 1 ;; esac
}

# ---------------------------------------------------------- validations --
is_uint()     { [[ $1 =~ ^[0-9]+$ ]]; }
in_range()    { is_uint "$1" && [ "$1" -ge "$2" ] && [ "$1" -le "$3" ]; }
is_port()     { in_range "$1" 1 65535; }
is_duration() { [[ $1 =~ ^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$ ]]; }
is_addr()     { [[ $1 =~ ^(\[[0-9a-fA-F:]+\]|[0-9.]+|localhost)?:[0-9]{1,5}$ ]] && is_port "${1##*:}"; }
is_channels() { # "#name[,#name...]", printable ASCII, 32 bytes max each
  local LC_ALL=C c
  local IFS=,
  [[ $1 != *,, && $1 != ,* && $1 != *, ]] || return 1
  for c in $1; do [[ $c =~ ^#[[:graph:]]{1,31}$ ]] || return 1; done
}
is_hosts()    { [[ $1 =~ ^[]A-Za-z0-9.:[-]+(,[]A-Za-z0-9.:[-]+)*$ ]]; }
is_url()      { [[ $1 =~ ^https?://[^[:space:]/?#]+(/[^[:space:]?#]*)?$ ]]; }
is_cidrs()    { [[ $1 =~ ^[0-9A-Fa-f.:/]+(,[0-9A-Fa-f.:/]+)*$ ]]; }

# ask_valid VAR "Label" "default" check "error" — loops until check passes
# (an empty answer is accepted when allow_empty=1).
ask_valid() {
  local __var=$1 label=$2 def=$3 check=$4 err=$5 allow_empty=${6:-1} v
  while :; do
    ask v "$label" "$def"
    if { [ -z "$v" ] && [ "$allow_empty" = 1 ]; } || { [ -n "$v" ] && "$check" "$v"; }; then
      printf -v "$__var" '%s' "$v"; return
    fi
    [ "$ASSUME_YES" = 1 ] && die "$label: $err"
    warn "$err"
  done
}

# ------------------------------------------------------------ docker ----
need_docker() {
  command -v docker >/dev/null 2>&1 || die "Docker is required: https://docs.docker.com/engine/install/"
  if docker info >/dev/null 2>&1; then return; fi
  command -v sudo >/dev/null 2>&1 || die "cannot reach the Docker daemon (is it running? is your user in the docker group?)"
  warn "Docker needs elevated rights here; using sudo."
  DOCKER=(sudo docker)
  "${DOCKER[@]}" info >/dev/null 2>&1 || die "cannot reach the Docker daemon, even with sudo"
}
d() { "${DOCKER[@]}" "$@"; }

installed() { d container inspect "$NAME" >/dev/null 2>&1; }

# resolve_image sets IMAGE to the latest (or requested) release.
resolve_image() {
  if [ -n "${FUGACE_INSTALL_IMAGE:-}" ]; then IMAGE=$FUGACE_INSTALL_IMAGE; return; fi
  local tag=$VERSION
  if [ -z "$tag" ]; then
    tag=$(curl -fsSL --max-time 10 "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null |
      sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1) || true
  fi
  if [ -n "$tag" ]; then IMAGE="$IMAGE_REPO:${tag#v}"; else IMAGE="$IMAGE_REPO:latest"; fi
}

pull_image() {
  if [ -n "${FUGACE_INSTALL_IMAGE:-}" ] && d image inspect "$IMAGE" >/dev/null 2>&1; then
    info "Using local image $IMAGE"
    return
  fi
  info "Pulling $IMAGE"
  d pull -q "$IMAGE" >/dev/null || die "cannot pull $IMAGE"
  if command -v cosign >/dev/null 2>&1 && [ -z "${FUGACE_INSTALL_IMAGE:-}" ]; then
    if cosign verify "$IMAGE" \
      --certificate-identity-regexp "^https://github.com/$REPO/.github/workflows/release.yml@refs/tags/v" \
      --certificate-oidc-issuer https://token.actions.githubusercontent.com >/dev/null 2>&1; then
      ok "cosign signature verified"
    else
      die "cosign signature verification FAILED for $IMAGE"
    fi
  else
    info "Tip: install cosign to have the image signature checked automatically."
  fi
}

# current_env prints the FUGACE_* variables of the installed container.
current_env() { d container inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$NAME" 2>/dev/null | grep '^FUGACE_' || true; }
current_port() { d container port "$NAME" "$1/tcp" 2>/dev/null | head -n1 | sed 's/.*://' || true; }
current_mount() { d container inspect -f "{{range .Mounts}}{{if eq .Destination \"$1\"}}{{.Source}}{{end}}{{end}}" "$NAME" 2>/dev/null || true; }

remove_container() {
  if installed; then
    d rm -f "$NAME" >/dev/null && ok "container removed"
  fi
}

port_busy() {
  command -v ss >/dev/null 2>&1 || return 1
  ss -Hltn "sport = :$1" 2>/dev/null | grep -q .
}

host_ip() {
  local ip=""
  if command -v ip >/dev/null 2>&1; then
    ip=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit }}')
  fi
  [ -z "$ip" ] && command -v hostname >/dev/null 2>&1 && ip=$(hostname -I 2>/dev/null | awk '{print $1}')
  printf '%s' "${ip:-<this-machine>}"
}

# ------------------------------------------------------------ configure --
configure() {
  # Defaults: environment > current container > built-in.
  local cur
  cur=$(current_env)
  pick() { # name builtin: environment > current container > builtin
    local n=$1 line
    if [ -n "${!n+x}" ]; then printf '%s' "${!n}"; return; fi
    line=$(printf '%s\n' "$cur" | grep -m1 "^$n=" || true)
    if [ -n "$line" ]; then printf '%s' "${line#*=}"; else printf '%s' "$2"; fi
  }
  port_default() { # installer variable, container port, builtin
    local n=$1 v
    if [ -n "${!n:-}" ]; then printf '%s' "${!n}"; return; fi
    v=$(current_port "$2")
    printf '%s' "${v:-$3}"
  }

  say ""
  say "${DIM}Press Enter to keep the value in brackets, or type \"-\" to clear it.${RST}"

  section "Network"
  say "  ${DIM}Domain(s) for a Let's Encrypt certificate, and/or the IP of this machine.${RST}"
  say "  ${DIM}Empty = local only: self-signed HTTPS for use on your local network (browser warning).${RST}"
  ask_valid C_HOST "Host (FUGACE_HOST)" "$(pick FUGACE_HOST "")" is_hosts "comma-separated domains or IPs, no spaces"
  ask_valid C_TLS "TLS mode: auto, or off behind your own proxy (FUGACE_TLS)" "$(pick FUGACE_TLS auto)" is_tls 'must be "auto" or "off"' 0
  ask_valid P_HTTP "Host port for HTTP" "$(port_default FUGACE_HTTP_PORT 8080 80)" is_port "port 1-65535" 0
  if [ "$C_TLS" != off ]; then
    ask_valid P_HTTPS "Host port for HTTPS" "$(port_default FUGACE_HTTPS_PORT 8443 443)" is_port "port 1-65535" 0
    ask_valid P_IRCS "Host port for IRC over TLS, \"none\" to disable" "$(port_default FUGACE_IRCS_PORT 6697 6697)" is_port_or_none "port 1-65535 or none" 0
  else
    P_HTTPS=none; P_IRCS=none
  fi
  ask_valid P_IRC "Host port for IRC (plain text), \"none\" to disable" "$(port_default FUGACE_IRC_PORT 6667 6667)" is_port_or_none "port 1-65535 or none" 0
  ask_valid C_PUBLIC "Public base URL for file links (FUGACE_PUBLIC_URL)" "$(pick FUGACE_PUBLIC_URL "")" is_url "absolute http(s) URL without query"
  ask_valid C_PROXIES "Trusted reverse proxies, CIDRs (FUGACE_TRUSTED_PROXIES)" "$(pick FUGACE_TRUSTED_PROXIES "")" is_cidrs "comma-separated CIDRs or IPs"

  section "Access"
  local pw_cur adm_cur
  pw_cur=$(pick FUGACE_PASSWORD "")
  adm_cur=$(pick FUGACE_ADMIN_PASSWORD "")
  say "  ${DIM}Server password: empty = anyone who can reach the server can join.${RST}"
  ask_secret C_PASSWORD "Server password (FUGACE_PASSWORD)" "$pw_cur"
  [[ $C_PASSWORD =~ [[:cntrl:]] ]] && die "the server password must not contain control characters"
  say "  ${DIM}Admin password (8+ characters): creates/deletes channels, clears history. Empty = no admin.${RST}"
  while :; do
    ask_secret C_ADMIN "Admin password (FUGACE_ADMIN_PASSWORD)" "$adm_cur"
    if [ -z "$C_ADMIN" ] || { [ ${#C_ADMIN} -ge 8 ] && [ ${#C_ADMIN} -le 128 ]; }; then break; fi
    [ "$ASSUME_YES" = 1 ] && die "the admin password needs 8 to 128 characters"
    warn "the admin password needs 8 to 128 characters"
  done

  section "Chat"
  ask_valid C_CHANNELS "Channels created at startup (FUGACE_CHANNELS)" "$(pick FUGACE_CHANNELS "#general")" is_channels "comma-separated #names, printable ASCII, 32 bytes max"
  ask_valid C_HISTORY "Channel history RAM budget in MB, 0 = no history (FUGACE_HISTORY_MAX_MB)" "$(pick FUGACE_HISTORY_MAX_MB 64)" is_history "integer 0-4096"

  section "Files"
  ask_valid C_MAXUP "Max size per file in MB (FUGACE_MAX_UPLOAD_MB)" "$(pick FUGACE_MAX_UPLOAD_MB 10)" is_maxup "integer 1-100"
  ask_valid C_FILESMAX "RAM budget for all files in MB (FUGACE_FILES_MAX_MB)" "$(pick FUGACE_FILES_MAX_MB 256)" is_filesmax "integer 1-4096"
  ask_valid C_TTL "File lifetime, e.g. 30m, 1h (FUGACE_FILE_TTL)" "$(pick FUGACE_FILE_TTL 1h)" is_duration "Go duration such as 30m or 1h (1 minute to 24 hours)"
  if [ -n "$C_MAXUP" ] && [ -n "$C_FILESMAX" ] && [ "$C_MAXUP" -gt "$C_FILESMAX" ]; then
    die "the max size per file cannot exceed the total file budget"
  fi

  section "Certificates"
  local cache_def=n certs_cur
  [ -n "$(current_mount "$CACHE_PATH")" ] || [ -n "${FUGACE_CERT_CACHE:-}" ] && cache_def=y
  if [ -n "$C_HOST" ]; then
    say "  ${DIM}Keeps the Let's Encrypt account and certificate across re-installs (avoids rate limits).${RST}"
    say "  ${DIM}This is the only thing fugace can ever write to disk (Docker volume \"$CACHE_VOLUME\").${RST}"
    if confirm "  Keep the certificate in a cache volume (FUGACE_CERT_CACHE)?" "$cache_def"; then C_CACHE=$CACHE_PATH; else C_CACHE=""; fi
  else
    C_CACHE=""
  fi
  certs_cur=$(current_mount "$CERTS_PATH")
  say "  ${DIM}Optional: a directory holding your own tls.crt and tls.key (used before anything else).${RST}"
  ask C_CERTS "Your own certificate directory" "${FUGACE_CERTS_DIR:-$certs_cur}"
  if [ -n "$C_CERTS" ]; then
    [ -f "$C_CERTS/tls.crt" ] && [ -f "$C_CERTS/tls.key" ] || die "$C_CERTS must contain tls.crt and tls.key"
    C_CERTS=$(cd "$C_CERTS" && pwd)
  fi

  if [ "$ASSUME_YES" = 1 ] || confirm "Configure advanced settings (ACME directory, listen addresses inside the container)?" n; then
    section "Advanced"
    ask_valid C_ACME "ACME directory URL (FUGACE_ACME_DIRECTORY)" "$(pick FUGACE_ACME_DIRECTORY "")" is_url "https URL"
    ask_valid C_HTTPADDR "HTTP listen address in the container (FUGACE_HTTP_ADDR)" "$(pick FUGACE_HTTP_ADDR "")" is_addr 'host:port or :port'
    ask_valid C_HTTPSADDR "HTTPS listen address in the container (FUGACE_HTTPS_ADDR)" "$(pick FUGACE_HTTPS_ADDR "")" is_addr 'host:port or :port'
    ask_valid C_IRCADDR "IRC listen address in the container (FUGACE_IRC_ADDR)" "$(pick FUGACE_IRC_ADDR "")" is_addr 'host:port or :port'
    ask_valid C_IRCSADDR "IRC TLS listen address in the container (FUGACE_IRCS_ADDR)" "$(pick FUGACE_IRCS_ADDR "")" is_addr 'host:port or :port'
  else
    C_ACME=$(pick FUGACE_ACME_DIRECTORY ""); C_HTTPADDR=$(pick FUGACE_HTTP_ADDR ""); C_HTTPSADDR=$(pick FUGACE_HTTPS_ADDR "")
    C_IRCADDR=$(pick FUGACE_IRC_ADDR ""); C_IRCSADDR=$(pick FUGACE_IRCS_ADDR "")
  fi
}
is_tls() { [ "$1" = auto ] || [ "$1" = off ]; }
is_port_or_none() { [ "$1" = none ] || is_port "$1"; }
is_history() { in_range "$1" 0 4096; }
is_maxup() { in_range "$1" 1 100; }
is_filesmax() { in_range "$1" 1 4096; }

# ----------------------------------------------------------------- run ----
container_port() { # FUGACE_*_ADDR value, default port
  local a=$1
  if [ -n "$a" ]; then printf '%s' "${a##*:}"; else printf '%s' "$2"; fi
}

run_container() {
  local envs=() e
  # Only non-default values are passed, so that future releases keep their
  # own defaults for everything left untouched.
  add_env() { # name value [builtin default]
    if [ -n "$2" ] && [ "$2" != "${3:-}" ]; then envs+=(-e "$1=$2"); fi
  }
  add_env FUGACE_HOST "$C_HOST"
  add_env FUGACE_TLS "$C_TLS" "auto"
  add_env FUGACE_PUBLIC_URL "$C_PUBLIC"
  add_env FUGACE_TRUSTED_PROXIES "$C_PROXIES"
  add_env FUGACE_PASSWORD "$C_PASSWORD"
  add_env FUGACE_ADMIN_PASSWORD "$C_ADMIN"
  add_env FUGACE_CHANNELS "$C_CHANNELS" "#general"
  add_env FUGACE_HISTORY_MAX_MB "$C_HISTORY" "64"
  add_env FUGACE_MAX_UPLOAD_MB "$C_MAXUP" "10"
  add_env FUGACE_FILES_MAX_MB "$C_FILESMAX" "256"
  add_env FUGACE_FILE_TTL "$C_TTL" "1h"
  add_env FUGACE_CERT_CACHE "$C_CACHE"
  add_env FUGACE_ACME_DIRECTORY "$C_ACME"
  add_env FUGACE_HTTP_ADDR "$C_HTTPADDR"
  add_env FUGACE_HTTPS_ADDR "$C_HTTPSADDR"
  add_env FUGACE_IRC_ADDR "$C_IRCADDR"
  add_env FUGACE_IRCS_ADDR "$C_IRCSADDR"
  [ "$P_IRC" = none ] && envs+=(-e "FUGACE_IRC_ADDR=")
  [ "$P_IRCS" = none ] && envs+=(-e "FUGACE_IRCS_ADDR=")

  local ports=(-p "$P_HTTP:$(container_port "$C_HTTPADDR" 8080)")
  [ "$C_TLS" != off ] && ports+=(-p "$P_HTTPS:$(container_port "$C_HTTPSADDR" 8443)")
  [ "$P_IRC" != none ] && ports+=(-p "$P_IRC:$(container_port "$C_IRCADDR" 6667)")
  [ "$P_IRCS" != none ] && ports+=(-p "$P_IRCS:$(container_port "$C_IRCSADDR" 6697)")

  local mounts=()
  if [ -n "$C_CACHE" ]; then
    d volume create "$CACHE_VOLUME" >/dev/null
    # fugace runs as 65532 in a scratch image: hand the volume over to it.
    d run --rm -v "$CACHE_VOLUME:/v" "$HELPER_IMAGE" chown 65532:65532 /v >/dev/null 2>&1 ||
      warn "could not set the owner of the cache volume; the certificate cache may not be writable"
    mounts+=(-v "$CACHE_VOLUME:$CACHE_PATH")
  fi
  [ -n "$C_CERTS" ] && mounts+=(-v "$C_CERTS:$CERTS_PATH:ro")

  for e in "$P_HTTP" "$P_HTTPS" "$P_IRC" "$P_IRCS"; do
    [ "$e" != none ] && port_busy "$e" && die "port $e is already in use on this machine"
  done

  info "Starting $NAME ($IMAGE)"
  d run -d --name "$NAME" --restart unless-stopped \
    --read-only --cap-drop ALL --security-opt no-new-privileges:true --user 65532:65532 \
    --memory 512m --memory-swap 512m --pids-limit 128 \
    --ulimit core=0 --ulimit nofile=4096:4096 --log-driver none \
    "${ports[@]}" "${mounts[@]}" "${envs[@]}" "$IMAGE" >/dev/null

  # Wait for the built-in healthcheck; explain a configuration error if any.
  local i=0 state health code
  while [ $i -lt $HEALTH_TIMEOUT ]; do
    state=$(d container inspect -f '{{.State.Status}}' "$NAME")
    health=$(d container inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$NAME")
    [ "$health" = healthy ] && { ok "fugace is up and healthy"; return; }
    code=$(d container inspect -f '{{.State.ExitCode}}' "$NAME")
    # With --restart, a container that fails at startup loops in "restarting".
    if [ "$state" = exited ] || [ "$state" = dead ] || { [ "$state" = restarting ] && [ "$code" != 0 ]; }; then
      if [ "$code" = 2 ]; then
        # The container has no logs (log driver none): replay the startup
        # check once to show the configuration error. It exits right away.
        d rm -f "$NAME" >/dev/null 2>&1 || true
        warn "fugace refused the configuration:"
        d run --rm "${envs[@]}" "$IMAGE" 2>&1 | sed 's/^/    /' >&2 || true
      fi
      d rm -f "$NAME" >/dev/null 2>&1 || true
      die "fugace stopped during startup (exit code $code)"
    fi
    sleep 1
    i=$((i + 1))
  done
  die "fugace did not become healthy within ${HEALTH_TIMEOUT}s (docker exec $NAME /fugace status)"
}

summary() {
  local host=${C_HOST%%,*} ip
  ip=$(host_ip)
  [ -z "$host" ] && host=$ip
  local web="https://$host"
  [ "$C_TLS" = off ] && web="http://$host"
  if [ "$C_TLS" = off ] && [ "$P_HTTP" != 80 ]; then web="$web:$P_HTTP"; fi
  if [ "$C_TLS" != off ] && [ "$P_HTTPS" != 443 ]; then web="$web:$P_HTTPS"; fi

  section "fugace is running"
  say "  Web client   ${B}$web${RST}"
  [ "$P_IRCS" != none ] && say "  IRC (TLS)    $host:$P_IRCS"
  [ "$P_IRC" != none ] && say "  IRC (plain)  $host:$P_IRC"
  if [ -z "$C_HOST" ]; then
    say "  ${DIM}Local mode: self-signed certificate, accept the browser warning once.${RST}"
  else
    say "  ${DIM}Certificate: Let's Encrypt for domains (ports 80 and 443 must be reachable from the Internet).${RST}"
  fi
  [ -n "$C_ADMIN" ] && say "  ${DIM}Admin: web Options > Mot de passe admin, or /oper admin <password> on IRC.${RST}"
  say ""
  say "  Status       docker exec $NAME /fugace status"
  say "  Reconfigure  run this installer again"
  say "  ${DIM}Nothing is logged and nothing is written to disk${C_CACHE:+ (except the certificate cache)}.${RST}"
}

# -------------------------------------------------------------- actions --
do_install() {
  resolve_image
  pull_image
  if installed; then
    warn "fugace is already installed: its settings are offered as defaults, and it will be replaced."
    warn "Replacing it erases the channel history and shared files (they only live in RAM)."
  fi
  configure
  if installed; then
    confirm "Replace the running fugace now?" y || die "cancelled"
    remove_container
  fi
  run_container
  summary
}

do_uninstall() { # $1 = clean (remove volume and image without asking)
  local clean=${1:-}
  if installed; then
    remove_container
  else
    info "no fugace container found"
  fi
  if d volume inspect "$CACHE_VOLUME" >/dev/null 2>&1; then
    if [ "$clean" = clean ] || confirm "Remove the certificate cache volume ($CACHE_VOLUME)?" y; then
      d volume rm "$CACHE_VOLUME" >/dev/null && ok "certificate cache removed"
    fi
  fi
  local imgs
  imgs=$(d image ls --format '{{.Repository}}:{{.Tag}}' "$IMAGE_REPO" 2>/dev/null | grep -v '<none>' || true)
  if [ -n "$imgs" ]; then
    if [ "$clean" = clean ] || confirm "Remove the fugace image(s)?" y; then
      # shellcheck disable=SC2086 # one image reference per word
      if d rmi $imgs >/dev/null 2>&1; then ok "image removed"; else warn "could not remove some images (in use?)"; fi
    fi
  fi
}

do_reinstall() {
  local saved=""
  if installed; then
    saved=$(current_env)
    say "  ${DIM}Current settings are kept as defaults for the new installation.${RST}"
  fi
  confirm "Remove fugace completely (container, certificate cache, image) and install it again?" y || die "cancelled"
  # Keep the old answers as defaults once the container is gone (variables
  # already set in the environment win).
  local k v
  while IFS='=' read -r k v; do
    if [ -n "$k" ] && [ -z "${!k+x}" ]; then export "$k=$v"; fi
  done <<<"$saved"
  do_uninstall clean
  do_install
}

menu() {
  section "fugace installer"
  if installed; then
    say "  fugace is ${GRN}installed${RST} ($(d container inspect -f '{{.Config.Image}}' "$NAME"))."
  else
    say "  fugace is not installed."
  fi
  say ""
  say "  1) Install (or reconfigure)"
  say "  2) Reinstall from scratch (clean)"
  say "  3) Uninstall"
  say "  q) Quit"
  local choice
  printf 'Choice: ' >&2
  IFS= read -r choice <&3 || choice=q
  case "$choice" in
    1) ACTION=install ;;
    2) ACTION=reinstall ;;
    3) ACTION=uninstall ;;
    *) exit 0 ;;
  esac
}

main() {
  need_docker
  [ -z "$ACTION" ] && { [ "$ASSUME_YES" = 1 ] && die "--yes needs an action (--install, --reinstall or --uninstall)"; menu; }
  case "$ACTION" in
    install) do_install ;;
    reinstall) do_reinstall ;;
    uninstall) do_uninstall; ok "fugace is uninstalled" ;;
  esac
}

main
