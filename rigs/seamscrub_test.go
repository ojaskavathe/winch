package rigs

import (
	"fmt"
	"testing"
)

// TestSeamSurvivesAScrub: scrolling the list must not take the corner with it.
//
// Two different things draw the sidebar's edge, and the pad has to follow
// whichever is on screen:
//
//   - docked idle, the TUI pane is exactly listW wide, so paintList sets
//     border=false and draws no divider of its own. tmux draws the real pane
//     border, and the pad's glyph continues that.
//   - scrubbing, the pane is ZOOMED to the whole window. tmux draws no border
//     at all — one pane has none — and paintList takes the other branch and
//     paints its own │ in pal.muted on pal.bg at the same column.
//
// padBordered suppressed the glyph on window_zoomed_flag, on the reasoning
// that a zoom means there is no border to continue. There is: it is the TUI's.
// So the first j emptied that one cell and left the edge running up to a gap.
//
// Measured against the cell BELOW it in both states, so the test never encodes
// which colour is right — only that the corner joins up.
func TestSeamSurvivesAScrub(t *testing.T) {
	r := New(t)

	r.T("set-option", "-g", "status-position", "top")
	r.T("set-option", "-g", "status-style", "bg=#181825,fg=#cdd6f4")
	r.T("set-option", "-gw", "pane-border-style", "fg=#6c7086")
	r.T("set-option", "-gw", "pane-active-border-style", "fg=#b4befe")
	r.KillDaemon()
	r.D("ls")
	sleep(300)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	w := r.Side().Width
	sp := r.Side().Pane
	sleep(700)

	// row 0 is the status row (bar at top); row 1 is the first row of the pane
	// area, which holds either tmux's border or the TUI's divider.
	joined := func(what string) bool {
		var last string
		for i := 0; i < 14; i++ {
			s := statusScreen(r)
			last = fmt.Sprintf("glyph=%q fg=%q bg=%q | below=%q fg=%q bg=%q",
				s.grid[0][w], s.fg[0][w], s.bg[0][w],
				s.grid[1][w], s.fg[1][w], s.bg[1][w])
			if s.grid[0][w] == '│' && s.grid[1][w] == '│' &&
				s.fg[0][w] == s.fg[1][w] && s.bg[0][w] == s.bg[1][w] {
				t.Logf("  %s: %s (attempt %d)", what, last, i+1)
				return true
			}
			sleep(400)
		}
		t.Logf("  %s: %s — never joined", what, last)
		return false
	}

	r.Chk("the corner joins up when docked", joined("docked idle"))

	// One step of the list: the selection leaves the real window, which starts
	// a scrub and zooms the sidebar. This is the keystroke in the report —
	// scrubAway just picks whichever of j/k is not already at the edge.
	scrubAway(r, sp)
	r.await(5000, "scrubbing", func() bool { return r.Side().Width == r.prof.cols })
	sleep(900)
	r.Chk("the corner still joins up while scrubbing", joined("scrubbing"))

	// And back: q ends the scrub, the pane unzooms, tmux's border returns, and
	// the glyph has to go back to matching THAT.
	r.SendKeys(r.Side().Pane, "q")
	r.await(5000, "unzoomed", func() bool { return r.Side().Width == w })
	sleep(900)
	r.Chk("the corner joins up again after the scrub", joined("after the scrub"))

	r.T("set-option", "-g", "status-position", "bottom")
	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}
