#!/usr/bin/env bash
#
# One-shot: configure + register the MiniTunnel agent on this Mac mini per
# docs/agent-setup-runbook.md. Idempotent — safe to re-run.
#
# It reads all config from the repo's .env (MINITUNNEL_PSK / _RELAY / _ID /
# _ALLOW / _CERT), enables SSH + Screen Sharing, (re)installs the LaunchDaemon,
# then tails the log so you can see the "registered with relay" line.
#
# Usage (run from anywhere; it cd's to the repo):
#   ./deploy/register-agent.sh
# You will be prompted for your macOS password once (sudo).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

fail() { echo "error: $*" >&2; exit 1; }

# --- load config from .env ---------------------------------------------------
[ -f .env ] || fail ".env not found in $REPO_ROOT (configure it first)"
set -a; . ./.env; set +a

RELAY="${MINITUNNEL_RELAY:-}"
AGENT_ID="${MINITUNNEL_ID:-}"
ALLOW="${MINITUNNEL_ALLOW:-22,5900}"
PSK="${MINITUNNEL_PSK:-}"
CLIP="${MINITUNNEL_CLIP:-}"

[ -n "$RELAY" ]    || fail "MINITUNNEL_RELAY missing in .env"
[ -n "$AGENT_ID" ] || fail "MINITUNNEL_ID missing in .env"
[ -n "$PSK" ]      || fail "MINITUNNEL_PSK missing in .env"

echo ">> config: relay=$RELAY  id=$AGENT_ID  allow=$ALLOW  psk=<set,len=${#PSK}>"

# --- step 1: prerequisites ---------------------------------------------------
# For a ws(s):// relay, derive the healthz URL from the RELAY value itself:
# wss://host/prefix/tunnel -> https://host/prefix/healthz (never hardcode a
# domain here — the tunnel path always ends in /tunnel, see MINITUNNEL_RELAY).
if [ "${RELAY#wss://}" != "$RELAY" ] || [ "${RELAY#ws://}" != "$RELAY" ]; then
  echo ">> checking relay health"
  HEALTH_URL="$(printf '%s' "$RELAY" | sed -e 's|^wss://|https://|' -e 's|^ws://|http://|' -e 's|/tunnel$|/healthz|')"
  curl -sS --max-time 15 "$HEALTH_URL" | grep -qx ok \
    && echo "   relay healthz: ok" \
    || echo "   warn: $HEALTH_URL did not return 'ok' (continuing; check network)"
fi

command -v go >/dev/null 2>&1 || fail "Go not installed (brew install go)"
echo ">> go: $(go version)"

# --- step 4: ensure pinned cert exists (matches .env inline PEM) --------------
if [ ! -f cert.pem ]; then
  if [ -n "${MINITUNNEL_CERT:-}" ]; then
    printf '%s\n' "$MINITUNNEL_CERT" > cert.pem
    echo ">> wrote cert.pem from MINITUNNEL_CERT"
  else
    fail "cert.pem missing and MINITUNNEL_CERT not set"
  fi
fi
openssl x509 -in cert.pem -noout -subject | grep -q "minitunnel-relay" \
  || fail "cert.pem is not the minitunnel-relay cert"
echo ">> cert.pem OK ($(openssl x509 -in cert.pem -noout -subject))"

# --- build sanity ------------------------------------------------------------
echo ">> building"
go build ./... && echo "   build OK"

# --- step 3: enable macOS services (SSH + Screen Sharing) --------------------
echo ">> enabling Remote Login (SSH, 22)"
sudo systemsetup -setremotelogin on
sudo systemsetup -getremotelogin || true

echo ">> enabling Screen Sharing (5900)"
sudo launchctl enable system/com.apple.screensharing || true
sudo launchctl bootstrap system /System/Library/LaunchDaemons/com.apple.screensharing.plist 2>/dev/null || true
sleep 1
if lsof -nP -iTCP:5900 -sTCP:LISTEN 2>/dev/null | grep -q 5900; then
  echo "   screen sharing: LISTENING"
else
  echo "   screen sharing: NOT listening — may need System Settings > General > Sharing > Screen Sharing"
fi

# --- step 5: install / re-install the LaunchDaemon ---------------------------
echo ">> (re)installing agent daemon"
RELAY="$RELAY" AGENT_ID="$AGENT_ID" ALLOW="$ALLOW" MINITUNNEL_PSK="$PSK" \
  MINITUNNEL_CLIP="$CLIP" ./deploy/install-agent.sh

# --- step 6: verify ----------------------------------------------------------
echo ">> verifying"
sleep 3
sudo launchctl print system/com.minitunnel.agent 2>/dev/null | grep -E "state =|program =" | head || true
echo "---- last 25 log lines (/var/log/minitunnel-agent.log) ----"
tail -n 25 /var/log/minitunnel-agent.log 2>/dev/null || echo "(no log yet)"

echo
if tail -n 60 /var/log/minitunnel-agent.log 2>/dev/null | grep -q "registered with relay"; then
  echo "✅ SUCCESS: agent registered with relay as \"$AGENT_ID\"."
else
  echo "⏳ Not yet confirmed. Watch the log:  tail -f /var/log/minitunnel-agent.log"
  echo "   (look for: registered with relay as \"$AGENT_ID\")"
  echo "   If you see repeated 'control link lost ... retrying', see runbook section 7."
fi
