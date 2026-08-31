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
