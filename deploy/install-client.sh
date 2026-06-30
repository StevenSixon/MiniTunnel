#!/usr/bin/env bash
#
# Installs the MiniTunnel client on your machine (e.g. the home Mac) as a
# LaunchAgent, so the local forwards (ssh -p 2222 / vnc :5901) come up at login
# and are restarted if the client ever exits. The client only opens 127.0.0.1
# listeners, so this needs NO root.
#
# Config is read from the repo's .env at launch, so this is the single source of
# truth — edit .env and reload (see bottom) to change relay/agent/forwards.
#
# Usage (from the repo root, with a populated .env):
#   ./deploy/install-client.sh
#
# Re-running upgrades an existing install.
set -euo pipefail

fail() { echo "error: $*" >&2; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TEMPLATE="$SCRIPT_DIR/com.minitunnel.client.plist"
LABEL="com.minitunnel.client"
PLIST_DST="$HOME/Library/LaunchAgents/$LABEL.plist"
LOG="$HOME/Library/Logs/minitunnel-client.log"

[ -f "$TEMPLATE" ]          || fail "plist template missing: $TEMPLATE"
[ -f "$REPO_ROOT/.env" ]    || fail "no .env in $REPO_ROOT — create it first (see .env.example)"
command -v go >/dev/null    || fail "Go toolchain not found (needed to build the client)"

echo ">> building client binary"
( cd "$REPO_ROOT" && go build -o bin/client ./cmd/client )

echo ">> writing LaunchAgent -> $PLIST_DST"
mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"
sed -e "s|__REPO__|$REPO_ROOT|g" \
    -e "s|__LOG__|$LOG|g" \
    "$TEMPLATE" > "$PLIST_DST"

echo ">> (re)loading agent"
launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST_DST"
launchctl enable "gui/$(id -u)/$LABEL"
launchctl kickstart -k "gui/$(id -u)/$LABEL" 2>/dev/null || true

cat <<EOF

Done. The client runs at login and is kept alive.

  logs:    tail -f $LOG
  status:  launchctl print gui/$(id -u)/$LABEL | head
  stop:    launchctl bootout gui/$(id -u)/$LABEL
  reload:  launchctl kickstart -k gui/$(id -u)/$LABEL   # after editing .env

Use it:
  ssh -p 2222 <user>@127.0.0.1
  open vnc://127.0.0.1:5901
EOF
