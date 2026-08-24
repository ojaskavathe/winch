# herdr triage (v0.8.2, 2026-08-22)

What herdr does vs where winch stands, from a full source read of the herdr
clone. Framing first, because it changes every judgment below: **herdr is not
a dashboard — it is a full terminal multiplexer** (client/server, own PTYs,
own copy mode, own remote story, plugin system, ~257k lines of Rust) with
agent awareness layered in. winch is the opposite bet: tmux stays the
multiplexer, winch adds only the agent layer. So herdr's surface splits into
thirds: things tmux already gives us, things we genuinely lack, and things we
do better.

## already matched (or won)

- **Live previews: winch wins outright.** herdr has NO pane previews of any
  kind — its navigator shows a one-line text summary. Billboards (live,
  delta-streamed, cursor, gesture-commit, statusbar tracking) don't exist
  there.
- **Detection architecture: same shape, shared DNA.** Screen-as-authority
  TOML manifests (ours derive from theirs — manifests/LICENSE-herdr), title
  fast path, working/blocked/idle/done with the seen bit, done-until-visited,
  idle-confirmed anti-flap, blocked-is-strict philosophy. Cadences are
  comparable (300ms identified, both).
- **Blocked reasons on the row**: winch shows "permission prompt" in the
  sidebar; herdr surfaces reasons only in a debug CLI.
- **Zero lock-in**: winch runs inside stock tmux, over plain ssh, and the
  NDJSON data plane (`winch events`) supports foreign frontends. herdr
  requires adopting herdr.
- **Flicker discipline**: both wrap frames in DECSET 2026; both keep chrome
  static (herdr deliberately has no spinner animation anywhere — same as us).

## N/A — tmux's job, not ours

Sessions/attach/detach/persistence, splits/zoom/resize/swap, copy mode +
search, scrollback, popups (display-popup), the entire keybinding system,
custom command binds, status segments (status-right), window titles
(set-titles), remote (ssh), drag-to-resize, per-pane mouse passthrough.
herdr had to rebuild all of it; rebuilding it is winch's explicit non-goal.
Worktree management also lands here for now (sessions-per-worktree is the
tmux idiom); revisit only if demand shows up.

## feature gaps (ranked)

1. **Agent coverage: 5 manifests vs herdr's 22.** Claude, codex, gemini,
   grok, opencode vs their list adding cursor, amp, copilot, droid, qwen,
   kimi, cline, devin, kiro, pi, etc. Port the high-usage ones first.
   Blocker for some: region vocabulary (next item).
2. **Manifest region vocabulary.** We have 9 regions; herdr adds the
   prompt-marker family (`after_last_prompt_marker`,
   `before/current_prompt_block_marker`, `whole_recent_without_...`) and
   `osc_progress` (OSC 9;4 progress → idle/working). Needed to port several
   manifests faithfully.
3. **`agent explain` equivalent.** herdr's killer debugging tool: which
   manifest, which rule matched, region evidence, fallback reason, --json.
   We have bench logs. This is the difference between users authoring
   manifest overrides and users filing issues.
4. **Notification delivery.** We display-message other clients on blocked,
   full stop. herdr: off/in-app/terminal/system delivery, a 1s re-check so
   flaps never notify, done-notifications, clickable toasts, sounds. The
   piece worth stealing first: **OSC 9 / OSC 99 to the outer terminal**
   (works over ssh, reaches the OS notification center via
   ghostty/kitty/iterm2/wezterm) + notify-on-done + the re-check delay.
   Delivery choice belongs in the config surface.
5. **Search/filter.** herdr's navigator: fuzzy search over panes plus
   state-filter chips (b/w/i/d/a). Our sidebar has no filtering at all. A
   `/` filter over the list + state chips would close it.
6. **Agent-targeted CLI verbs.** herdr: `agent prompt --wait --until`,
   `send-keys`, `wait`, `read` — an orchestration API for scripting agents.
   We have the world stream but no agent verbs. Pairs naturally with the
   planned cmd-namespace split; the data plane already knows which pane is
   which agent.
7. **Manifest ops.** Overrides dir exists; missing: hot-reload without
   daemon restart (`winch reload` is planned anyway) and an
   update/version story for manifests. herdr phones home to herdr.dev for
   manifest updates — we probably don't want auto-phone-home; a
   `winch update-manifests` style explicit pull is the tasteful version.
8. **Config surface.** Already on the roadmap; herdr's config-reference is
   effectively our menu of candidate `@winch-*` options (sidebar width,
   indicators style, notification delivery, confirm-close...).

## ui/ux gaps (ranked) — why it "looks nothing like herdr"

The one-line diagnosis: herdr's chrome is built on a 19-token semantic
palette with row fills and an accent color; ours is bold/dim/reverse plus
three hardcoded colors. Structure is fine — it's the color and typography
system that's missing.

1. **Palette + theming.** herdr: semantic tokens (accent, panel/active-row/
   selection backgrounds, surfaces, overlays, per-state colors), 18 themes,
   and — the one that fits winch best — a `terminal` theme mapping
   everything to ANSI-16 so chrome inherits the host scheme. winch should
   grow a small token set (accent, muted, state colors, row-fill) defaulting
   to ANSI colors, themable later via @winch-* options.
2. **Selection style.** We use full reverse-video for the selected row —
   harsh, and it's also our cursor. herdr fills the row bg (active_row_bg)
   and keeps a *separate* selection_bg token so cursor-on-active-row stays
   legible (they added that distinction in 0.8.2 for exactly this reason).
   Row-fill + accent text beats reverse.
