# Security Policy

Cotty gives other people a path into a live shell, so security reports get
top priority.

## Supported versions

| Version | Supported |
| ------- | --------- |
| 1.x     | ✅        |
| < 1.0   | ❌        |

## Reporting a vulnerability

**Please do not report security vulnerabilities through public issues.**

Instead, use GitHub's private vulnerability reporting: go to the repository's
**Security** tab → **Report a vulnerability**. Include:

- a description of the issue and its impact,
- steps to reproduce (a minimal session setup if possible),
- the affected component — host, relay, CLI client, web client, or protocol.

You can expect an acknowledgement within a few days. Please give us a
reasonable window to ship a fix before disclosing publicly.

## Scope

Especially interesting areas:

- **End-to-end encryption** ([`internal/e2ee`](internal/e2ee)) — key
  handling, nonce use, anything that lets a relay read or tamper with
  session traffic
- **Permission enforcement** ([`internal/session`](internal/session)) —
  any way for a view-only guest to reach the PTY, or to bypass
  allow/deny/kick
- **The relay** ([`internal/relay`](internal/relay)) — session isolation,
  code guessing, resource exhaustion
- **The web client** ([`internal/webui`](internal/webui)) — XSS, key
  leakage out of the URL fragment

## Hardening tips for users

- Prefer the default end-to-end encryption for relayed sessions; avoid
  `-plain`.
- Share join URLs over a channel you trust — anyone with the full URL has
  the session key.
- Keep guests view-only unless they need to type; grants are per guest and
  revocable live (`cotty ctl deny NAME`).
