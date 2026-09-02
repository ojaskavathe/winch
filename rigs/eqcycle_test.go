package rigs

import (
	"strings"
	"testing"
)

// TestEqualizeCycle pins the open/close round trip: open, prefix-e, close,
// open must land back on the SAME equalized geometry (dockOpen asserts the
// scaleX widths, the exact inverse of the give-back rescale), and a second
// prefix-e plus close must change nothing. Before the width assertion,
// tmux's own split-time redistribution came back lopsided and every cycle
// shifted the layout slightly.
func TestEqualizeCycle(t *testing.T) {
	r := NewLive(t)
	// five uneven vertical panes in play:p1 (remainder-y at 480 cols)
	for i := 0; i < 4; i++ {
		r.T("split-window", "-h", "-t", "play:0")
	}
	r.T("resize-pane", "-t", "play:0.0", "-x", "40")
	r.T("switch-client", "-c", r.CL, "-t", "play", ";", "select-window", "-t", r.P1)
	sleep(300)
	lay := func() string { return r.T("display-message", "-p", "-t", "play:0", "#{window_layout}") }
	a0 := lay()

	r.D("toggle", r.CL) // open
	sleep(500)
	r.D("equalize")
	sleep(400)
	e1 := lay()
	r.D("toggle", r.CL) // close (focus is in sidebar? toggle from content pane focuses sidebar first)
	sleep(300)
	if r.Side().Pane != "" {
		r.D("toggle", r.CL)
		sleep(300)
	}
	a1 := lay()

	r.D("toggle", r.CL) // open again
	sleep(500)
	preE2 := lay()
	r.D("equalize")
	sleep(400)
	e2 := lay()
	r.D("toggle", r.CL)
	sleep(300)
	if r.Side().Pane != "" {
		r.D("toggle", r.CL)
		sleep(300)
	}
	a2 := lay()

	t.Logf("a0    = %s", a0)
	t.Logf("e1    = %s", e1)
	t.Logf("a1    = %s", a1)
	t.Logf("preE2 = %s", preE2)
	t.Logf("e2    = %s", e2)
	t.Logf("a2    = %s", a2)
	strip := func(s string) string {
		out := ""
		for _, part := range strings.Split(s, ",") {
			if len(part) > 0 && part[0] >= '0' && part[0] <= '9' && !strings.ContainsAny(part, "x") {
				continue
			}
			out += part + ","
		}
		return out
	}
	if strip(preE2[5:]) != strip(e1[5:]) {
		t.Errorf("reopen did not restore the equalized layout:\n e1=%s\n preE2=%s", e1, preE2)
	}
	if strip(e1[5:]) != strip(e2[5:]) {
		t.Errorf("equalized docked layout drifted:\n e1=%s\n e2=%s", e1, e2)
	}
	if strip(a1[5:]) != strip(a2[5:]) {
		t.Errorf("give-back layout drifted:\n a1=%s\n a2=%s", a1, a2)
	}
}
