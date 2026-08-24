# winch architecture

winch is a sidebar for tmux — one dockable view converging the server's two
worlds: sessions/windows, and the coding agents (claude, codex, gemini,
grok, opencode) running inside them with their working/blocked/idle/done
states. A daemon mirrors the tmux server's entire state and serves the
sidebar with live previews of every window. One Go binary, three hats:

- `winch run` — the daemon. One per tmux server, keyed by the server socket.
- `winch tui` — the sidebar, spawned by the daemon into a real tmux pane.
- everything else (`toggle`, `nav`, `browse`, `ls`, `events`, `sock`,
  `equalize`) — thin client verbs; each is one short-lived socket connection,
  and any of them lazily autostarts the daemon.

Requires tmux >= 3.6: older tmux octal-escapes non-printable bytes in
control-mode command replies, which mangles both the daemon's `\x1f` field
separator and captured SGR. The daemon probes for this at attach and refuses
loudly rather than showing an empty world.

## the one big decision: control mode, not hooks

The daemon holds a single persistent `tmux -C attach` connection. Every
server event arrives as a notification line on it; commands go down the same
connection and replies correlate FIFO against `%begin`/`%end` blocks. No
`set-hook`, no `run-shell`, zero process spawns on the event path.

Notifications are treated as **dirty triggers only** — each one schedules a
debounced (15ms) whole-world re-list over the same connection. Whole-entity
on purpose: a few KB over a local socket, sub-millisecond, and it self-heals
every gap in tmux's notification matrix. The gaps are real and probe-verified
(tmux 3.7b): geometry does not notify across sessions, cross-session
`split-window -d` emits nothing, and pane title changes emit nothing. Diffing
whole entities beats trusting per-event bookkeeping.

The attach loop survives the attached session dying (`%exit` while the server
lives): the daemon reattaches and pushes a fresh snapshot, never a diff
across a gap.

## sockets and process model

`winch` mirrors tmux's own socket resolution (`-S` > `-L` > `$TMUX` >
default), resolves symlinks (macOS `/tmp` -> `/private/tmp`) so every client
agrees on server identity, and derives its own socket as
`/tmp/winch-$UID/<name>-<sha256[:4]>.sock`. The daemon binds only after the
first world fetch — a subscriber can never connect before there is a
truthful snapshot behind the socket. Startup also sweeps state a dead
predecessor may have leaked: orphaned spacer panes and stale `@winch_docked`
session options.

## world model and wire protocol

The world is sessions / windows / panes / clients, keyed by tmux's own ids
(`$0`, `@1`, `%2` — never indexes, which shift). Panes carry agent fields:
kind, state, and (blocked only) a human reason like "permission prompt".

The protocol is newline-delimited JSON over the unix socket, both directions
(`protocol.go` is the complete vocabulary). A connection begins with one
snapshot; whole-entity put/del diffs follow as the world changes. Everything
the daemon pushes is type-tagged (`snapshot`, `diff`, `select`, `frame`,
`reply`), so replies interleave safely with the world stream. Compatibility
is additive: the snapshot carries a protocol version and clients must ignore
unknown keys.

The TUI has no privileged channel — it is just a socket client. `winch
events` streams the same NDJSON to anything else; alternate frontends are a
supported direction, not an accident.

## agent detection

