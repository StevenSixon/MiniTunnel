#!/usr/bin/env bash
#
# Installs the MiniTunnel relay as a systemd service on a Linux host — an
# internal company server (scenario B) or a public VPS. Re-running upgrades.
#
# Copy these into one directory on the server first, then run as root:
#   - the relay binary   (e.g. relay-linux-amd64, built with GOOS=linux)
#   - cert.pem, key.pem  (from gencert)
#
# Usage:
#   sudo MINITUNNEL_PSK=... ./install-relay.sh
#
# Optional env:
#   PORT=7000                     listen port (default 7000)
#   BIND=                         bind address; empty = all interfaces.
#                                 For an internal-only relay, set BIND to the
#                                 server's internal IP so it never listens on a
#                                 public interface, e.g. BIND=10.0.0.5
#   BINARY=./relay-linux-amd64    relay binary to install
#   CERT=./cert.pem  KEY=./key.pem
#
set -euo pipefail

PSK="${MINITUNNEL_PSK:-}"
PORT="${PORT:-7000}"
BIND="${BIND:-}"
BINARY="${BINARY:-./relay-linux-amd64}"
SRC_CERT="${CERT:-./cert.pem}"
SRC_KEY="${KEY:-./key.pem}"

fail() { echo "error: $*" >&2; exit 1; }

[ "$(id -u)" = "0" ] || fail "run as root (sudo)"
[ -n "$PSK" ]        || fail "MINITUNNEL_PSK is required"
[ -f "$BINARY" ]     || fail "relay binary not found: $BINARY (set BINARY=...)"
[ -f "$SRC_CERT" ]   || fail "certificate not found: $SRC_CERT"
[ -f "$SRC_KEY" ]    || fail "key not found: $SRC_KEY"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE="$SCRIPT_DIR/minitunnel-relay.service"
[ -f "$TEMPLATE" ] || fail "unit template missing: $TEMPLATE"

ADDR="${BIND}:${PORT}"
BIN_DST="/usr/local/bin/minitunnel-relay"
CFG_DIR="/etc/minitunnel"
UNIT_DST="/etc/systemd/system/minitunnel-relay.service"

echo ">> creating service user 'minitunnel'"
id minitunnel >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin minitunnel

echo ">> installing binary -> $BIN_DST"
install -m 0755 "$BINARY" "$BIN_DST"

echo ">> installing certificate, key and env -> $CFG_DIR"
install -d -m 0750 -o minitunnel -g minitunnel "$CFG_DIR"
install -m 0444 -o minitunnel -g minitunnel "$SRC_CERT" "$CFG_DIR/cert.pem"
install -m 0400 -o minitunnel -g minitunnel "$SRC_KEY"  "$CFG_DIR/key.pem"
# The PSK lives in an env file readable only by the service user.
umask 077
printf 'MINITUNNEL_PSK=%s\n' "$PSK" > "$CFG_DIR/relay.env"
chown minitunnel:minitunnel "$CFG_DIR/relay.env"
chmod 0400 "$CFG_DIR/relay.env"

echo ">> writing systemd unit -> $UNIT_DST (listen $ADDR)"
sed -e "s|__ADDR__|$ADDR|g" "$TEMPLATE" > "$UNIT_DST"
chmod 0644 "$UNIT_DST"

echo ">> enabling and starting service"
systemctl daemon-reload
systemctl enable --now minitunnel-relay

echo
echo "Done. Relay is running."
echo "  status:  systemctl status minitunnel-relay"
echo "  logs:    journalctl -u minitunnel-relay -f"
echo "  listen:  $ADDR"
echo
echo "Open port $PORT to the agent (office mini) and to your VPN client range."
echo "If this is an internal-only relay, do NOT expose $PORT on any public interface."
