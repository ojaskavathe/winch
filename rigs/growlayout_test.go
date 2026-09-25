package rigs

import (
	"strings"
	"testing"
)

// layoutMismatches lists windows whose layout root disagrees with the window's
// own size — panes laid out for a size the window no longer has.
func layoutMismatches(r *Rig) []string {
	var bad []string
	for _, ln := range strings.Split(r.T("list-windows", "-a", "-F", "#{window_id} #{window_width}x#{window_height} #{window_layout}"), "\n") {
		f := strings.Fields(ln)
		if len(f) != 3 {
			continue
		}
		lay := f[2]
		if i := strings.Index(lay, ","); i >= 0 {
			lay = lay[i+1:]
		}
		root := lay
		if i := strings.Index(root, ","); i >= 0 {
			root = root[:i]
		}
		if root != f[1] {
			bad = append(bad, f[0]+" win="+f[1]+" layout="+root)
		}
	}
	return bad
}

// TestGrowWhileDocked: the client GROWS while the sidebar is docked (a
// terminal moved to a bigger monitor), the dock is toggled, and a scrub
// commits into another session — every window must stay laid out for its own
// size.
//
// Undock replayed the layout saved at dock time verbatim. tmux applies a
// layout string at the size it records, so after the grow the panes stayed
// 200x49 inside a 250x59 window: the dead margin and squashed panes of a
// session that "got resized and is cooked".
func TestGrowWhileDocked(t *testing.T) {
	r := New(t)
	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(600)

	r.ResizeClient(60, 250)
	sleep(1500)
	for i := 0; i < 3; i++ {
		r.Undock()
		sleep(400)
		bad := layoutMismatches(r)
		r.Chk("undock after the grow fits the window: "+strings.Join(bad, "; "), len(bad) == 0)
		r.D("toggle", r.CL)
		r.await(5000, "re-docked", func() bool { return r.Side().Pane != "" })
		sleep(400)
	}

	// scrub up into play and commit there: beta keeps a spacer
	sp := r.Side().Pane
	r.T("select-pane", "-t", sp)
	for i := 0; i < 4; i++ {
		r.SendKeys(sp, "k")
		sleep(300)
	}
	r.SendKeys(sp, "Enter")
	r.WaitUntil(3000, func() bool { return r.ClientSess() == "play" })
	sleep(1200)
	t.Logf("client on %s %s", r.ClientSess(), r.ClientWin())
	bad := layoutMismatches(r)
	r.Chk("every window laid out for its own size (docked): "+strings.Join(bad, "; "), len(bad) == 0)

	r.Undock()
	sleep(2500)
	bad = layoutMismatches(r)
	r.Chk("every window laid out for its own size (undocked): "+strings.Join(bad, "; "), len(bad) == 0)
}

// TestGrowReleasesHeldWindow: a window carved at the OLD size (the dock
// visited it, then moved on) is released after the grow. The release replays
// the pre-carve layout, which is just as stale as the dock-time one.
func TestGrowReleasesHeldWindow(t *testing.T) {
	r := New(t)
	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(600)
	home := r.Side().Win

	// Visit play (carving it), then come back: play keeps a spacer carved at
	// the old size.
	r.D("nav", "next", r.CL)
	sleep(500)
	sp := r.Side().Pane
	r.T("select-pane", "-t", sp)
	for i := 0; i < 4; i++ {
		r.SendKeys(sp, "k")
		sleep(300)
	}
	r.SendKeys(sp, "Enter")
	r.WaitUntil(3000, func() bool { return r.ClientSess() == "play" })
	sleep(600)
	r.T("switch-client", "-c", r.CL, "-t", home)
	r.WaitUntil(3000, func() bool { return r.Side().Win == home })
	sleep(800)
	r.Chk("a window is held by a spacer", r.Spacers() > 0)

	r.ResizeClient(60, 250)
	sleep(1500)
	r.Undock()
	r.WaitUntil(5000, func() bool { return r.Spacers() == 0 })
	sleep(800)
	bad := layoutMismatches(r)
	r.Chk("released windows fit their own size: "+strings.Join(bad, "; "), len(bad) == 0)
	// Visiting them must not be what fixes it: check before AND after.
	for _, w := range []string{r.P1, r.W1, r.W2, r.W3} {
		r.T("select-window", "-t", w)
	}
	sleep(500)
	bad = layoutMismatches(r)
	r.Chk("and still fit once visited: "+strings.Join(bad, "; "), len(bad) == 0)
}

// TestHealAtAttach: a window an older build already left laid out at the
// wrong size is repaired when the daemon (re)attaches — a deploy fixes the
// damage without the user having to find and reshape each window.
func TestHealAtAttach(t *testing.T) {
	r := New(t)
	// Make the damage exactly the way the bug did: replay a ONE-pane layout
	// recorded at the old size into a window the client has since grown. For
	// a single leaf tmux 3.7 sizes the pane to the string and leaves the
	// window alone (a multi-pane string resizes the window too, and the
	// client's resize pass then fixes both) — which is why undocking a window
	// with one real pane in it is what broke.
	lay := r.T("display-message", "-p", "-t", r.W2, "#{window_layout}")
	r.ResizeClient(60, 250)
	r.T("select-window", "-t", r.W2)
	sleep(1000)
	r.T("select-layout", "-t", r.W2, lay)
	sleep(300)
	r.Chk("damage in place", len(layoutMismatches(r)) > 0)

	r.KillDaemon()
	r.D("ls")
	sleep(500)
	bad := layoutMismatches(r)
	r.Chk("attach healed every window: "+strings.Join(bad, "; "), len(bad) == 0)
}
