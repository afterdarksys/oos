#!/usr/bin/env bash
# oos fleet deploy. Builds linux/amd64, ships the binary, seeds a server config
# if none exists (never overwrites one), installs the system timer, and runs a
# quick check. Dry-run unless --yes: the dry-run still builds and probes each
# host read-only (kernel, free space, installed version, config, timer) so a
# host that will not take the deploy is found before anything is shipped.
# No --force exists on purpose.
#
#   deploy/deploy.sh [--yes] [--config deploy/oos.server.json] [--user root] host [host ...]
set -euo pipefail

YES=0
CONFIG="$(dirname "$0")/oos.server.json"
USER_="root"
HOSTS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --yes) YES=1 ;;
    --config) CONFIG="$2"; shift ;;
    --user) USER_="$2"; shift ;;
    --force|--break-glass) echo "deploy.sh: $1 is not a thing here" >&2; exit 2 ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    -*) echo "deploy.sh: unknown flag $1" >&2; exit 2 ;;
    *) HOSTS+=("$1") ;;
  esac
  shift
done
[ ${#HOSTS[@]} -gt 0 ] || { echo "deploy.sh: no hosts given" >&2; exit 2; }
[ -f "$CONFIG" ] || { echo "deploy.sh: config $CONFIG not found" >&2; exit 2; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="$(sed -n 's/^const version = "\(.*\)"/\1/p' "$ROOT/main.go")"
BIN="$ROOT/dist/oos-linux-amd64"
mkdir -p "$ROOT/dist"
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=15)

echo "oos $VERSION -> ${HOSTS[*]} (user $USER_, config $CONFIG)"
echo "building $BIN"
( cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$BIN" . )

# probe: read-only look at each host. Runs in both modes; a host that cannot
# be reached stops the run before anything is shipped anywhere.
for h in "${HOSTS[@]}"; do
  echo "== $h (probe)"
  if ! "${SSH[@]}" "$USER_@$h" bash -s <<'PROBE'
printf '  %s, %s\n' "$(uname -srm)" "$(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME")"
printf '  root fs: %s\n' "$(df -h / | awk 'NR==2{print $4" free of "$2" ("$5" used)"}')"
if command -v oos >/dev/null 2>&1; then printf '  oos: %s at %s\n' "$(oos -V)" "$(command -v oos)"; else echo "  oos: not installed"; fi
if [ -f "$HOME/.config/oos/oos.json" ]; then echo "  config: present ($HOME/.config/oos/oos.json)"; else echo "  config: none, would be seeded"; fi
if command -v systemctl >/dev/null 2>&1; then
  printf '  oos.timer: %s\n' "$(systemctl is-enabled oos.timer 2>/dev/null || echo 'not installed')"
else
  echo "  systemctl: absent, agent would not be installed"
fi
for c in docker logger notify-send; do command -v "$c" >/dev/null 2>&1 && echo "  has $c"; done
true
PROBE
  then
    echo "deploy.sh: cannot probe $h; nothing shipped" >&2
    exit 1
  fi
done

if [ "$YES" -ne 1 ]; then
  echo "dry-run. Per host: scp binary to /usr/local/bin/oos.new, mv into place,"
  echo "  seed ~/.config/oos/oos.json only if absent (else write oos.json.new beside it),"
  echo "  oos --install-agent --system, oos -Q --critical 5, oos -F."
  echo "add --yes to do it."
  exit 0
fi

for h in "${HOSTS[@]}"; do
  echo "== $h"
  T="$USER_@$h"
  scp -q "$BIN" "$T:/usr/local/bin/oos.new"
  scp -q "$CONFIG" "$T:/tmp/oos.server.json"
  "${SSH[@]}" "$T" bash -s <<'REMOTE'
set -euo pipefail
chmod 0755 /usr/local/bin/oos.new
mv /usr/local/bin/oos.new /usr/local/bin/oos
mkdir -p "$HOME/.config/oos" "$HOME/.local/state/oos"
if [ -f "$HOME/.config/oos/oos.json" ]; then
  if ! cmp -s /tmp/oos.server.json "$HOME/.config/oos/oos.json"; then
    cp /tmp/oos.server.json "$HOME/.config/oos/oos.json.new"
    echo "config exists and differs; shipped copy left at ~/.config/oos/oos.json.new"
  fi
else
  cp /tmp/oos.server.json "$HOME/.config/oos/oos.json"
  echo "config seeded"
fi
rm -f /tmp/oos.server.json
oos -V
oos -s >/dev/null || { echo "config does not validate on this host"; exit 1; }
if command -v systemctl >/dev/null 2>&1; then
  oos --install-agent --system
  systemctl list-timers oos.timer --no-pager --no-legend | sed 's/^/  timer: /'
else
  echo "no systemctl; agent not installed"
fi
# status lines must not abort the deploy: exit 1/2 here mean warn/critical, not failure
if oos -Q --critical 5; then echo "free space ok"; else echo "free space under 5 GB (exit $?)"; fi
FREE="$(oos -F || true)"
echo "free: ${FREE} GB"
REMOTE
done
