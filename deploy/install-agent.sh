#!/usr/bin/env bash
#
# Installs the MiniTunnel agent on the office Mac mini as a LaunchDaemon, so it
# starts at boot, survives reboots, and runs even when no one is logged in or
# the screen is locked. Re-running upgrades an existing install.
#
# Usage:
#   RELAY=your.vps.ip:7000 AGENT_ID=office-mini MINITUNNEL_PSK=... \
#     ./deploy/install-agent.sh
#
# Optional env:
#   CERT=path/to/cert.pem   (default: ./cert.pem)
#   ALLOW=22,5900           (default: 22,5900 — local ports clients may reach)
#
set -euo pipefail

RELAY="${RELAY:-}"
AGENT_ID="${AGENT_ID:-}"
PSK="${MINITUNNEL_PSK:-}"
ALLOW="${ALLOW:-22,5900}"
SRC_CERT="${CERT:-cert.pem}"

fail() { echo "error: $*" >&2; exit 1; }

[ -n "$RELAY" ]     || fail "RELAY is required (e.g. RELAY=1.2.3.4:7000)"
[ -n "$AGENT_ID" ]  || fail "AGENT_ID is required (e.g. AGENT_ID=office-mini)"
[ -n "$PSK" ]       || fail "MINITUNNEL_PSK is required"
[ -f "$SRC_CERT" ]  || fail "certificate not found: $SRC_CERT (run gencert first, copy cert.pem here)"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TEMPLATE="$SCRIPT_DIR/com.minitunnel.agent.plist"
[ -f "$TEMPLATE" ] || fail "plist template missing: $TEMPLATE"

BIN_DST="/usr/local/bin/minitunnel-agent"
CFG_DIR="/usr/local/etc/minitunnel"
CERT_DST="$CFG_DIR/cert.pem"
PLIST_DST="/Library/LaunchDaemons/com.minitunnel.agent.plist"
LABEL="com.minitunnel.agent"

echo ">> building agent binary"
( cd "$REPO_ROOT" && go build -o "$SCRIPT_DIR/.minitunnel-agent.build" ./cmd/agent )

echo ">> installing binary -> $BIN_DST"
# /usr/local/bin doesn't exist by default on Apple Silicon (Homebrew lives in
# /opt/homebrew), and install(1) won't create the destination dir — make it.
sudo mkdir -p "$(dirname "$BIN_DST")"
sudo install -m 0755 "$SCRIPT_DIR/.minitunnel-agent.build" "$BIN_DST"
rm -f "$SCRIPT_DIR/.minitunnel-agent.build"

echo ">> installing certificate -> $CERT_DST"
sudo mkdir -p "$CFG_DIR"
sudo install -m 0644 "$SRC_CERT" "$CERT_DST"

echo ">> writing LaunchDaemon -> $PLIST_DST"
TMP_PLIST="$(mktemp)"
sed -e "s|__BIN__|$BIN_DST|g" \
    -e "s|__RELAY__|$RELAY|g" \
    -e "s|__AGENT_ID__|$AGENT_ID|g" \
    -e "s|__CERT__|$CERT_DST|g" \
    -e "s|__ALLOW__|$ALLOW|g" \
    -e "s|__PSK__|$PSK|g" \
    "$TEMPLATE" > "$TMP_PLIST"
# 0600 root:wheel keeps the embedded pre-shared key out of other users' reach.
sudo install -m 0600 -o root -g wheel "$TMP_PLIST" "$PLIST_DST"
rm -f "$TMP_PLIST"

echo ">> (re)loading daemon"
sudo launchctl bootout system/"$LABEL" 2>/dev/null || true
sudo launchctl bootstrap system "$PLIST_DST"
sudo launchctl enable system/"$LABEL"

echo ">> preventing sleep (so the tunnel stays up)"
sudo pmset -c sleep 0 disablesleep 1 displaysleep 0 || echo "   (pmset failed; set Energy settings manually)"

cat <<EOF

Done. The agent is running as a LaunchDaemon and will start on every boot.

  logs:    /var/log/minitunnel-agent.log
  status:  sudo launchctl print system/$LABEL | head
  stop:    sudo launchctl bootout system/$LABEL
  remove:  sudo launchctl bootout system/$LABEL; sudo rm $PLIST_DST $BIN_DST

Make sure Remote Login (SSH) and Screen Sharing are enabled in
System Settings -> General -> Sharing.
EOF
