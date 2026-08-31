package rigs

import (
	"strings"
	"testing"
)

// TestTheAgentYouAreInIsFilled: the agent card for the pane the client is
// actually sitting in carries a background the other cards do not.
//
// herdr hangs its whole ladder off is_active_pane (src/ui/sidebar.rs:1503);
// without it, five agents render identically and the list answers "who is
// blocked" but never "where am I". winch derives the same thing from tmux's
// own flags, which makes one case load-bearing and easy to get wrong: the
// sidebar is a REAL PANE, so opening it can move tmux's focus onto winch, and
// a live reading of pane_active would blank the fill at exactly the moment
// you are looking at the list. Hence a sticky value — and hence a rig, since
// the stickiness only matters against a real docked sidebar.
//
// Stated as a comparison between measured cells rather than against an
// expected colour, so the test cannot inherit the code's idea of the answer.
// The second agent is not decoration: it is what proves the fill is a
// statement about location and not something every agent row gets.
func TestTheAgentYouAreInIsFilled(t *testing.T) {
	r := New(t)
	fake := buildFakeAgent(t)

	mine := r.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", r.W2, fake+" 100000")
	other := r.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "play:", fake+" 100000")
	sleep(1700)
	r.T("select-pane", "-T", "⠧ Mine", "-t", mine)
	r.T("select-pane", "-T", "⠧ Theirs", "-t", other)

	// The client sits IN its agent. That is the fact the fill claims, so the
	// test has to establish it before docking rather than hope for it.
	r.T("select-pane", "-t", mine)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	w := r.Side().Width
	r.await(8000, "both agents listed", func() bool {
		return strings.Count(r.Capture(sp), "claude") >= 2
	})
	// Docking hands winch the focus, which is the whole difficulty: the agent
	// the user was in is active NOWHERE by the time the sidebar first paints.
	// Logged because a future change to who holds focus after a dock would
	// break this feature silently otherwise.
	t.Logf("  after dock: mine=%s sidebar=%s | %s", mine, sp,
		strings.ReplaceAll(r.T("list-panes", "-t", r.W2, "-F", "#{pane_id} act=#{pane_active} last=#{pane_last}"), "\n", " | "))

	// The agents rule is the region boundary: session cards above it carry
	// "work" and "play" too, and grabbing one of those would measure the
	// session fill instead — a different feature that happens to look the
	// same.
	var ruleY, mineY, otherY int
	find := func(s *screen) bool {
		ruleY, mineY, otherY = -1, -1, -1
		for y := 0; y < len(s.grid); y++ {
			line := string(s.grid[y][:min(w, len(s.grid[y]))])
			switch {
			case ruleY < 0 && strings.Contains(line, "agents"):
				ruleY = y
			case ruleY >= 0 && mineY < 0 && strings.Contains(line, "work"):
				mineY = y
			case ruleY >= 0 && otherY < 0 && strings.Contains(line, "play"):
				otherY = y
			}
		}
		return ruleY >= 0 && mineY >= 0 && otherY >= 0
	}
	s := statusScreenUntil(r, find)
	t.Logf("  rule=%d mine=%d other=%d", ruleY, mineY, otherY)
	r.Chk("found both agent cards under the rule", find(s))
	if !find(s) {
		t.Logf("  sidebar:\n%s", r.Capture(sp))
		return
	}

	// The rule is drawn on the sidebar's own ground and nothing else, so it
	// is the reference for "unfilled" — no hardcoded colour required.
	const col = 4
	ground := s.bg[ruleY][col]
	mineBG, otherBG := s.bg[mineY][col], s.bg[otherY][col]
	t.Logf("  ground=%q mine=%q other=%q", ground, mineBG, otherBG)

	r.Chk("the agent you are in is filled", mineBG != ground)
	r.Chk("the agent you are not in is not", otherBG == ground)

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}
