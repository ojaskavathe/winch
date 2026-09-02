package main

import (
	"fmt"
	"log"
	"strings"
)

// The shadow oracle: predict the exact geometry a window will have once the
// dock carves it, without touching the window. Porting tmux's layout
// arithmetic (layout_resize's distribution, layout_split_pane's rounding)
// would have to match it bug-for-bug forever; instead the prediction ASKS
// tmux, on a window that costs nothing to resize. A hidden detached session
// holds one window of dummy panes with no history; a prediction replays the
// carve there — apply the target's layout, normalize to the dock's size,
// split the sidebar slot off — and reads the resulting layout back. Same
// code path as the real carve, so the answer is exact by construction, and
// with no scrollback anywhere the whole round trip is O(1) whatever the
// target's history holds.
const (
	shadowSession = "winch-shadow"
	// Distinct from spacerCmd on purpose: the startup sweep and doctor hunt
	// stray spacers BY command string, and the shadow's dummies are not
	// strays.
	shadowCmd = "sleep 100000002"
)

// prect is one pane's predicted rectangle, in docked-window coordinates
// (the sidebar slot starts at x=0; mains sit right of it).
type prect struct {
	x, y, w, h int
}

// shadowState is the daemon's handle on the oracle window, plus a one-entry
// prediction cache: scrub re-previews the same window every stream tick, and
// the answer only changes when the source layout or dock size does.
type shadowState struct {
	win      string // shadow window id, "" until built
	panes    int    // dummy panes it currently holds
	cacheKey string
	cache    map[string]prect
}

// predictDock returns srcPane -> predicted rect for the docked (carved)
// geometry of a window whose CURRENT layout is layout, on a dock of
// dockW x dockH with a sideW-column sidebar. Any failure tears the shadow
// down and returns the error; callers fall back to the unpredicted frame.
func (d *daemon) predictDock(ctl *control, layout string, dockW, dockH, sideW int) (map[string]prect, error) {
	key := fmt.Sprintf("%s|%dx%d|%d", layout, dockW, dockH, sideW)
	if d.shadow.cacheKey == key {
		// Failures cache too (as a nil map, returned without error): the
		// stream ticker asks ten times a second, and an oracle that broke
		// once for this layout will break the same way until the layout
		// changes.
		return d.shadow.cache, nil
	}
	fail := func(err error) (map[string]prect, error) {
		d.shadow.cacheKey, d.shadow.cache = key, nil
		return nil, err
	}
	_, body, ok := strings.Cut(layout, ",")
	if !ok {
		return fail(fmt.Errorf("shadow: no checksum in %q", layout))
	}
	root, err := (&lparser{s: body}).node()
	if err != nil {
		return fail(err)
	}
	srcLeaves := leafOrder(root)
	n := len(srcLeaves)
	if n == 0 || n > dockH/2 {
		return fail(fmt.Errorf("shadow: %d panes unsupportable", n))
	}
	if err := d.ensureShadow(ctl, n); err != nil {
		d.dropShadow(ctl)
		return fail(err)
	}
	w := d.shadow.win
	// The predict batch mirrors the carve batch move for move: source-sized
	// window, source layout, dock-sized window (the carve's stale-size
	// normalize), sidebar split. Pane ids are listed IN the batch, right
	// before select-layout hands cells to panes in index order — the mapping
	// is read from the same instant it is created.
	seq := []string{
		fmt.Sprintf("resize-window -x %d -y %d -t %s", root.w, root.h, q(w)),
		"list-panes -t " + q(w) + " -F " + f("#{pane_id}"),
		"select-layout -t " + q(w) + " " + q(layout),
		fmt.Sprintf("resize-window -x %d -y %d -t %s", dockW, dockH, q(w)),
		fmt.Sprintf("split-window -d -hb -f -l %d -P -F '#{pane_id}' -t %s %s",
			sideW, q(w), q(shadowCmd)),
		"display-message -p -t " + q(w) + " -F " + f("#{window_layout}"),
	}
	out, err := ctl.runSeq(seq...)
	if err != nil {
		d.dropShadow(ctl)
		return fail(err)
	}
	// Outputs, in order: n pane ids, the split's pane id, the layout.
	if len(out) != n+2 {
		d.dropShadow(ctl)
		return fail(fmt.Errorf("shadow: predict batch returned %d lines, want %d", len(out), n+2))
	}
	order, spacer, predicted := out[:n], out[n], out[n+1]
	if _, err := ctl.run("kill-pane -t " + q(spacer)); err != nil {
		d.dropShadow(ctl)
		return fail(err)
	}
	_, pbody, ok := strings.Cut(predicted, ",")
	if !ok {
		return fail(fmt.Errorf("shadow: no checksum in predicted %q", predicted))
	}
	proot, err := (&lparser{s: pbody}).node()
	if err != nil {
		return fail(err)
	}
	byShadow := map[string]prect{}
	for _, leaf := range leafOrder(proot) {
		byShadow["%"+leaf.pane] = prect{x: leaf.x, y: leaf.y, w: leaf.w, h: leaf.h}
	}
	rects := make(map[string]prect, n)
	for j, leaf := range srcLeaves {
		r, ok := byShadow[order[j]]
		if !ok {
			return fail(fmt.Errorf("shadow: pane %s missing from predicted layout", order[j]))
		}
		rects["%"+leaf.pane] = r
	}
	d.shadow.cacheKey, d.shadow.cache = key, rects
	return rects, nil
}

