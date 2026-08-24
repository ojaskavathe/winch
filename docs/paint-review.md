# Paint architecture review

Goal: painting should be fast, cheap, and structurally incapable of showing
the user a wrong intermediate state. This reviews the render path against
both halves of that, using measurements from a real session
(`main-fcb4dc0f.sock.tui-bench.log`, ~3000 keystrokes, 480x96 kitty client)
rather than estimates.

Verdict in one line: **the daemon is not the problem — the TUI's canvas
painter is, and more than half of what it writes is blank space.** Separately,
correctness during transitions rests on 46 hand-maintained ordering rules,
which is why the same class of bug keeps resurfacing.

---

## 1. Where the time actually goes

| Operation | count | mean | p90 / max | bytes | total |
|---|---:|---:|---:|---:|---:|
| canvas paint, full (`prev == nil`) | 2634 | **22.6 ms** | — / **263 ms** | 77 KB | 193 MB |
| canvas paint, diffed | 3850 | 0.49 ms | — / 55 ms | 3.6 KB | 13 MB |
| list paint | 6557 | 1.13 ms | 2.1 ms / 98 ms | 5.2 KB | 33 MB |
| daemon re-list | 2804 | 2.3 ms | — / 25 ms | — | — |
| daemon preview (capture) | 8066 | 2.3 ms | — / 25 ms | — | — |

Two facts fall out immediately:

- **A full canvas paint is 46x more expensive than a diffed one** (22.6 ms vs
  0.49 ms) and 21x larger. Full paints are 40% of all canvas paints.
- **The daemon is fast.** Capture and re-list are ~2.3 ms each. The cost is
  the TUI writing escapes and tmux ingesting them. Optimising the daemon
  would be optimising the wrong end.

Per keystroke during a scrub the TUI issues **2.18 list paints and 2.15
canvas paints**. Total time spent inside paint calls over the session:
**68.9 seconds**.

---

## 2. Cost findings

### F1 — 57% of a full canvas paint is blank space

`paintFrame` (tui.go:1793) begins, whenever `prev == nil`:

```go
blank := strings.Repeat(" ", avail)
for y := 1; y <= height; y++ {
    fmt.Fprintf(&b, "\033[%d;%dH%s", y, offX+1, blank)
}
```

At 480x96 that is 95 rows x (453 spaces + addressing) = **43.8 KB**, against
a measured mean full paint of 77.0 KB. The prefill exists so "stale content
from differently-shaped windows cannot linger" — but panes *tile* the region.
Every cell the prefill blanks is immediately overwritten by a pane, except
the handful of border cells, which `paintBorders` paints anyway.

**Fix:** erase only cells the incoming layout does not cover — the set
difference of the old and new pane rectangles. For the common case (same
layout, or any layout that tiles) that set is empty and the 43.8 KB
disappears entirely.

### F2 — the diff is keyed to window identity, not to screen content

tui.go:503:

```go
if win == paintedWin && sameGeometry(paintedPanes, scaled) {
    prev = paintedPanes
}
```

The screen does not care which window content came from. The TUI knows
exactly what it last painted; if the geometry matches, a line-level diff is
valid **regardless of source window**. As written, every scrub step to a new
window takes the full path by definition — which is precisely the hot path.

**Fix:** drop `win == paintedWin`; keep `sameGeometry`. Scrubbing between
same-shaped windows becomes a line diff. Combined with F1 this should take a
typical scrub step from ~77 KB to a few KB.

### F3 — 38% of list paints change nothing

2505 of 6557 `paint_list` calls produced zero changed rows. They rewrite the
whole 26-column strip (5.2 KB) so tmux can diff it back to nothing. Harmless
on screen, pure waste in CPU and pty bandwidth.

**Fix:** cache the last emitted string; if the newly rendered one is
identical, write nothing.

### F4 — multiple paints per event

2.18 list + 2.15 canvas paints per keystroke, and there are **16 direct paint
call sites** in the event loop (tui.go:507–1069). Each message handler paints
for itself, so one settled key batch that also receives a diff and a frame
paints three times.

**Fix:** handlers set dirty flags; one render pass runs at the bottom of the
event-loop iteration. This subsumes F3 and is the precondition for §3.

### F5 — prefetch frames are always full, and often never painted

Every settled keystroke sends up to three `preview` commands (target + both
neighbours, tui.go:585). Prefetches "never delta and never gate"
(preview.go:443) — each is a full capture, a full `selfContain` pass over
every line, and a full JSON marshal of ~77 KB, for content that may never be
displayed. Roughly half the full frames in the log arrived with
`current=false`.

**Fix:** let prefetches delta against the client's cached generation for that
window (the client already keeps `frames[win]` with a gen), and skip the
prefetch entirely when the client's cache for that window is fresh.

### F6 — daemon work that runs when nothing changed

Not currently a bottleneck (2.3 ms), but all of it is unconditional:

- **Detect ticker** does a whole-server `list-panes -a` every 300 ms (100 ms
  while confirming an idle transition) forever, even with zero agents at 2 s.
  Deliberate — it is the self-heal for panes notifications miss — but it is
  the one poll in an otherwise event-driven daemon.
