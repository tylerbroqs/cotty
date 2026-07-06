# Cotty

<p align="center">
  <img src="assets/wordmark.svg" alt="COTTY — the multiplayer terminal" width="640">
</p>

**The multiplayer terminal.** Host your shell, let teammates join over the
network, watch together — and, when you allow it, type together.

Cotty (*collaborative + tty*) targets the gap between screen-sharing hacks
(`tmate`, `sshx`, "look at my Zoom") and what real-time collaboration should
feel like in a terminal: first-class sessions with per-guest permissions,
presence, and eventually per-user cursors and audit trails.

## Status

Early (v0.4). Working today:

- `cotty host` spawns your shell in a PTY and serves it over a websocket
- `cotty join -name alice` mirrors the session in any terminal, with a
  display name everyone sees in join/leave notices
- `cotty relay` + `cotty host --relay <server>` share sessions across
  networks without port forwarding — the host dials out, so NAT is not a
  problem
- Guests are **view-only by default**, with per-guest grants at runtime:
  `cotty ctl allow alice`, `cotty ctl deny alice`, `cotty ctl kick bob`,
  `cotty ctl list` — from any terminal on the host machine
- Relayed sessions are **end-to-end encrypted by default** — the relay
  forwards ciphertext it cannot read
- Sessions are protected by a random join code

Not yet built: web client. See the roadmap below.

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
./cotty join -name alice "ws://192.168.1.10:7373/ws?code=XJ4K2P"
```

Guests press `Ctrl-]` to leave. The session ends when the host's shell
exits.

### Managing guests

Guests join view-only under the name they picked (`-name`, defaulting to
`$USER`). The host manages them live from any other terminal on the host
machine — or straight from inside the hosted shell, since every session
exports `$COTTY_SESSION`:

```sh
cotty ctl list          # who's here, and who can type
cotty ctl allow alice   # let alice type
cotty ctl deny alice    # back to view-only
cotty ctl kick bob      # disconnect bob
```

Starting with `--write` makes new guests writable by default instead.
Everyone gets join/leave notices, and permission changes are announced to
the affected guest.

### Across networks: hosting through a relay

Direct hosting requires guests to reach your machine. When you're behind
NAT (home network, office, coffee shop), run a relay on any machine with a
public address and host through it — the host connects *outward*, so no
port forwarding is needed on either side:

```sh
# On a public server
cotty relay -addr :7374
# behind TLS? tell it the public base URL guests should use:
cotty relay -addr :7374 -public-url wss://relay.example.com

# On your machine (anywhere)
cotty host --relay relay.example.com:7374
# prints: cotty join "ws://relay.example.com:7374/ws?code=XJ4K2P"

# Guests, from anywhere
cotty join "ws://relay.example.com:7374/ws?code=XJ4K2P"
```

The relay forwards frames and enforces the session's read-only setting;
the host additionally enforces it locally.

### End-to-end encryption

Relayed sessions are encrypted end-to-end by default. The host generates
a 256-bit session key and puts it in the join URL's *fragment*:

```
cotty join "ws://relay.example.com:7374/ws?code=XJ4K2P#k=8D0Uy-5ugL..."
                                                      └── never sent over
                                                          the network
```

URL fragments are stripped by clients before any request is made, so
guests receive the key from the host (through however the URL was shared)
while the relay never sees it. Terminal output and guest input are sealed
with AES-256-GCM; a guest joining without the key is refused with an
explanation, and a wrong key fails loudly rather than showing garbage.
Opt out with `cotty host --relay ... -plain`.

What the relay can still see: guest names, join/leave events, the session
code, terminal size, and traffic timing/volume. Share the join URL over a
channel you trust — anyone with the full URL has the key.

## How it works

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

With a relay, the fan-out moves server-side — the host holds one outbound
connection and the relay maintains the guest hub:

```
host terminal ── PTY ── ws (outbound) ──► relay ──► guest ws
                                            │ ────► guest ws
```

## Roadmap

- ~~**v0.2 — relay**: `cotty host --relay <server>` so sessions work across
  NATs without port forwarding; short shareable session URLs~~ ✅
- ~~**v0.3 — identity & permissions**: named guests, per-guest read/write
  grants, join/leave presence, host can kick~~ ✅
- ~~**v0.4 — end-to-end encryption**: relay sees ciphertext only~~ ✅
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
