# demux

tmux-native agent dashboard. Needs tmux >= 3.6 (older tmux octal-escapes
control-mode output). A Go daemon (`demuxd`) rides tmux control mode,
detects agent state in every pane (claude, codex, gemini, grok, opencode — via
TOML manifests in `~/.config/demux/agents`), and serves a dockable sidebar TUI
with live, faithful billboard previews of every window.

- `cmd/demuxd` — daemon + sidebar TUI (one binary; deps: `x/term`, `BurntSushi/toml`)
- `rigs/` — integration rigs; need a real tmux server and ptys, so a separate
  module that never enters the nix checkPhase. Run with
  `go test -count=1 -parallel 2 .` (full parallel oversubscribes timing tests)
- `flake.nix` — `packages.demuxd`, `packages.demux` (sidebar launcher), overlay

Manifest match rules under `cmd/demuxd/manifests/` are derived from
[herdr](https://github.com/herdrdev/herdr) — see `manifests/LICENSE-herdr`.
