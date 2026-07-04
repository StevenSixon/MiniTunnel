# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

MiniTunnel is a self-hosted reverse tunnel (pure Go, stdlib only with a single
exception — see below) for reaching a machine behind NAT with a changing IP —
its origin use case is remoting into an office Mac mini from home.

> **stdlib-only exception:** `github.com/coder/websocket` is the sole third-party
> dependency. It backs the optional WebSocket transport (`internal/proto/ws.go`),
> used only when the relay is reachable solely through an L7 HTTP gateway that
> terminates TLS and speaks HTTP, not raw TLS. A correct WebSocket framing
> implementation that survives an uncontrolled intermediary is not worth
> hand-rolling. Do not add further dependencies without the same bar. It deliberately does **not** implement screen capture
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
call site). Vars: `MINITUNNEL_PSK/CERT/KEY/ADDR/RELAY/ID/ALLOW/AGENT/FORWARD/SNI/CLIP`.
`cert`/`key` accept three forms via `proto.loadPEM`: a value containing
`-----BEGIN` is literal PEM; an otherwise-valid base64 string that decodes to PEM
is single-line base64 (for cloud env-var UIs that mangle newlines); else a file
path. So the cert and key can live entirely in env/`.env` with no `.pem` files.

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
- **Transport (`DialRelay` / `AcceptWS` in `internal/proto/ws.go`).**
  `MINITUNNEL_RELAY` is either a `host:port` (direct TLS dial, optionally through
  an L4 gateway; `MINITUNNEL_SNI` sets the routing SNI while the cert stays
  pinned) **or** a `ws://` / `wss://` URL. The URL form tunnels through an L7 HTTP
  gateway as a WebSocket and runs the pinned TLS as an **inner** layer inside it,
  so trust is unchanged and the gateway sees only ciphertext. The relay serves
  the WebSocket at `<MINITUNNEL_HTTP_PREFIX>/tunnel` on its HTTP listener
  (`MINITUNNEL_HTTP_ADDR`) — the same listener as the dashboard, so it rides a
  gateway route that already reaches the relay. The raw TLS listener
  (`MINITUNNEL_ADDR`) stays up regardless; both entry points feed `relay.handle`.
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
- **Clipboard sync** (`internal/clip`, enabled by `-clip <port>` / `MINITUNNEL_CLIP`
  on both agent and client) is deliberately NOT a protocol extension: the agent
  serves it as one more local TCP service (auto-added to the allowlist) and the
  client opens a normal tunnel session to it, so the relay needs no changes. Both
  ends run the same symmetric `clip.Sync` loop (poll → push, hash guard against
  echo loops, ping keepalive). The agent sends one `hello` on accept and the
  client requires it before declaring the sync up (a relay ack alone only proves
  the agent is online, not that it serves the port); refused ports are answered
  with an immediate dial-back-and-close so clients fail in seconds, not on a 90s
  timeout. The client reconnects with 3s→60s exponential backoff — a fixed 3s
  retry once got the pooled connection WAF-banned for hours; don't reintroduce it.
  Images sync too (macOS ends only): PNG via osascript (`clipboard info` gives a
  cheap change signature so the multi-MB fetch only happens on change), chunked
  into `img` frames (32 KiB raw/chunk, 8 MiB cap) since WriteMsg frames top out
  at 64 KiB. Unknown msg types are ignored, so mixed versions fall back to text. Clipboard access shells out to pbcopy/pbpaste
  (wrapped in `launchctl asuser` when the agent runs as root, so the LaunchDaemon
  reaches the console user's pasteboard, not root's). File transfer is
  intentionally NOT a feature — scp/sftp/rsync already ride the forwarded SSH.

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
