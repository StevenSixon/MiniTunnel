# Security Policy

MiniTunnel exists to move sensitive traffic (SSH, remote desktop) safely across
untrusted networks, so security reports are taken seriously.

## Reporting a vulnerability

**Please do not open a public GitHub issue for security vulnerabilities.**

Instead, report privately via GitHub's
[Security Advisories](https://github.com/StevenSixon/MiniTunnel/security/advisories/new)
("Report a vulnerability"). Include:

- A description of the issue and its impact.
- Steps to reproduce, or a proof of concept.
- Affected component(s): `relay`, `agent`, `client`, or the wire protocol.

You can expect an initial acknowledgment within a few days. Once a fix is
available, we will coordinate disclosure with you.

## Security model (what MiniTunnel assumes)

- **Every connection is TLS.** The agent and client pin the relay's self-signed
  certificate via `RootCAs` and verify the fixed SAN `minitunnel-relay`, so
  trust is never tied to an IP or hostname.
- **A pre-shared key (PSK)** authenticates every connection with a constant-time
  compare. Treat it as a secret; rotate it if exposed.
- **A port allowlist** on the agent (`-allow`, default `22,5900`) prevents a
  client from reaching arbitrary ports on the target.
- When tunneled over WebSocket through an L7 gateway, the pinned TLS runs
  **inside** the WebSocket, so the gateway only ever sees ciphertext.

## Handling secrets

- `cert.pem`, `key.pem`, `*.psk`, and `.env` are gitignored. Never commit them.
- The relay's `key.pem` must live only on the relay host.
- Prefer a long, random PSK: `openssl rand -hex 24`.
