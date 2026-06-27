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
                 public VPS (you own it)
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
| `relay`   | public VPS         | Rendezvous: matches client requests to a registered agent.  |
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

### 2. Agent (office Mac mini)

First enable the macOS services it will bridge to (System Settings → General →
Sharing): **Remote Login** (SSH, port 22) and **Screen Sharing** (port 5900).
Then keep the machine awake so the tunnel survives:

```sh
sudo pmset -c sleep 0 disablesleep 1 displaysleep 0
```

Run the agent (it reconnects forever on its own):

```sh
MINITUNNEL_PSK=... ./agent -relay your.vps.ip:7000 -cert cert.pem -id office-mini
```

To keep it running across reboots, install it as a LaunchAgent/Daemon (see
`Auto-start` below).

### 3. Client (home Mac Pro)

```sh
MINITUNNEL_PSK=... ./client -relay your.vps.ip:7000 -cert cert.pem -agent office-mini
```

Defaults forward `2222 -> 22` and `5901 -> 5900`. Then:

```sh
ssh -p 2222 you@127.0.0.1          # terminal
open vnc://127.0.0.1:5901          # Screen Sharing (works at the lock screen)
```

Add more forwards with repeated `-L localPort:remotePort` (each remote port must
be in the agent's `-allow` list).

## Auto-start on the Mac mini (recommended)

Create `~/Library/LaunchAgents/com.minitunnel.agent.plist` pointing at the agent
binary with its flags and `MINITUNNEL_PSK` in `EnvironmentVariables`, set
`RunAtLoad` and `KeepAlive` to `true`, then `launchctl load` it. This survives
reboots and restarts the agent if it ever exits.

## Caveats

- You need a VPS with a public IP. That's networking reality for NAT + dynamic
  IP, not an optional dependency — but it's a host you control, not a SaaS.
- This MVP relays all traffic through the VPS. Latency and the VPS's bandwidth
  set the ceiling; a region close to the office helps. Direct P2P via UDP hole
  punching is a planned optimization.
- If FileVault is on and the mini reboots, it stops at the pre-boot unlock
  screen, which is below this tunnel — someone must unlock it physically.
