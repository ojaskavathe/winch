package rigs

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestRespawnNoSelectionJump: committing onto the window the sidebar is
// ALREADY docked in (Enter after scrubbing away and back, or clicking a
// split of that same window) takes scrubEnd's respawn path — the TUI
// process is replaced so the zoomed grid can't rewrap into the narrow
// strip. The fresh TUI must not paint the list until the daemon replays the
// selection: painting the snapshot first highlights row one for a few ms and
// then jumps to the real row, which reads as a flick of the selection bar on
// every such commit.
func TestRespawnNoSelectionJump(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	sleep(600)

	// Scrub away and back so the selection ends where it started; Enter then
	// commits onto the docked window itself. Which direction leaves the
	// origin row depends on where the dock sits in the list — at an edge one
	// of them silently no-ops.
	away, back := scrubAway(r, sp)
	r.Chk("scrubbing after moving off the origin", r.Side().Width == 200)
	r.SendKeys(sp, back)
	sleep(700)

	mark := benchSize(r)
	r.SendKeys(sp, "Enter")
	sleep(1500)
	sels := selectionsPainted(r, mark)
	r.Chk("enter-home paints one selection, no jump", len(sels) == 1)
	if len(sels) != 1 {
		t.Logf("selections painted: %q", sels)
	}
	r.Chk("respawn happened (the path under test)", r.LogHas("unzoom=respawn"))

	// The same path via a billboard click: scrub away, then click a split of
	// the ORIGIN window's billboard to commit back into it.
	sp = r.Side().Pane
	_, back = scrubAway(r, sp)
	r.SendKeys(sp, back)
	sleep(700)
	mark = benchSize(r)
	_ = away
	r.Click(sp, sideW+20, 6) // into the canvas: commits that billboard split
	sleep(1500)
	sels = selectionsPainted(r, mark)
	r.Chk("billboard-click commit paints one selection", len(sels) <= 1)
	if len(sels) > 1 {
		t.Logf("selections painted: %q", sels)
	}

	r.D("toggle", r.CL)
	sleep(800)
}

// scrubAway moves the selection off the origin row, whichever direction
// actually leaves it (at a list edge one of them no-ops), and returns the
// key that moved and the key that comes back.
func scrubAway(r *Rig, sp string) (away, back string) {
	away, back = "j", "k"
	r.SendKeys(sp, away)
	sleep(600)
	if r.Side().Width != 200 { // no scrub: that direction was the edge
		away, back = "k", "j"
		r.SendKeys(sp, away)
		sleep(600)
	}
	return away, back
}

func benchSize(r *Rig) int64 {
	fi, err := os.Stat(r.Sock + ".tui-bench.log")
	if err != nil {
		return 0
	}
	return fi.Size()
}

var selRowRe = regexp.MustCompile(`tui list row \d+: ".*" -> ">(.*)"`)

// selectionsPainted returns the sequence of rows that became selected in the
// TUI bench log after byte offset off, collapsing repeats.
func selectionsPainted(r *Rig, off int64) []string {
	b, err := os.ReadFile(r.Sock + ".tui-bench.log")
	if err != nil || int64(len(b)) <= off {
		return nil
	}
	var out []string
	for _, m := range selRowRe.FindAllStringSubmatch(string(b[off:]), -1) {
		s := strings.TrimSpace(m[1])
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}
