# winch

a sidebar for tmux. One dockable pane that converges both views of the
world: your sessions and windows, and the coding agents running inside
them — with live, faithful billboard previews of every window. A Go daemon
(`winch`) rides tmux control mode, mirrors the whole server, and detects
agent state in every pane (claude, codex, gemini, grok, opencode — TOML
manifests, overridable in `~/.config/winch/agents`). Needs tmux >= 3.6
(older tmux octal-escapes control-mode output).

- `cmd/winch` — daemon + sidebar TUI (one binary; deps: `x/term`, `BurntSushi/toml`)
- `rigs/` — integration rigs; need a real tmux server and ptys, so a separate
  module that never enters the nix checkPhase. Run with
  `go test -count=1 -parallel 2 .` (full parallel oversubscribes timing tests)
- `flake.nix` — `packages.winch`, `packages.winch` (sidebar launcher), overlay

## agent cards

Each agent is a card: a head row of context and a row carrying the agent's
own name for the conversation. Both are yours.

    tmux set -g @winch-agent-rows "state_text workspace tab agent | title"

Rows are separated by `|`, tokens by whitespace. Tokens:

| token        | is                                                     |
|--------------|--------------------------------------------------------|
| `state_text` | `blocked` / `background` / `working` / `done` / `idle`  |
| `workspace`  | session name                                           |
| `tab`        | window label                                           |
| `agent`      | manifest id — `claude`, `codex`, …                     |
| `title`      | the agent's name for this conversation                 |
| `reason`     | why it stopped — `permission prompt`, `shell still running` |

State words are coloured herdr's way: blocked red, working yellow, done
teal, idle green, plus peach for winch's own `background`. There is no
`state_icon` token — the glyph lives in the mark column with the session and
window marks, which is what makes a card read as one thing.

The head row **drops** trailing tokens when the sidebar is narrow (a
half-written agent name reads as breakage); the rows under it **truncate**
on whole tokens instead, so a card never loses its identity. An unknown
token is ignored with a line in the daemon log and the default layout is
used — one typo should not render a card with a hole in it.

Left unset, the default is `state_text workspace tab agent | title` with one
adjustment: the state word is suppressed for `working`, `done` and `idle`,
where the glyph's colour already says it. Set the option explicitly and it
is never second-guessed. herdr does the same thing
(`default_agent_rows_remove_redundant_state_text`).

### agent states

| state        | means                                                            |
|--------------|------------------------------------------------------------------|
| `working`    | the turn is running                                              |
| `blocked`    | it wants you — permission prompt, form, elicitation              |
| `background` | the turn ENDED and the agent takes input; side work is still live |
| `done`       | the turn ended in a window nobody was watching, still unvisited  |
| `idle`       | ended and seen                                                   |

`background` is winch-only. herdr classifies "1 shell still running" as
working, which means the agent never reaches a completion and the turn's
notification never fires at all while the shell lives. Here it is a
completion: it notifies once, then falls to `idle` when the side work ends.

## desktop notifications

A blocked agent notifies your terminal, not just tmux — winch writes the
notification OSC straight to `#{client_tty}`, because tmux itself swallows
OSC 9 (it only understands the ConEmu `9;4` progress form). Works over ssh.

Check your terminal understands it, and which dialect it wants:

    winch notify-test          # OSC 777 (kitty, wezterm, ghostty, urxvt)
    winch notify-test 9        # OSC 9   (iTerm2, kitty, Windows Terminal)
    winch notify-test 99       # kitty's own protocol

    tmux set -g @winch-notify        blocked   # off | blocked | all (adds turn-end)
    tmux set -g @winch-notify-osc    777
    tmux set -g @winch-notify-via    terminal  # terminal | system | both
    tmux set -g @winch-notify-delay  1000      # ms a state must hold to notify

The delay is a flap guard: agents flicker through blocked on their own, and
a prompt you answered before the toast arrived did not need one.

`via system` asks the OS instead of the terminal — `osascript` on macOS,
`notify-send` elsewhere. It loses the works-over-ssh property (it notifies
the machine the daemon is on), so `terminal` stays the default everywhere.

**macOS:** if nothing appears, check System Settings → Notifications for your
terminal *before* suspecting the sequence. Two different failures live there
and neither reports an error:

- **listed but off** — macOS prompted once, the prompt was dismissed, and the
  app has been denied ever since. Flip it on.
- **not listed at all** — the app has never successfully asked for
  authorization, so there is nothing to grant. kitty from nixpkgs is in this
  state. No dialect helps; use `winch notify-test system` and set
  `@winch-notify-via system`.

Not the cause, each ruled out by controlled comparison: the ad-hoc code
signature nixpkgs gives its own builds (`terminal-notifier` has the identical
`Signature=adhoc` / `Info.plist=not bound` and registers fine), the process
tree the notifier runs in, and the OSC dialect.

Manifest match rules under `cmd/winch/manifests/` are derived from
[herdr](https://github.com/herdrdev/herdr) — see `manifests/LICENSE-herdr`.

## platform support

The daemon and TUI are pure Go and platform-neutral. Everything OS-specific
lives under `platform/`, and the split is enforced by build tags rather than
by runtime checks, so a Linux build cannot compile a reference to a macOS
path:

    platform/darwin/notifier/   winch-notify.app — Objective-C, UserNotifications
    platform/darwin/mkicns/     draws the .icns, stdlib only
    cmd/winch/notify_darwin.go  the bundle route + notify-install
    cmd/winch/notify_other.go   stubs; there is nothing to do elsewhere

**Linux and BSD need none of it.** Notifications go to the terminal as an OSC
(the default, and the one that follows you over ssh), or to `notify-send`
with `@winch-notify-via system`. Neither has macOS's rule that a notification
belongs to a registered app bundle, which is the only reason the bundle
exists. `winch notify-install` says so and exits.

The flake exposes `winch-notify` only on darwin, and only a darwin `winch`
carries the `-X main.notifyApp=` ldflag.
