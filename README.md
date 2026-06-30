# MiniTunnel

A tiny, self-hosted reverse tunnel for reaching a machine behind NAT with a
changing IP — built to remote into an office Mac mini from home without paying
for, or trusting, third-party remote-desktop services.

It does **not** reinvent SSH or VNC. It builds the *transport*: a TLS tunnel
through a relay you control, then forwards your terminal (SSH) and remote
desktop (macOS Screen Sharing) over it. Because the office machine only ever
dials **out** to the relay, NAT and a rotating office IP are non-issues, and you
get locked-screen login + adaptive image quality for free from macOS's own
`sshd` and `screensharingd`.

```
        relay host — VPS or internal server (you run it)
               ┌────────────────────────┐
               │         relay          │
               └───────────┬────────────┘
       control + sessions  │  sessions
        ┌──────────────────┴──────────────────┐
   ┌────┴─────┐                          ┌─────┴─────┐
   │  agent   │  office Mac mini         │  client   │  home Mac Pro
   │ dials out│                          │ local fwd │
   └────┬─────┘                          └─────┬─────┘
   127.0.0.1:22  (sshd)            ssh -p 2222 you@127.0.0.1
   127.0.0.1:5900 (Screen Sharing) open vnc://127.0.0.1:5901
```

## Components

| Binary    | Runs on            | Role                                                        |
|-----------|--------------------|-------------------------------------------------------------|
| `relay`   | VPS *or* internal server | Rendezvous: matches client requests to a registered agent. |
| `agent`   | office Mac mini    | Dials out, stays connected, bridges sessions to local ports.|
| `client`  | home Mac Pro       | Opens local ports that forward through the relay.           |
| `gencert` | once, anywhere     | Generates the relay's pinned TLS certificate.               |

## Security model

- All links are **TLS**. Clients pin the relay's self-signed certificate (SAN
  `minitunnel-relay`), so the relay's public IP can change without breaking
  trust, and there is no CA to manage.
- A **pre-shared key** authenticates every connection (constant-time compare).
  Set it via `-psk` or the `MINITUNNEL_PSK` env var. Use a long random value.
- The agent enforces a **port allowlist** (`-allow`, default `22,5900`), so a
  client can never ask it to reach arbitrary ports/hosts.

## Reliability

- **Heartbeat.** The agent's control link exchanges a Ping every 30s in both
  directions and drops the link if nothing arrives within 90s, on top of TCP
  keepalive. This detects connections silently severed by a NAT/firewall idle
  timeout instead of waiting on a stuck socket.
- **Auto-reconnect.** The agent re-dials the relay every 3s after any drop, so a
  relay restart, Wi-Fi blip, or office IP change recovers on its own.
- **Clear client errors.** The relay acknowledges each client request before
  piping, so the client logs a specific reason — `agent "X" is not online`,
  `relay unreachable`, or `no response (check PSK/certificate)` — instead of a
  silent hang. The client also retries the relay dial briefly and probes the
  relay once at startup.

## Build

```sh
go build -o bin/relay   ./cmd/relay
go build -o bin/agent   ./cmd/agent
go build -o bin/client  ./cmd/client
go build -o bin/gencert ./cmd/gencert
```

The Mac binaries cross-compile from anywhere; build the relay for your VPS, e.g.
Linux x86-64:

```sh
GOOS=linux GOARCH=amd64 go build -o bin/relay-linux ./cmd/relay
```

## Setup

> **Configuration.** Every flag below also reads from a `MINITUNNEL_*`
> environment variable, so you can drive all three programs entirely from the
> environment (handy for systemd `EnvironmentFile=`, the macOS LaunchDaemon, or a
> local `.env`). Precedence is **flag > env var > default**. See
> [`.env.example`](.env.example) for the full list: `MINITUNNEL_PSK`,
> `MINITUNNEL_CERT`, `MINITUNNEL_KEY`, `MINITUNNEL_ADDR`, `MINITUNNEL_RELAY`,
> `MINITUNNEL_ID`, `MINITUNNEL_ALLOW`, `MINITUNNEL_AGENT`, `MINITUNNEL_FORWARD`.
> A quick local run: `cp .env.example .env`, edit it, then
> `set -a; source .env; set +a` and start each binary with no flags.
>
> **No `.pem` files needed.** `MINITUNNEL_CERT`/`MINITUNNEL_KEY` (and `-cert`/
> `-key`) accept either a file path *or* inline PEM — paste the certificate/key
> straight into `.env` (multi-line, double-quoted) and there is nothing on disk
> to manage or copy around.

### 0. Generate the certificate and a key (once)

```sh
go run ./cmd/gencert            # writes cert.pem + key.pem
export MINITUNNEL_PSK="$(openssl rand -hex 24)"   # share this secret across all three
```

- `key.pem` stays **only** on the relay.
- `cert.pem` is copied to the agent and the client.
- The same `MINITUNNEL_PSK` is set on all three.

### 1. Relay

The relay can run anywhere that **both** the agent and you (the client) can
reach it — it does not have to be public. Two common placements:

- **Public VPS** — when home and office share no network.
- **Internal company server (no VPS)** — when you reach the office over a VPN
  but the VPN does *not* route to the mini's subnet. Put the relay on an
  internal host that the VPN *can* reach and that the mini can also reach, give
  it an internal DNS name, and point everything at that name. No traffic leaves
  the corporate network. The pinned certificate is keyed to the fixed SAN
  `minitunnel-relay`, so the relay's internal IP/hostname can be anything and
  can change without breaking trust.

