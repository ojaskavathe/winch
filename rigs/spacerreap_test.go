package rigs

import (
	"strings"
	"testing"
)

// TestClosingEverySplitClosesTheWindow: a window the sidebar has visited keeps
// a spacer pane in the sidebar's slot after the sidebar moves on. That is
// deliberate — it is what makes coming back a geometry-free swap instead of a
// resize that reflows every pane's scrollback — but it also means the window no
// longer dies when the user closes their last real pane.
//
// From the user's side: you close every split in a window and the window is
// still in the status line, with a blank strip where the sidebar used to be.
// Nothing explains it, and nothing reaps it — releaseOne only runs at undock,
// which may be hours away.
//
// The spacer is winch's, so winch has to notice when it is the only thing left
// holding a window open.
func TestClosingEverySplitClosesTheWindow(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL) // sidebar lands in W2 (beta), beside beta's own pane
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(600)
	victim := r.Side().Win

	// Move the sidebar out, which leaves a spacer behind in W2.
	r.T("select-window", "-t", r.W3)
	r.await(5000, "sidebar followed", func() bool { return r.Side().Win == r.W3 })
	sleep(800)

	var spacer string
	var real []string
	for _, ln := range strings.Split(r.T("list-panes", "-t", victim, "-F", "#{pane_id} #{pane_left}"), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		if f[1] == "0" {
			spacer = f[0]
		} else {
			real = append(real, f[0])
		}
	}
	r.Chk("the window it left is spacer-held", spacer != "")
	r.Chk("and still has the user's own panes", len(real) > 0)

	// The user closes every split they own.
	for _, p := range real {
		r.T("kill-pane", "-t", p)
	}
	sleep(400)

	// No intermediate "is only the spacer left" assertion: the reaper is meant
	// to win that race, and reading the window between the two is a Fatal the
	// moment it does. The claim is about where things end up.
	alive := func() bool {
		out, err := r.TQ("list-windows", "-a", "-F", "#{window_id}")
		return err == nil && containsID(out, victim)
	}
	r.await(6000, "the window goes away", func() bool { return !alive() })
	r.Chk("window gone from the server", !alive())
	if alive() {
		out, _ := r.TQ("list-panes", "-t", victim, "-F", "#{pane_id} #{pane_current_command}")
		t.Logf("  %s survives with panes: %q", victim, out)
	}

	// And the spacer went with it rather than being orphaned into another
	// window — a leaked `sleep 100000001` is winch's most embarrassing litter.
	panes, _ := r.TQ("list-panes", "-a", "-F", "#{pane_id}")
	r.Chk("the spacer is gone too", !containsID(panes, spacer))

	r.D("toggle", r.CL)
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// containsID matches a whole tmux id in a newline-separated listing. Substring
// matching would call @1 present because @11 is.
func containsID(listing, id string) bool {
	for _, ln := range strings.Split(listing, "\n") {
		if f := strings.Fields(ln); len(f) > 0 && f[0] == id {
			return true
		}
	}
	return false
}
