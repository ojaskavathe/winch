# demux

a sidebar for tmux. One dockable pane that converges both views of the
world: your sessions and windows, and the coding agents running inside
them — with live, faithful billboard previews of every window. A Go daemon
(`demuxd`) rides tmux control mode, mirrors the whole server, and detects
agent state in every pane (claude, codex, gemini, grok, opencode — TOML
manifests, overridable in `~/.config/demux/agents`). Needs tmux >= 3.6
(older tmux octal-escapes control-mode output).

- `cmd/demuxd` — daemon + sidebar TUI (one binary; deps: `x/term`, `BurntSushi/toml`)
- `rigs/` — integration rigs; need a real tmux server and ptys, so a separate
  module that never enters the nix checkPhase. Run with
  `go test -count=1 -parallel 2 .` (full parallel oversubscribes timing tests)
- `flake.nix` — `packages.demuxd`, `packages.demux` (sidebar launcher), overlay

Manifest match rules under `cmd/demuxd/manifests/` are derived from
[herdr](https://github.com/herdrdev/herdr) — see `manifests/LICENSE-herdr`.
