//go:build !noequalize

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
)

// `winch equalize` — equalize the current window's panes, weighting each
// nvim pane by its internal split counts (a pane showing 2 nvim columns
// deserves twice the width), then `wincmd =` inside every nvim. Absorbed
// from the standalone tmux-equalize-nvim tool; runs standalone over exec'd
// tmux — no daemon needed, so it works in plain tmux with winch idle.
//
// Docked interplay: with a winch sidebar in the window (@winch_sidebar), only
// the main region is equalized — select-layout assigns geometry by pane INDEX
// order, which diverges from geometric order with a joined sidebar and would
// shuffle contents. Per-leaf absolute resize-pane is content-safe. The window
// is marked @winch_layout_dirty so the daemon gives the sidebar's columns
// back proportionally on undock instead of restoring the pre-dock snapshot.

type eqPane struct {
	command string
	server  string
	fixed   bool
}

// eqTmux runs tmux against the resolved socket (unlike the old standalone
// tool, which leaned on the TMUX env var).
type eqTmux struct{ sock string }

func (t eqTmux) out(args ...string) (string, error) {
	out, err := exec.Command(tmuxPath, append([]string{"-S", t.sock}, args...)...).Output()
	return strings.TrimRight(string(out), "\n"), err
}

func (t eqTmux) ok(args ...string) bool {
	cmd := exec.Command(tmuxPath, append([]string{"-S", t.sock}, args...)...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run() == nil
}

// cmdEqualize is the `winch equalize [pane]` entrypoint. The pane argument
// (bind with "#{pane_id}") pins the target; without it tmux's default
// resolution picks the attached client's current pane.
func cmdEqualize(tmuxSock, pane string) {
	if err := equalizeRun(eqTmux{sock: tmuxSock}, pane); err != nil {
		fmt.Fprintf(os.Stderr, "winch equalize: %v\n", err)
		os.Exit(1)
	}
}

func equalizeRun(t eqTmux, pane string) error {
	target := []string{"display-message", "-p"}
	if pane != "" {
		target = append(target, "-t", pane)
	}
	currentPane, err := t.out(append(target, "#{pane_id}")...)
	if err != nil {
		return err
	}
	currentWindow, err := t.out(append(target, "#{window_id}")...)
	if err != nil {
		return err
	}
	// Standalone equalize runs on the window you are looking at, and
	// select-layout can move the active pane, so restore focus to it.
	return equalizeWindow(t, currentWindow, currentPane)
}

// equalizeWindow equalizes one window's panes (nvim splits weighted) and runs
// `wincmd =` in every nvim it holds. focusPane, when non-empty, is selected
// last: select-layout can move the active pane, so a focus-preserving caller
// passes the pane to land back on. The daemon-routed docked caller passes ""
// — the sidebar owns focus, and equalizing a window you may only be PREVIEWING
// must not yank the keyboard into it.
func equalizeWindow(t eqTmux, window, focusPane string) error {
	if window == "" {
		w, err := t.out("display-message", "-p", "#{window_id}")
		if err != nil {
			return err
		}
		window = w
	}
	layout, err := t.out("display-message", "-pt", window, "#{window_layout}")
	if err != nil {
		return err
	}
	_, body, ok := strings.Cut(layout, ",")
	if !ok {
		return fmt.Errorf("invalid tmux layout: %s", layout)
	}
	root, err := (&lparser{s: body}).node()
	if err != nil {
		t.ok("select-layout", "-t", window, "-E")
		return err
	}

	panes := eqCurrentPanes(t, window)
	fixed := ""
	for id, p := range panes {
		if p.fixed {
			fixed = id
		}
	}
	counts, clients := eqNvimCounts(t, panes)
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()

	if fixed != "" {
		if !eqDocked(t, root, fixed, window, counts) {
			return nil
		}
	} else {
		eqAssign(root, root.x, root.y, root.w, root.h, counts)
		body = lrender(root)
		if !t.ok("select-layout", "-t", window, lchecksum(body)+","+body) {
			t.ok("select-layout", "-t", window, "-E")
		}
	}

	for paneID, c := range clients {
		if err := c.Command("wincmd ="); err != nil {
			t.ok("set-option", "-pt", "%"+paneID, "-u", "@nvim_server")
		}
	}
	if focusPane != "" {
		t.ok("select-pane", "-t", focusPane)
	}
	return nil
}

// dockEqualize is the daemon side of `winch equalize-dock`: equalize honoring
// the rule that a non-sidebar-native command acts on the SELECTION, not the
// sidebar pane the keystroke lands on.
//
//   - Scrubbing: equalize the previewed window (the selection), then force a
//     fresh billboard capture so the balanced layout is what you see and what
//     Enter commits. Without this the standalone tool resolved the window to
//     the docked ORIGIN (the sidebar is zoomed over it) and its trailing
//     select-pane snapped focus home — the reported bug.
//   - Docked idle: equalize the docked window's main region (eqDocked skips
//     the fixed sidebar pane).
//
// Focus is never pulled out of the sidebar.
func (d *daemon) dockEqualize(ctl *control) error {
	p := d.dock
	if p == nil {
		return nil // undocked between the if-shell test and the command
	}
	win := p.win
	if p.scrubbing {
		switch {
		case p.scrubWin != "":
			win = p.scrubWin
		case d.pv.target != "":
			win = d.pv.target
		}
	}
	if win == "" {
		return nil
	}
	if err := equalizeWindow(eqTmux{sock: d.tmuxSock}, win, ""); err != nil {
		return err
	}
	if p.scrubbing {
		d.pv.reset()
		return d.preview(ctl, win, false, false)
	}
	return nil
}

func eqCurrentPanes(t eqTmux, window string) map[string]eqPane {
	lines, err := t.out("list-panes", "-t", window, "-F",
		"#{pane_id}\t#{pane_dead}\t#{pane_current_command}\t#{@nvim_server}\t#{@winch_sidebar}")
	if err != nil {
		return nil
	}
	panes := map[string]eqPane{}
	for _, line := range strings.Split(lines, "\n") {
		parts := strings.SplitN(line, "\t", 5)
		for len(parts) < 5 {
			parts = append(parts, "")
		}
		if parts[1] != "0" {
			continue
		}
		panes[strings.TrimPrefix(parts[0], "%")] = eqPane{
			command: parts[2],
			server:  parts[3],
			fixed:   parts[4] == "1",
		}
	}
	return panes
}

// eqNvimCounts asks each registered nvim (@nvim_server pane option) for its
// window layout and reduces it to per-axis visible split counts. A server
// that fails to answer gets its registration cleared — it's gone.
func eqNvimCounts(t eqTmux, panes map[string]eqPane) (map[string]map[string]int, map[string]*nvimConn) {
	counts := map[string]map[string]int{}
	clients := map[string]*nvimConn{}
	for paneID, p := range panes {
		if p.server == "" || !eqIsNvim(p.command) {
			continue
		}
		drop := func() { t.ok("set-option", "-pt", "%"+paneID, "-u", "@nvim_server") }
		if _, err := os.Stat(p.server); err != nil {
			drop()
			continue
		}
		c, err := nvimDial(p.server)
		if err != nil {
			drop()
			continue
		}
		text, err := c.EvalJSON("winlayout()")
		if err != nil {
			c.Close()
			drop()
			continue
		}
		var layout any
		if err := json.Unmarshal([]byte(text), &layout); err != nil {
			c.Close()
			drop()
			continue
		}
		counts[paneID] = map[string]int{
			"x": eqAxisCount(layout, "x"),
			"y": eqAxisCount(layout, "y"),
		}
		clients[paneID] = c
	}
	return counts, clients
}

func eqIsNvim(command string) bool {
	return command == "nvim" || command == "vim" || command == "view" || strings.HasPrefix(command, "nvim-")
}

// eqAxisCount walks a winlayout() tree (["leaf", id] / ["row"|"col", [kids]])
// and counts visible splits along one axis.
func eqAxisCount(layout any, axis string) int {
	items, ok := layout.([]any)
	if !ok || len(items) < 2 {
		return 1
	}
	kind, _ := items[0].(string)
	if kind == "leaf" {
		return 1
	}
	children, ok := items[1].([]any)
	if !ok || len(children) == 0 {
		return 1
	}
	counts := make([]int, 0, len(children))
	for _, child := range children {
		counts = append(counts, eqAxisCount(child, axis))
	}
	layoutAxis := "y"
	if kind == "row" {
		layoutAxis = "x"
	}
	if layoutAxis == axis {
		total := 0
		for _, c := range counts {
			total += c
		}
		return total
	}
	m := 1
	for _, c := range counts {
		m = max(m, c)
	}
	return m
}

func eqTmuxAxis(kind byte) string {
	if kind == '{' {
		return "x"
	}
	return "y"
}

// eqWeight is a subtree's visual weight along an axis: how many visible
// columns/rows it shows, nvim splits included.
func eqWeight(n *lnode, axis string, counts map[string]map[string]int) int {
	if n.kind == 'l' {
		if pc, ok := counts[n.pane]; ok {
			if c, ok := pc[axis]; ok && c > 0 {
				return c
			}
		}
		return 1
	}
	weights := make([]int, 0, len(n.kids))
	for _, kid := range n.kids {
		weights = append(weights, eqWeight(kid, axis, counts))
	}
	if eqTmuxAxis(n.kind) == axis {
		total := 0
		for _, w := range weights {
			total += w
		}
		return max(total, 1)
	}
	m := 1
	for _, w := range weights {
		m = max(m, w)
	}
	return m
}

// eqAllocate splits total cells across weights, largest-remainder rounded,
// every share at least 1.
func eqAllocate(total int, weights []int) []int {
	if len(weights) == 0 {
		return nil
	}
	total = max(total, len(weights))
	weightSum := 0
	for _, w := range weights {
		weightSum += w
	}
	if weightSum == 0 {
		weightSum = len(weights)
	}
	raw := make([]float64, len(weights))
	sizes := make([]int, len(weights))
	for i, w := range weights {
		raw[i] = float64(total) * float64(w) / float64(weightSum)
		sizes[i] = max(1, int(math.Floor(raw[i])))
	}
	sum := func() int {
		s := 0
		for _, v := range sizes {
			s += v
		}
		return s
	}
	for sum() > total {
		idx := 0
		for i := range sizes {
			if sizes[i] > sizes[idx] {
				idx = i
			}
		}
		if sizes[idx] == 1 {
			break
		}
		sizes[idx]--
	}
	for remaining := total - sum(); remaining > 0; remaining-- {
		idx, best := 0, raw[0]-math.Floor(raw[0])
		for i := range raw {
			rem := raw[i] - math.Floor(raw[i])
			if rem > best || (rem == best && sizes[i] < sizes[idx]) {
				best, idx = rem, i
			}
		}
		sizes[idx]++
		raw[idx] = math.Floor(raw[idx])
	}
	return sizes
}

// eqAssign recomputes a subtree's geometry: children share the axis
// weighted, the other dimension passes through.
func eqAssign(n *lnode, x, y, w, h int, counts map[string]map[string]int) {
	n.x, n.y, n.w, n.h = x, y, w, h
	if n.kind == 'l' {
		return
	}
	seps := len(n.kids) - 1
	if n.kind == '{' {
		weights := make([]int, 0, len(n.kids))
		for _, kid := range n.kids {
			weights = append(weights, eqWeight(kid, "x", counts))
		}
		widths := eqAllocate(w-seps, weights)
		cx := x
		for i, kid := range n.kids {
			eqAssign(kid, cx, y, widths[i], h, counts)
			cx += widths[i] + 1
		}
		return
	}
	weights := make([]int, 0, len(n.kids))
	for _, kid := range n.kids {
		weights = append(weights, eqWeight(kid, "y", counts))
	}
	heights := eqAllocate(h-seps, weights)
	cy := y
	for i, kid := range n.kids {
		eqAssign(kid, x, cy, w, heights[i], counts)
		cy += heights[i] + 1
	}
}

// eqDocked equalizes everything EXCEPT the fixed sidebar pane, which must be
// a direct child of the root row (winch docks with join-pane -hb -f).
// Targets are computed by the normal weighted assign over the main region,
// then applied as absolute per-leaf resize-pane calls in geometric order —
// once every boundary left of / above a leaf is settled, setting the leaf's
// width/height lands its own boundary exactly. One tmux invocation, so the
// server coalesces the redraw.
func eqDocked(t eqTmux, root *lnode, fixed, window string, counts map[string]map[string]int) bool {
	if root.kind != '{' {
		return false
	}
	idx := -1
	for i, kid := range root.kids {
		if kid.kind == 'l' && kid.pane == fixed {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	mains := make([]*lnode, 0, len(root.kids)-1)
	for i, kid := range root.kids {
		if i != idx {
			mains = append(mains, kid)
		}
	}
	if len(mains) == 0 {
		return false
	}
	left := mains[0].x
	width := len(mains) - 1 // separators
	for _, kid := range mains {
		if kid.x < left {
			left = kid.x
		}
		width += kid.w
	}
	pseudo := &lnode{kind: '{', kids: mains}
	eqAssign(pseudo, left, root.y, width, root.h, counts)

	args := []string{"set-option", "-w", "-t", window, "@winch_layout_dirty", "1"}
	add := func(cmd ...string) {
		args = append(args, ";")
		args = append(args, cmd...)
	}
	eqLeafResizes(pseudo, root.x+root.w, root.y+root.h, add)
	return t.ok(args...)
}
