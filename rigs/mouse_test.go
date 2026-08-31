package rigs

import (
	"strconv"
	"strings"
	"testing"
)

// TestMouse: wheel scrubs, click selects, click-again enters, and a click
// on a billboard split enters that window with the clicked split focused.
func TestMouse(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL) // dock on beta; rows [head, gap, play, gap, work]
	sleep(800)
	sp := r.Side().Pane

	// rowY finds a list row by its label text (list column only — the
	// canvas can echo session names in prompts). Git rows shift positions
	// depending on the checkout, so coordinates are discovered, not fixed.
	rowY := func(sub string) int {
		for i, l := range strings.Split(r.Capture(sp), "\n") {
			rs := []rune(l)
			if len(rs) > sideW {
				rs = rs[:sideW]
			}
			if strings.Contains(string(rs), sub) {
				return i + 1
			}
		}
		return -1
	}

	// wheel up = k: selection to the play row, zoom + billboard
	r.Mouse(sp, 64, 5, 5, true)
	sleep(800)
	r.Chk("wheel scrubs (zoom)", r.Side().Width == 200)
	r.Chk("wheel billboards another session", r.ClientWin() == r.W2)

	// click the work row: selects; second click enters its pick (the dock
	// origin, beta)
	wy := rowY("work")
	r.Chk("work row located", wy > 0)
	r.Click(sp, 5, wy)
	sleep(700)
	r.Chk("click keeps scrubbing", r.ClientWin() == r.W2)
	r.Click(sp, 5, wy)
	r.WaitUntil(1500, func() bool { return r.ClientWin() == r.W2 })
	sleep(500)
	r.Chk("click-click enters the pick", r.ClientWin() == r.W2)
	r.Chk("sidebar docked in beta", r.Side().Win == r.W2 && r.Side().Width == sideW)

	// canvas click: page to w1's billboard, click inside the RIGHT
	// split -> enters w1 with that split focused (not the last-active one)
	sp = r.Side().Pane
	r.T("select-pane", "-t", sp)
	r.SendKeys(sp, "h") // work's pick w2 -> w1
	sleep(900)
	r.Chk("billboard shows w1", strings.Contains(r.Capture(sp), "MARKW1"))
	r.Click(sp, 160, 10)
	r.WaitUntil(1500, func() bool { return r.ClientWin() == r.W1 })
	sleep(500)
	r.Chk("canvas click enters w1", r.ClientWin() == r.W1)
	right := ""
	for _, ln := range strings.Split(r.T("list-panes", "-t", r.W1, "-F", "#{pane_id} #{pane_left}"), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		if n, _ := strconv.Atoi(f[1]); n > 100 {
			right = f[0]
		}
	}
	active := r.T("display-message", "-p", "-t", r.W1, "#{pane_id}")
	t.Logf("right=%s active=%s", right, active)
	r.Chk("canvas click focuses the clicked split", right != "" && active == right)
}
