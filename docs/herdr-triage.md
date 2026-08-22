# herdr triage (v0.8.2, 2026-08-22)

What herdr does vs where demux stands, from a full source read of the herdr
clone. Framing first, because it changes every judgment below: **herdr is not
a dashboard — it is a full terminal multiplexer** (client/server, own PTYs,
own copy mode, own remote story, plugin system, ~257k lines of Rust) with
agent awareness layered in. demux is the opposite bet: tmux stays the
multiplexer, demux adds only the agent layer. So herdr's surface splits into
thirds: things tmux already gives us, things we genuinely lack, and things we
do better.

## already matched (or won)

- **Live previews: demux wins outright.** herdr has NO pane previews of any
  kind — its navigator shows a one-line text summary. Billboards (live,
  delta-streamed, cursor, gesture-commit, statusbar tracking) don't exist
  there.
- **Detection architecture: same shape, shared DNA.** Screen-as-authority
  TOML manifests (ours derive from theirs — manifests/LICENSE-herdr), title
  fast path, working/blocked/idle/done with the seen bit, done-until-visited,
  idle-confirmed anti-flap, blocked-is-strict philosophy. Cadences are
  comparable (300ms identified, both).
- **Blocked reasons on the row**: demux shows "permission prompt" in the
  sidebar; herdr surfaces reasons only in a debug CLI.
- **Zero lock-in**: demux runs inside stock tmux, over plain ssh, and the
  NDJSON data plane (`demuxd events`) supports foreign frontends. herdr
  requires adopting herdr.
- **Flicker discipline**: both wrap frames in DECSET 2026; both keep chrome
  static (herdr deliberately has no spinner animation anywhere — same as us).

## N/A — tmux's job, not ours

Sessions/attach/detach/persistence, splits/zoom/resize/swap, copy mode +
search, scrollback, popups (display-popup), the entire keybinding system,
custom command binds, status segments (status-right), window titles
(set-titles), remote (ssh), drag-to-resize, per-pane mouse passthrough.
herdr had to rebuild all of it; rebuilding it is demux's explicit non-goal.
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
   daemon restart (`demuxd reload` is planned anyway) and an
   update/version story for manifests. herdr phones home to herdr.dev for
   manifest updates — we probably don't want auto-phone-home; a
   `demuxd update-manifests` style explicit pull is the tasteful version.
8. **Config surface.** Already on the roadmap; herdr's config-reference is
   effectively our menu of candidate `@demux-*` options (sidebar width,
   indicators style, notification delivery, confirm-close...).

## ui/ux gaps (ranked) — why it "looks nothing like herdr"

The one-line diagnosis: herdr's chrome is built on a 19-token semantic
palette with row fills and an accent color; ours is bold/dim/reverse plus
three hardcoded colors. Structure is fine — it's the color and typography
system that's missing.

1. **Palette + theming.** herdr: semantic tokens (accent, panel/active-row/
   selection backgrounds, surfaces, overlays, per-state colors), 18 themes,
   and — the one that fits demux best — a `terminal` theme mapping
   everything to ANSI-16 so chrome inherits the host scheme. demux should
   grow a small token set (accent, muted, state colors, row-fill) defaulting
   to ANSI colors, themable later via @demux-* options.
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
