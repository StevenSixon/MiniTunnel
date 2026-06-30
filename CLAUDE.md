# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

MiniTunnel is a self-hosted reverse tunnel (pure Go, stdlib only) for reaching a
machine behind NAT with a changing IP — its origin use case is remoting into an
office Mac mini from home. It deliberately does **not** implement screen capture
or a terminal protocol: it builds only the encrypted transport and forwards to
the target's existing `sshd` (:22) and macOS Screen Sharing (`screensharingd`,
:5900). New transport features belong here; anything resembling a VNC/SSH
reimplementation does not.

## Commands

```sh
go build ./...                 # build all four programs
go vet ./...                   # vet (run before committing)
go run ./cmd/gencert           # generate cert.pem + key.pem (SAN is fixed, see below)

# cross-compile the relay for a Linux VPS / internal server
GOOS=linux GOARCH=amd64 go build -o bin/relay-linux-amd64 ./cmd/relay
```

There are **no Go unit tests** in this repo. Verification is done by running the
binaries end to end: start `relay`, point an `agent` at it with a local echo/SSH
service, start a `client` with `-L`, and send bytes through the forwarded local
port. The PSK is shared via the `MINITUNNEL_PSK` env var across all three.

Every flag has a `MINITUNNEL_*` env-var fallback via `proto.EnvOr(key, default)`,
which is used as the flag's default so precedence stays **flag > env > default**.
When adding a new flag, give it the same treatment and list it in `.env.example`.
PSK keeps its own `proto.ResolvePSK` (identical fallback, kept for the clearer
call site). Vars: `MINITUNNEL_PSK/CERT/KEY/ADDR/RELAY/ID/ALLOW/AGENT/FORWARD`.
`cert`/`key` accept a path **or** inline PEM — `proto.loadPEM` treats a value
containing `-----BEGIN` as literal PEM, else a file path — so the cert and key
can live entirely in env/`.env` with no `.pem` files on disk.

## Architecture

Four programs share one wire protocol in `internal/proto`. They version
together — a protocol change requires rebuilding and redeploying all of them.

- **`cmd/relay`** — runs on a host both other ends can reach (public VPS *or* an
  internal server reachable over VPN). The rendezvous point.
- **`cmd/agent`** — runs on the target machine (the mini). Only ever dials
  **out**, which is what makes NAT + dynamic IP a non-issue.
- **`cmd/client`** — runs where you are. Opens local listening ports that
  forward through the relay.
- **`cmd/gencert`** — one-shot self-signed cert generator.

### How a connection is established (the core flow)

This spans all three programs; read them together:

1. The agent dials the relay and registers a long-lived **control link**
   (`RoleAgentControl`), keyed by its `-id`.
2. The client accepts a local TCP connection (e.g. on :2222) and dials the relay
   with `RoleClientSession`, naming the target agent and remote port. The relay
   parks this connection under a freshly generated **session ID** and pushes a
   `ControlMsg{Type: MsgNotify}` down the agent's control link, then replies to
   the client with a `SessionAck`.
3. The agent receives the notify, dials the relay **back** with
   `RoleAgentSession` carrying that session ID, and also dials its own
   `127.0.0.1:<port>`.
4. The relay matches the agent's data connection to the parked client connection
   by session ID and pipes the two together. The agent pipes its relay
   connection to the local service. End to end the bytes flow:
   `client local accept ↔ relay ↔ agent ↔ 127.0.0.1:<service>`.

The relay therefore never needs an inbound route to the agent — both sides dial
out to the relay. Parking happens **before** the notify is sent so a fast agent
dial-back cannot miss the session.

### Wire protocol (`internal/proto/proto.go`)

- Every connection is **TLS**. The client/agent pin the relay's self-signed cert
  via `RootCAs` and verify against the fixed SAN `ServerName` ("minitunnel-relay"),
  so the relay's IP/hostname can change freely — never tie trust to the address.
- Every connection opens with one length-prefixed JSON `Hello` (`WriteMsg`/
  `ReadMsg` use a 4-byte length prefix specifically so a data connection can
  switch to raw byte piping afterward without over-reading). A **`SessionAck`**
  then precedes piping on client sessions so failures surface a clear reason
  instead of a silent hang.
- The agent control link carries `ControlMsg` (type `notify` or `ping`). Both
  ends send a `ping` every `PingInterval` (30s) and drop the link after
  `ControlReadTimeout` (90s) of silence; the agent's `run()` loop reconnects
  every 3s. `SetKeepAlive` adds OS-level TCP keepalive underneath.
- **`Pipe`** does a half-close (`CloseWrite`) per direction rather than closing
  both on first EOF — required so request/response streams (SSH, VNC) that shut
  down one direction early still receive the reply. Don't "simplify" this back
  to a both-sides close.
- The agent enforces a **port allowlist** (`-allow`, default `22,5900`); a client
  cannot make it reach arbitrary ports.

## Deployment

`deploy/` holds the production install scripts: `install-relay.sh` (systemd unit
for a Linux relay; `BIND=<internal-ip>` keeps it off public interfaces) and
`install-agent.sh` (macOS LaunchDaemon on the mini, also sets `pmset` to prevent
sleep). README.md has the full walkthrough including the internal-server (VPN)
deployment.

## Gotchas

- `.gitignore` artifact patterns (`/relay`, `/agent`, ...) are **root-anchored
  with a leading slash on purpose**. Bare names like `relay` would also match the
  `cmd/relay/` source directory and silently un-track the source — this already
  happened once. Don't remove the anchoring.
- `cert.pem` / `key.pem` / `*.psk` are gitignored secrets; never commit them.
