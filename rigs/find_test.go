package rigs

import (
	"strings"
	"testing"
)

// TestFindEntersFilter: `winch find` (M-/) is the one-shot launcher — from no
// dock it docks AND enters the fuzzy finder, so the query field is up without a
// separate toggle. Typing narrows the flat session+agent list; a non-matching
// session drops out. Exercises the pendingFilter -> hello-list replay path (the
// TUI is not subscribed when findOpen runs, so the enter-filter push waits for
// its first paint).
//
// It also guards the "opening the finder must not yank me to another session"
// bug: an empty query holds the session you are IN, so the view does not scrub
// onto the top-ranked match before you have typed. The rig pins order
// [play, work], so with the client on "work" the top row is a DIFFERENT session
// — the pre-fix code snapped to it and zoomed its billboard.
func TestFindEntersFilter(t *testing.T) {
	r := New(t)

	// Put the client on "work" — NOT the top-ordered session (play), so a stray
	// snap-to-top on entry would visibly jump/zoom onto play.
	r.T("switch-client", "-c", r.CL, "-t", "work")
	sleep(300)

	// The launcher, from a cold start (no dock yet).
	r.D("find", r.CL)
	sleep(900)
	sp := r.Side().Pane

	r.Chk("find docked straight into filter mode", r.WaitUntil(3000, func() bool {
		c := r.Capture(sp)
		return strings.Contains(c, "find") && strings.Contains(c, "/") &&
			strings.Contains(c, "play") && strings.Contains(c, "work")
	}))

	// The fix: an empty query holds the current session, so nothing scrubs —
	// the sidebar pane is NOT zoomed. Pre-fix it snapped to play and zoomed.
	r.Chk("empty query does not scrub-jump to another session", r.WaitUntil(2000, func() bool {
		z := strings.TrimSpace(r.T("display-message", "-p", "-t", sp, "#{window_zoomed_flag}"))
		return z == "0"
	}))

	// Type a query that only "play" matches — "work" has no p/l/a/y subsequence,
	// so it must drop out of the flat list.
	r.SendKeys(sp, "p", "l", "a", "y")
	r.Chk("query narrows to the match, drops the non-match", r.WaitUntil(3000, func() bool {
		c := r.Capture(sp)
		return strings.Contains(c, "play") && !strings.Contains(c, "work")
	}))

	// esc backs out of the filter; the full list returns.
	r.SendKeys(sp, "Escape")
	r.Chk("esc restores the full list", r.WaitUntil(3000, func() bool {
		c := r.Capture(sp)
		return strings.Contains(c, "work") && strings.Contains(c, "play")
	}))

	r.Undock()
	sleep(500)
}