- **Git ticker** spawns 1–2 `git` processes *per session* every 5 s
  regardless of whether anything changed.
- **Debounce re-arm** allocates a fresh `time.Timer` every 15 ms for the
  entire duration of a held-key scrub (daemon.go:231).
- **`fetchWorld`** issues four *separate* round trips (model.go:74) although
  `runPipelined` exists.
- **`publishAgents`** can run a complete `fetchWorld` *inside* the detect
  tick (detect.go:329).
- **`setWidth`** issues 4–5 unbatched `run` calls (dock.go:1364).

---

## 3. The correctness problem: invariants that live in prose

Every visual bug found this week was one of two invariant violations:

- **I1 — content precedes geometry.** No tmux operation that changes what the
  user is looking at may run before the content for the new geometry is
  already in its grid.
- **I2 — never paint from partial state.** The TUI must not paint a world it
  only partly knows.

Neither is enforced by the architecture. Instead there are **46 distinct
ordering constraints asserted in comments** across `dock.go` and `router.go`
— "status pad must ride the batch BEFORE switch-client", "capture the target
FIRST, zoom SECOND", "freeze automatic-rename before the split", "respawn
then unzoom, same batch", and so on. Each is correct. Each is also enforced
only by whoever writes the next call site remembering it.

The four bugs, mapped:

| Symptom | Invariant | Cause | Fixed by |
|---|---|---|---|
| garbled strip on unzoom | I1 | geometry changed, grid reinterpreted | alternate screen (clip, not reflow) |
| blank strip on commit-home | I1 | process killed, then geometry | dropped the respawn |
| blank strip on cross-window commit | I1 | client switched before first paint | hello now means "painted" |
| selection bar jumping | I2 | painted before the selection was known | selection stamped into the snapshot |

Each was patched at its own site. The next transition added will have to
re-derive all 46 rules.

### 3a. The structural fix now available: retire the two-phase handoff

`handoffState` exists for one stated reason (dock.go:776):

> Moving the zoomed TUI pane into the target shrinks its canvas-filled grid
> 480->40 and tmux REWRAPS it — a frame of garbled billboard in the sidebar
> strip before the repaint covers it.

**That reason no longer holds.** With the alternate screen tmux clips instead
of rewrapping (`window_pane_resize` passes `reflow = saved_grid == NULL`;
probe-verified). And the codebase already contains the simpler path for the
identical situation: `toggle` mid-scrub onto a carved window calls
`dockMove` — a geometry-free `swap-pane` — while zoomed, and comments that
"the swap already unzoomed the sidebar on its way out" (dock.go:755).

So `commitScrub` could call `dockMove` instead of `dockMoveStart`, deleting:

- `handoffState`, `armHandoff`, `handoffFinish`, `handoffTimeout`
- the "ack and drop every command for 300 ms" window (router.go:66)
- the second TUI process per commit, and with it the entire
  "switch before paint" bug class
- the coupling that makes `hello` a rendering signal

This is the single highest-value change in this document: it removes a state
machine, a process spawn, and a bug class simultaneously. It needs one rig
proving a swap-from-zoomed commit under the alternate screen produces no
artifact frame — `blankStripFrames` already measures exactly that.

### 3b. Make the invariants mechanical

- **I2:** one render pass per event-loop turn (F4), reading a state struct
  where "unknown" is representable. Painting cannot then race a message.
- **I1:** funnel every visible transition through one helper taking
  `(contentOps, geometryOps)` and emitting them in that order in a single
  control-mode batch. The 46 comments collapse into one enforced rule plus
  per-site notes on *what* is content and what is geometry.
- **Process lifetime:** with 3a, the TUI is created once per dock and never
  replaced. "Sidebar has no content" then exists only at `dockOpen`, where
  the pane itself is appearing and there is nothing to flicker.

---

## 4. Recommended order

| # | Change | Payoff | Risk |
|---|---|---|---|
| 1 | ~~F1 + F2~~ **done** (d51ec46) — measured 88.0 KB → 43.3 KB worst case, ~0.5 KB when content mostly matches; every scrub step now diffs. `rigs/paintcost_test.go` pins it | | |
| 2 | 3a: retire the handoff, commit via `swap-pane` | deletes a bug class and a state machine | medium — needs the swap-from-zoom rig |
| 3 | F4 + F3: single render pass, output cache | −38% list paints, −50% paints per key | low |
| 4 | F5: delta prefetches | ~halves frame bytes on the wire | low |
| 5 | 3b: mechanical I1/I2 | stops the bug class returning | medium, refactor |
| 6 | F6: batch `setWidth`, pipeline `fetchWorld`, stop timer churn | tidiness; daemon is not hot | low |

## 5. What is working and should not be touched

- **The spacer architecture.** Geometry-free `swap-pane` entry is why
  entering a 700k-line window costs ~7 ms instead of ~200 ms.
- **Zoom-based scrubbing.** Hidden panes keep byte-exact sizes; zero app
  reflows.
- **The alternate screen.** Now load-bearing for every resize path.
- **The delta frame path.** 0.49 ms / 3.6 KB is the target everything else
  should look like.
- **Whole-world re-list.** At 2.3 ms it is cheap, and it self-heals every gap
  in tmux's notification matrix. Do not shard it for performance.
