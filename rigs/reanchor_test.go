package rigs

import (
	"regexp"
	"strings"
	"testing"
)

// TestAgentSwitcherReanchorsWhenAlreadyDocked: M-a from an agent must land on
// THAT agent's row whether it is opening the sidebar or merely focusing one
// that is already up.
//
// Already-docked M-a used to hand straight to M-s, which focuses without
// touching the selection — so with the sidebar opened earlier on a session
// row, M-a from inside an agent focused the session row. Anchoring is the only
// thing that distinguishes M-a from M-s; skipping it in the docked case left
// the key doing nothing of its own precisely when the sidebar was already
// there, which is most of the time.
func TestAgentSwitcherReanchorsWhenAlreadyDocked(t *testing.T) {
	r := New(t)
	fake := buildFakeAgent(t)

	// Two agents in gamma so the anchor and the ranking can disagree: the
	// test parks on the WORKING one and demands it over the blocked one.
	ap := r.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", r.W3, fake+" 100000")
	sleep(1700)
	r.T("select-pane", "-T", "⠧ Cooking again", "-t", ap)
	// Blocked comes from the SCREEN tier, not the title — a permission prompt
	// on screen is what the manifest matches.
	bp := r.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", r.W3,
		"sh -c 'printf \"  Do you want to proceed?\\n❯ 1. Yes\\n  2. No, and tell Claude what to do differently (esc)\\n\"; exec "+fake+" 100000'")
	r.await(8000, "both agents detected", func() bool {
		return r.LogHas("agent claude pane=.* state=.*->working") &&
			r.LogHas("agent claude pane=.* state=.*->blocked")
	})

	esc := regexp.MustCompile("\x1b\\[[0-9;:]*m")
	filled := func(sub string) bool {
		raw, _ := r.TQ("capture-pane", "-p", "-e", "-t", r.Side().Pane)
		for _, ln := range strings.Split(raw, "\n") {
			if !strings.Contains(esc.ReplaceAllString(ln, ""), sub) {
				continue
			}
			if strings.Contains(ln, "49;50;68") || strings.Contains(ln, "49:50:68") {
				return true
			}
		}
		return false
	}

	// Open with M-s, which selects the docked WINDOW — not an agent. This is
	// "opened through session selection": the state the sidebar is in most of
	// the time, because M-s is how it usually gets opened.
	r.T("select-window", "-t", r.W3)
	r.T("select-pane", "-t", ap)
	r.D("toggle", r.CL)
	r.await(5000, "docked via M-s", func() bool { return r.Side().Pane != "" })
	sleep(800)
	r.Chk("M-s did not anchor on the working agent", !filled("Cooking again"))

	// Put the keyboard back in the agent pane — M-s may have left it in the
	// sidebar, and M-a from the sidebar is the CLOSING press, not this case.
	r.T("select-pane", "-t", ap)
	r.await(3000, "keyboard back in the agent", func() bool { return r.ClientPane() == ap })

	// The press under test.
	r.D("agents", r.CL)
	r.Chk("M-a anchors on the agent you are in", r.WaitUntil(3000, func() bool {
		return filled("Cooking again")
	}))
	r.Chk("and it does not pick the higher-ranked blocked one", !filled("permission prompt"))
	r.Chk("and it focuses the sidebar", r.WaitUntil(3000, func() bool {
		return r.ClientPane() == r.Side().Pane
	}))
	r.Chk("without moving the client off its window", r.ClientWin() == r.W3)
	r.Chk("and without zooming", !r.Zoomed(r.Side().Win))

	// From inside the sidebar M-a still closes — the contextual half must
	// survive the re-anchoring.
	r.D("agents", r.CL)
	r.Chk("M-a from inside the sidebar closes it", r.WaitUntil(3000, func() bool {
		return r.WinchPanes("-a") == 0
	}))

	// Re-anchoring must follow the client, not remember its last answer: park
	// on the BLOCKED agent and M-a has to move to it.
	r.T("select-pane", "-t", bp)
	r.D("toggle", r.CL)
	r.await(5000, "docked again", func() bool { return r.Side().Pane != "" })
	r.T("select-pane", "-t", bp)
	r.await(3000, "keyboard in the blocked agent", func() bool { return r.ClientPane() == bp })
	r.D("agents", r.CL)
	r.Chk("M-a follows the client to the other agent", r.WaitUntil(3000, func() bool {
		return filled("permission prompt")
	}))
	r.Chk("and left the first agent's row unselected", !filled("Cooking again"))

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}