3. **State indicators.** herdr default: colored dots — ● red blocked,
   ● yellow working, ● teal done, ○ green idle, · unknown (attention by
   hue, hollow = seen); a `symbols` alt set (× ◐ ✓ ○ ·) for shape
   distinguishability. Ours (! ✓ ✻ colored) is closest to symbols mode;
   worth adopting the dots default + an indicator-style option, plus an
   unknown marker.
4. **Row typography.** herdr rows: token system with ` · ` separators,
   width-aware truncation (CJK-safe; ours is rune-count), lowercase
   chrome everywhere, two-row entries (state+name / branch+git ↑2↓1). Even
   staying single-line: separators, width-aware truncation, and a git
   branch token on session rows would move us most of the way.
5. **Headers.** ` sessions` / ` agents` lowercase, muted+bold — cheap,
   instantly herdr-flavored. Our `─ agents ───` rule stays (it's also the
   drag handle).
6. **Key-hint bar.** herdr shows a one-row mode bar (accent badge + key
   hints) for every mode. A hint row during scrub (`enter commit · q undock
   · / filter`) would teach the UI for free.
7. **Accent cues.** herdr lights the sidebar's `│` edge in accent while
   navigating. Same trick works for scrub mode. (Our billboard active-pane
   border already does accent — but it's a hardcoded RGB; fold it into the
   palette.)
8. **Sidebar width.** Fixed 40 vs herdr's 26 default, 18–36 clamp,
   drag-to-resize, collapsible to a 4-col dot strip. Ours should at least
   be a config option; we already drag the section divider, the outer edge
   can drag too. A collapsed dot-strip mode is a nice later trick.
9. **Empty states + diagnostics.** herdr: friendly empty-state copy with a
   live keybind hint; a yellow top banner when config is invalid. We show
   blank space and log-file errors.

## explicitly not adopting

- The multiplexer (all of §N/A).
- Plugin system / marketplace, remote-manifest phone-home, auto-update
  machinery, sounds-by-default (evaluate a sound option later; herdr ships
  mp3 playback with per-agent overrides — heavy).
- Their 16ms render loop: our painting is event/tick-driven with delta
  frames; different architecture, same observed smoothness.

## sidebar anatomy, from source (2026-08-23)

What herdr's sidebar is actually made of (`src/ui/sidebar.rs`,
`src/ui/status.rs`, `src/app/state.rs`). Terminals have no font sizes — the
"size" impression is a four-tier foreground ladder plus bold:

**The type ladder** (catppuccin values): `text` #cdd6f4 + BOLD for the
active/selected name -> `subtext0` #a6adc8 for inactive names -> `overlay0`
#6c7086 for chrome (headers, prefixes, agent labels, separators, "new"/
"menu") -> DIM stacked on a state color for state words. Four brightness
steps read like three font sizes. ANSI-16 gives us at most default/dim/90 —
the flatness we have left is exactly the missing ladder, and the fix is an
RGB theme (default catppuccin like herdr, `terminal` = ANSI fallback).

**Spacing** (the part that makes it breathe):
- spaces section: 2 header rows (` spaces` overlay0+BOLD, then a blank).
- agents section: 3 header rows (blank + ` agents` + blank), header carries
  a right-aligned sort toggle (`grouped`/`priority`, accent when filtered).
- entries are 2-row cards; `row_gap` between cards (0 in config default,
  1 in the shipped look; suppressed before indented worktree children).
- prefixes: 1 space before the icon row, 3 spaces on continuation rows;
  worktree children get `   ├─ `/`   └─ ` (overlay0) and `   │    `.
- token separator ` · `; a single space after the state icon.
- sidebar width 26 default, clamped 18-36, drag-resizable — a third
  narrower than our 40; tight width + 2-row cards is half the aesthetic.
- footer row: ` new` left, `menu` right, both overlay0.

**Backgrounds** (cell-level fills across the whole card):
- selected (navigate cursor): `selection_bg` #313244 — falls back to
  `active_row_bg` when the theme leaves selection transparent.
- active-but-not-selected: `active_row_bg` #1e1e2e. TWO fills exist so the
  cursor stays visible on the active row; we only have selection.
- dragged: `surface1`. Drag drop-target: full-width accent `─`.

**State encoding**: icon full-strength state color (● red/yellow/teal,
○ green, · overlay0), state WORD same color + DIM. We ship the word at full
color and no icon dimming — inverted; icon bright + word dim is theirs.
Branch token: mauve when active else overlay0; git `↑n` green `↓n` red.

**Still missing in winch after the restyle**: the RGB ladder + themes,
active-vs-selected dual fills, branch/git second row for sessions (needs
daemon git awareness), narrower default width + drag resize, scrollbar
(`▕` track / `▐` thumb), footer actions, collapsed 4-col dot strip,
right-aligned header tokens, drag reorder.

## what is a "space" for winch

herdr's hierarchy maps 1:1 onto tmux's, shifted one level up:

| herdr | tmux | note |
|---|---|---|
| session (server ns) | server / socket | both are the daemon boundary |
| workspace ("space") | **session** | project unit: name, cwd, git identity |
| tab | **window** | layout of panes |
| pane | pane | |

A space is a session, not a window. Sessions carry the project identity
(name, cwd, git); windows are auto-named command noise (`.claude-wrapped`).
The agents panel already uses the session name in the workspace slot for
exactly that reason. Worktree-as-workspace maps to session-per-worktree;
herdr's grouped/indented worktree children are the model for a future
"group sessions by repo" tree.

One consequence to hold onto: herdr's sidebar lists ONLY workspaces — tabs
live in the tab bar (tmux's own status-line window list is our tab bar).
winch deliberately deviates by nesting windows under sessions, because
window rows are what billboard scrubbing scrubs. The herdr-faithful styling
for that deviation is the worktree-child treatment: windows as indented,
subordinate one-line rows under two-row session cards.