// ensureShadow makes the hidden session exist and hold exactly n dummy
// panes. The window is grown tall before splitting so a run of even splits
// always has room, whatever state the last prediction left it in.
func (d *daemon) ensureShadow(ctl *control, n int) error {
	if d.shadow.win == "" {
		// Create first, atomically learning the window id from -P. NEVER
		// probe with display-message: from a session-less control client an
		// unresolvable -t doesn't error, it expands the format against
		// nothing — the probe once handed back an EMPTY window id, and the
		// split below, aimed at -t '', cut a pane into the user's current
		// window and unzoomed the sidebar mid-scrub.
		out, err := ctl.run(fmt.Sprintf("new-session -d -s %s -x 80 -y 50 -P -F %s %s",
			q(shadowSession), f("#{window_id}"), q(shadowCmd)))
		if err == nil && len(out) == 1 && strings.HasPrefix(out[0], "@") {
			d.shadow.win, d.shadow.panes = out[0], 1
		} else {
			// Duplicate session (an earlier daemon's leftover): adopt it.
			// list-panes, unlike display-message, fails properly when the
			// session truly is missing.
			out, err = ctl.run("list-panes -t " + q(shadowSession+":") + " -F " + f("#{window_id}"))
			if err != nil {
				return err
			}
			if len(out) == 0 || !strings.HasPrefix(out[0], "@") {
				return fmt.Errorf("shadow: adopt found no window (%q)", out)
			}
			d.shadow.win, d.shadow.panes = out[0], len(out)
		}
		if bench {
			log.Printf("bench shadow adopt win=%s panes=%d", d.shadow.win, d.shadow.panes)
		}
	}
	if d.shadow.panes == n {
		return nil
	}
	var seq []string
	if d.shadow.panes < n {
		seq = append(seq, fmt.Sprintf("resize-window -x 80 -y %d -t %s", max(50, 2*n+2), q(d.shadow.win)))
		for i := d.shadow.panes; i < n; i++ {
			seq = append(seq, fmt.Sprintf("split-window -d -v -t %s %s", q(d.shadow.win), q(shadowCmd)))
			seq = append(seq, "select-layout -t "+q(d.shadow.win)+" even-vertical")
		}
	} else {
		out, err := ctl.run("list-panes -t " + q(d.shadow.win) + " -F " + f("#{pane_id}"))
		if err != nil {
			return err
		}
		for i := n; i < len(out); i++ {
			seq = append(seq, "kill-pane -t "+q(out[i]))
		}
	}
	if _, err := ctl.runSeq(seq...); err != nil {
		return err
	}
	d.shadow.panes = n
	return nil
}

// dropShadow forgets (and kills) the oracle after any inconsistency; the
// next prediction rebuilds it from nothing.
func (d *daemon) dropShadow(ctl *control) {
	if d.shadow.win != "" {
		_, _ = ctl.run("kill-session -t " + q(shadowSession))
	}
	d.shadow = shadowState{}
}

// leafOrder walks a parsed layout and returns its leaves in string order —
// the order select-layout uses to hand cells to panes.
func leafOrder(n *lnode) []*lnode {
	if n.kind == 'l' {
		return []*lnode{n}
	}
	var out []*lnode
	for _, kid := range n.kids {
		out = append(out, leafOrder(kid)...)
	}
	return out
}
