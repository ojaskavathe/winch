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
func TestFindEntersFilter(t *testing.T) {
	r := New(t)

	// The launcher, from a cold start (no dock yet).
	r.D("find", r.CL)
	sleep(900)
	sp := r.Side().Pane

	r.Chk("find docked straight into filter mode", r.WaitUntil(3000, func() bool {
		c := r.Capture(sp)
		// The filter view: its " find" heading and the "/" query field, with
		// both sessions still listed under an empty query.
		return strings.Contains(c, "find") && strings.Contains(c, "/") &&
			strings.Contains(c, "play") && strings.Contains(c, "work")
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
