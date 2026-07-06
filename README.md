# Cotty

**The multiplayer terminal.** Host your shell, let teammates join over the
network, watch together — and, when you allow it, type together.

Cotty (*collaborative + tty*) targets the gap between screen-sharing hacks
(`tmate`, `sshx`, "look at my Zoom") and what real-time collaboration should
feel like in a terminal: first-class sessions with per-guest permissions,
presence, and eventually per-user cursors and audit trails.

## Status

Early scaffold (v0). The core loop already works on a LAN:

- `cotty host` spawns your shell in a PTY and serves it over a websocket
- `cotty join` mirrors the session in any terminal
- Guests are **view-only by default**; the host opts into shared typing
  with `--write`
- Sessions are protected by a random join code

Not yet built: relay for NAT traversal, per-guest identity, encryption,
web client. See the roadmap below.

## Quick start

```sh
# Build
go build -o cotty ./cmd/cotty

# On the host machine
./cotty host            # view-only guests
./cotty host --write    # guests can type too

# Cotty prints something like:
#   cotty: session code XJ4K2P
#   cotty: guests join with: cotty join ws://<this-host>:7373/ws?code=XJ4K2P

# On a guest machine
./cotty join "ws://192.168.1.10:7373/ws?code=XJ4K2P"
```

Guests press `Ctrl-]` to leave. The session ends when the host's shell
exits.

## How it works (v0)

```
host terminal ──┐
                ├── PTY (your shell)
guest ws ───────┤        │
guest ws ───────┘        ▼
                 output fan-out to local stdout + all guests
```

The host process owns the PTY. Local keystrokes and (if `--write`) guest
keystrokes are written to it; everything the PTY emits is fanned out to the
local terminal and every connected guest. Frames are JSON over websocket —
see [`internal/protocol`](internal/protocol/protocol.go). v0 is
deliberately debuggable; a binary protocol comes later.

## Roadmap

- **v0.2 — relay**: `cotty host --relay <server>` so sessions work across
  NATs without port forwarding; short shareable session URLs
- **v0.3 — identity & permissions**: named guests, per-guest read/write
  grants, join/leave presence, host can kick
- **v0.4 — end-to-end encryption**: relay sees ciphertext only
- **v0.5 — web client**: join from a browser (xterm.js), proper resize
  handling
- **v1.0 — true multiplayer**: CRDT-backed shared input with per-user
  cursors, "who ran what" audit log, session recording & replay

## Development

```sh
go vet ./...
go build ./...
```

Requires Go 1.24+. Linux and macOS; Windows guests should work (`join`
uses no PTY), Windows hosting is untracked for now.

## License

MIT — see [LICENSE](LICENSE).
