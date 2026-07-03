# Contributing to MiniTunnel

Thanks for your interest in improving MiniTunnel! This is a small, focused
project, and contributions of all sizes are welcome — bug reports, docs fixes,
and features alike.

## Scope: what belongs here

MiniTunnel builds an **encrypted transport** and forwards to services the target
already runs (`sshd`, macOS Screen Sharing). To keep it small and auditable:

- ✅ **In scope:** transport, relay/agent/client reliability, deployment,
  documentation, new forwarding modes, security hardening.
- ❌ **Out of scope:** anything that reimplements SSH or VNC, a screen-capture
  or terminal protocol, or a hosted/SaaS control plane.

When in doubt, open an issue to discuss before writing code.

## Dependency policy

MiniTunnel is **stdlib-only, with a single deliberate exception**
(`github.com/coder/websocket`, backing the optional WebSocket transport). New
third-party dependencies must clear a high bar: they need to solve a problem
that is genuinely unsafe or unreasonable to hand-roll. Please raise it in an
issue first.

## Development setup

```sh
git clone https://github.com/StevenSixon/MiniTunnel.git
cd MiniTunnel
go build ./...        # builds all four programs
go vet ./...          # run before every commit
```

Requirements: Go 1.23 or newer.

## Testing

There are **no Go unit tests** — the four programs are verified end to end.
A quick local loop:

```sh
export MINITUNNEL_PSK="$(openssl rand -hex 24)"
go run ./cmd/gencert                                   # cert.pem + key.pem
./relay  -addr :7000 -cert cert.pem -key key.pem &     # rendezvous
./agent  -relay 127.0.0.1:7000 -cert cert.pem -id dev &# target side (allows :22,:5900)
./client -relay 127.0.0.1:7000 -cert cert.pem -agent dev
# then: ssh -p 2222 you@127.0.0.1
```

Please describe the end-to-end scenario you exercised in your PR.

## Pull requests

- Keep changes focused; one logical change per PR.
- Run `go build ./...` and `go vet ./...` — both must pass. CI runs them too.
- Match the surrounding style: the code favors clear comments explaining *why*,
  not *what*. Read a neighboring file before adding a new one.
- All four programs share one wire protocol in `internal/proto` and version
  together. A protocol change means rebuilding and redeploying all of them —
  call that out explicitly in the PR description.
- Update `README.md` and `.env.example` when you add or change a flag (every
  flag has a `MINITUNNEL_*` env fallback — give new ones the same treatment).

## Reporting bugs

Open an issue with what you expected, what happened, and the minimal steps to
reproduce. Redact any PSK, certificate, or hostname you don't want public.

## Security

Please do **not** open public issues for security vulnerabilities. See
[SECURITY.md](SECURITY.md) for private reporting.