Detection is the daemon's only poll, because there is no event source for
what it needs (titles and screens don't notify). Cadence adapts: 300ms
normally, 100ms while an idle transition is confirming, 2s when no agent
panes are known (discovery only). The tick's own `list-panes` doubles as the
self-heal for panes the notification matrix misses.

Two tiers, screen as authority:

1. **Title** — a cheap fast path: an OSC-title rule (claude's spinner)
   classifies without a capture.
2. **Screen** — `capture-pane` text run through per-agent TOML manifests:
   ordered rules with priorities, regions, and gates (`contains`, regex,
   line anchors). Manifests are embedded in the binary and overridable from
   `~/.config/winch/agents/`. The match rules derive from
   [herdr](https://github.com/herdrdev/herdr) (see `manifests/LICENSE-herdr`),
   which learned the hard way that agent hooks are edge-triggered and lie —
   the screen is level-triggered truth.

State semantics:

- **working / blocked** publish instantly; blocked carries the matched
  rule's label as the reason shown in the sidebar.
- **idle** is confirmed, never assumed: a working -> idle transition holds
  for 3 consecutive samples before publishing (anti-flap).
- **done** is UI state, not detection: a completion in a window no attached
  client is watching becomes "done" and sticks until the user visits it.

Aggregate counts land in the `@winch_agents` server option for the
statusline (`!blocked ✓done ✻working`), and a blocked transition notifies
clients that aren't looking at that window.

## sidebar, dock, spacers

`winch toggle` docks the TUI as a **real 40-col pane** at the left edge of
the client's current window; the main area stays the user's actual panes —
live, focusable, typable. Undock restores the window's exact pre-dock layout.

Cross-window scrubbing can't resize every window eagerly, so windows the
scrub visits get a 40-col **spacer pane** holding the carved layout; spacers
are released one per tick after undock (releasing inline would stall tmux
mid-transition) and swept by the next daemon if one leaks. All layout
mutations batch kill-pane + select-layout into single tmux calls — two calls
means two visible reflows.

While docked, window navigation (`M-h`/`M-l`) routes through `winch nav` so
the sidebar rides along atomically; `@winch_docked` on the session is what
the tmux binds test to decide routing.

## billboards: the scrub preview

Scrubbing the list previews other windows as **billboards**: the daemon
captures the target window (`capture-pane -e`, one control-mode round trip
for all panes), rewrites each line to be self-contained SGR, and ships it as
a frame; the TUI paints it into the canvas right of the list, scaled to the
real window's geometry with tmux-style dim borders between panes.

The stream is a 100ms ticker on the current target with two cost gates: a
geometry+activity fingerprint skips capture entirely for quiet windows, and
frames diff against the last shipped grid so a spinner tick ships 1-3
changed rows, not the whole screen. Deltas carry a generation and base;
ordered hub delivery makes them safe (a client that missed a message is
disconnected, and a fresh hello replays a full frame).

Billboards are meant to be indistinguishable from the real thing:

- the real pane's **cursor** is painted (inverse cell), following the pane
  the current selection would focus on commit;
- the origin session's **status line** is overridden during a scrub to
  render the target session's windows, and restored verbatim on exit;
- **any mouse gesture** on a billboard split — wheel, click, middle, right —
  commits into the real pane, focused on the split under the pointer.
  Faithful mouse emulation is impossible from captures (alt-screen apps
  have no scrollback history to serve), so the gesture routes to reality
  and inherits full parity. The keyboard stays the deliberate picker.

All TUI paints wrap in synchronized output (DECSET 2026) so tmux applies
them atomically — no tearing, no flicker frames.

## equalize

`winch equalize` rebalances panes with nvim splits weighted by their
window's content (talking to nvim over its RPC socket). It needs no daemon
and can be compiled out with `-tags noequalize`.

## testing

- **Unit tests** (`cmd/winch`) are pure and run in the nix build sandbox.
- **Rigs** (`rigs/`, a separate module on purpose) are the real harness:
  each test boots an isolated tmux server, attaches a fake 200x50 client on
  a real pty (raw `/dev/ptmx` shims for darwin and linux), starts a daemon,
  and asserts on recorded client bytes — the ground truth of what a user
  sees. Run `go test -count=1 -parallel 2 .` — full parallelism
  oversubscribes the machine and flakes the timing tests. `-short` skips
  the big-scrollback tests (CI does).

## direction

Near-term architecture debts, recorded here so the shape is explicit:
protocol versioning docs for third-party frontends, decoupling the frame
stream from dock state, and a config surface (`@winch-*` tmux user options
read at attach plus `winch init` emitting the tmux.conf snippet).