Quick run (foreground):

```sh
MINITUNNEL_PSK=... ./relay -addr :7000 -cert cert.pem -key key.pem
```

As a managed service on a Linux host (VPS or internal server) — copy the
`relay-linux-*` binary, `cert.pem`, `key.pem` and `deploy/` to the server, then:

```sh
# internal-only relay: bind to the server's internal IP so it never listens publicly
sudo MINITUNNEL_PSK=... BIND=10.0.0.5 PORT=7000 BINARY=./relay-linux-amd64 \
  ./deploy/install-relay.sh
```

Open the port to the agent (office mini) and to your VPN client range. Build the
Linux relay binary with `GOOS=linux GOARCH=amd64 go build -o relay-linux-amd64 ./cmd/relay`
(use `arm64` for ARM servers).

#### Behind a cloud domain gateway (changing IP)

On a PaaS that only exposes a **domain** (the IP changes on restart), point the
agent/client at the domain — trust is pinned to the SAN `minitunnel-relay`, not
the address, so a moving IP is a non-issue. Pick the option that matches what the
gateway can do.

**A. Raw TCP stream (L4) — preferred, zero code on the wire path.** If the
platform can route a raw TCP stream to the relay, the protocol flows unchanged:

- **Plain TCP route / port mapping** — ask for an L4 TCP route to the relay's
  listen port. Set `MINITUNNEL_RELAY=your.domain:<port>`; nothing else changes.
- **SNI-based stream route sharing :443** — when the L4 route selects the
  upstream by TLS SNI (e.g. an APISIX `stream route`), set
  `MINITUNNEL_SNI=your.domain` on the agent and client. The SNI is sent for
  routing while the cert stays pinned to `minitunnel-relay`.

**B. Only an L7 HTTP(S) gateway — tunnel over WebSocket.** Many PaaS gateways
(e.g. APISIX) terminate TLS with their own certificate and only speak HTTP, so a
raw TLS stream cannot pass. Run the relay's HTTP listener and connect over a
WebSocket instead:

1. On the relay set `MINITUNNEL_HTTP_ADDR=:8080` and point the gateway's HTTP
   route at it (the same route that serves `/healthz` and the dashboard). Enable
   WebSocket on that route (on APISIX, `enable_websocket: true`).
2. On the agent and client set the relay to the WebSocket URL:
   `MINITUNNEL_RELAY=wss://your.domain<prefix>/tunnel` (e.g.
   `wss://tunnel.example.com/api/tunnel` when `MINITUNNEL_HTTP_PREFIX=/api`). Use
   `ws://` only for a plain-HTTP gateway.

The pinned relay TLS runs **inside** the WebSocket, so the certificate is still
pinned end to end and the gateway sees only ciphertext — it never learns the PSK
or the traffic. This is the only mode that adds a dependency
(`github.com/coder/websocket`); the L4 options above use stdlib alone.

### 2. Agent (office Mac mini)

First enable the macOS services it will bridge to (System Settings → General →
Sharing): **Remote Login** (SSH, port 22) and **Screen Sharing** (port 5900).
Then keep the machine awake so the tunnel survives:

```sh
sudo pmset -c sleep 0 disablesleep 1 displaysleep 0
```

Recommended — install as a LaunchDaemon so it runs at boot, even when logged
out or the screen is locked (the script also applies the `pmset` settings above):

```sh
RELAY=relay.host:7000 AGENT_ID=office-mini MINITUNNEL_PSK=... ./deploy/install-agent.sh
```

Or run it in the foreground for a quick test (it reconnects forever on its own):

```sh
MINITUNNEL_PSK=... ./agent -relay relay.host:7000 -cert cert.pem -id office-mini
```

### 3. Client (home Mac Pro)

```sh
MINITUNNEL_PSK=... ./client -relay relay.host:7000 -cert cert.pem -agent office-mini
```

Defaults forward `2222 -> 22` and `5901 -> 5900`. Then:

```sh
ssh -p 2222 you@127.0.0.1          # terminal
open vnc://127.0.0.1:5901          # Screen Sharing (works at the lock screen)
```

Add more forwards with repeated `-L localPort:remotePort` (each remote port must
be in the agent's `-allow` list).

## Managing the Mac mini agent

`deploy/install-agent.sh` (used above) installs the agent as a LaunchDaemon, so
it starts at boot, runs even when logged out / locked, and is restarted if it
exits. Manage it with:

```sh
sudo launchctl print system/com.minitunnel.agent | head   # status
tail -f /var/log/minitunnel-agent.log                      # logs
sudo launchctl bootout system/com.minitunnel.agent         # stop / uninstall
```

## Caveats

- You need a **relay host reachable by both ends** — a public VPS when home and
  office share no network, or an internal server when you reach the office over
  a VPN that does not route to the mini's subnet. Either way it's a host you
  control, not a SaaS. This is networking reality for NAT + a changing IP, not
  an optional dependency.
- All traffic currently flows through the relay, so its latency and bandwidth
  set the ceiling; pick a host close to the office. Direct P2P via UDP hole
  punching is a planned optimization.
- If FileVault is on and the mini reboots, it stops at the pre-boot unlock
  screen, which is below this tunnel — someone must unlock it physically.
