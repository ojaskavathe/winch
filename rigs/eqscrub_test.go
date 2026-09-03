package rigs

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var scrubTargetRe = regexp.MustCompile(`scrub start from=\S+ target=(@\d+)`)

// scrubTarget reads the window the daemon last began scrubbing, from its log.
// Nothing switches to it (the sidebar zooms in place over the origin), so the
// target is not readable from tmux pane geometry — the log is the readout.
func scrubTarget(r *Rig) string {
	b, err := os.ReadFile(r.Sock + ".log")
	if err != nil {
		return ""
	}
	m := scrubTargetRe.FindAllStringSubmatch(string(b), -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

// winMains lists a window's non-winch panes as [pane, left, width].
func winMains(r *Rig, win string) [][3]int {
	var out [][3]int
	for _, ln := range strings.Split(r.T("list-panes", "-t", win, "-F",
		"#{pane_id} #{pane_left} #{pane_width} #{pane_current_command}"), "\n") {
		f := strings.Fields(ln)
		if len(f) != 4 || strings.Contains(f[3], "winch") {
			continue
		}
		id, _ := strconv.Atoi(strings.TrimPrefix(f[0], "%"))
		left, _ := strconv.Atoi(f[1])
		w, _ := strconv.Atoi(f[2])
		out = append(out, [3]int{id, left, w})
	}
	return out
}

// TestEqualizeDockTargetsScrubSelection: prefix-e / M-e while scrubbing must
// equalize the SELECTED window (the one billboarded), not the sidebar's host
// window. The sidebar is a pane like any other, so the keystroke resolves to
// the docked origin; routing through the daemon retargets it to the scrub
// selection. Reported symptom: equalize "took me back to the original
// session" — the standalone tool equalized the origin and snapped focus home.
//
// The discriminator is target selection: a dockEqualize that used p.win (the
// origin) instead of scrubWin would leave the selection's panes unbalanced and
// touch the origin — both asserted against here.
func TestEqualizeDockTargetsScrubSelection(t *testing.T) {
	r := New(t)

	// Dock in W1, the origin.
	r.T("select-window", "-t", r.W1)
	r.D("toggle", r.CL)
	sleep(600)
	r.WaitUntil(2000, func() bool { return r.Side().Win == r.W1 })
	r.Chk("docked in w1", r.Side().Win == r.W1)

	// Scroll the selection off the origin row: the selection leaving the real
	// window starts a scrub and zooms the sidebar full-width.
	sp := r.Side().Pane
	scrubAway(r, sp)
	r.await(5000, "scrubbing", func() bool { return r.Side().Width == r.prof.cols })

	target := scrubTarget(r)
	r.Chk("scrub target known from log", target != "")
	r.Chk("scrub target is not the origin", target != "" && target != r.W1)
	if target == "" || target == r.W1 {
		t.Fatalf("scrub target unusable: %q (origin %s)", target, r.W1)
	}

	// Give the target two clearly unequal panes so "equalized" is observable.
	// The target isn't zoomed (only the origin's sidebar is), so resizing it is
	// independent of the scrub.
	if len(winMains(r, target)) < 2 {
		r.T("split-window", "-h", "-t", target, "while :; do sleep 2; done")
		sleep(300)
	}
	tm := winMains(r, target)
	if len(tm) < 2 {
		t.Fatalf("target %s needs 2 panes, got %d", target, len(tm))
	}
	r.T("resize-pane", "-t", "%"+strconv.Itoa(tm[0][0]), "-x", "20")
	sleep(300)
	tm = winMains(r, target)
	r.Chk("target panes unequal before", abs(tm[0][2]-tm[1][2]) > 4)

	// Origin's own layout, to prove equalize does not touch it.
	lw1 := r.T("display-message", "-p", "-t", r.W1, "#{window_layout}")

	// The routed equalize.
	r.D("equalize-dock", r.CL)
	sleep(600)

	tm = winMains(r, target)
	r.Chk("scrub selection equalized", len(tm) == 2 && abs(tm[0][2]-tm[1][2]) <= 1)
	r.Chk("origin window untouched", r.T("display-message", "-p", "-t", r.W1, "#{window_layout}") == lw1)
	r.Chk("still scrubbing (no snap home)", r.Side().Width == r.prof.cols)

	// Land back home and undock cleanly.
	r.SendKeys(r.Side().Pane, "q")
	r.await(5000, "unzoomed", func() bool { return r.Side().Width != r.prof.cols })
	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}
