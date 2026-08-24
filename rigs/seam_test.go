package rigs

import (
	"strings"
	"testing"
)

// TestSeamIsOneColour: the sidebar's edge must be ONE colour — the whole pane
// border down the side, plus the glyph continuing it into the status bar. Two
// separate tmux behaviours broke that:
//
//   - pane-border-indicators (default `colour`) paints only HALF the border in
//     a window with exactly two panes and flips which half as focus moves.
//     Docked, two panes is the common shape, so the edge came out half in one
//     colour and half in the other, and no single colour for the glyph could
//     agree with both halves.
//   - the divider is the sidebar's OWN right border, so tmux dims all of it
//     whenever the sidebar is not the focused pane — which, after any commit,
//     is always. A 49-row dim line still reads as a border; the one dim cell
//     in the status row reads as no seam at all.
//
// Measured on the raw client stream at 40x12 (small enough that one redraw is
// unambiguous): stock draws rows 1-6 in one colour and 7-11 in the other,
// inverting with focus; with the indicator off the same divider draws in a
// single colour, and stays single past two panes.
//
// The assertions here are on the OPTIONS rather than on pixels: at the sizes
// the rest of the suite runs at, one refresh-client yields several overlapping
// redraws and the colour counts stop being readable. What must not regress is
// that the daemon sets both mechanisms and gives them back.
func TestSeamIsOneColour(t *testing.T) {
	r := New(t)

	before := strings.TrimSpace(r.ShowOpt("-w", "-t", r.W2, "-v", "pane-border-indicators"))
	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	side := r.Side()
	sleep(600)

	r.Chk("indicator off on the docked window",
		strings.TrimSpace(r.ShowOpt("-w", "-t", side.Win, "-v", "pane-border-indicators")) == "off")

	pinB := strings.TrimSpace(r.ShowOpt("-p", "-t", side.Pane, "-v", "pane-border-style"))
	pinA := strings.TrimSpace(r.ShowOpt("-p", "-t", side.Pane, "-v", "pane-active-border-style"))
	t.Logf("  sidebar pane pinned: border=%q active=%q", pinB, pinA)
	r.Chk("both border styles pinned on the sidebar pane", pinB != "" && pinB == pinA)

	// The pin is what keeps the edge from dimming, so it has to survive the
	// sidebar moving between windows — pane options ride the pane, but only if
	// nothing re-creates it.
	r.T("select-window", "-t", r.W3)
	r.await(5000, "followed", func() bool { return r.Side().Win == r.W3 })
	sleep(600)
	r.Chk("indicator off on the window it moved to",
		strings.TrimSpace(r.ShowOpt("-w", "-t", r.W3, "-v", "pane-border-indicators")) == "off")
	r.Chk("pin still on the sidebar pane after the move",
		strings.TrimSpace(r.ShowOpt("-p", "-t", r.Side().Pane, "-v", "pane-border-style")) == pinB)

	r.D("toggle", r.CL)
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
	sleep(500)

	// It is the user's option: every window the sidebar visited gets it back.
	for _, w := range []string{r.W2, r.W3} {
		got := strings.TrimSpace(r.ShowOpt("-w", "-t", w, "-v", "pane-border-indicators"))
		r.Chk("indicator restored on "+w, got == before)
		if got != before {
			t.Logf("  %s left at %q, was %q", w, got, before)
		}
	}
}
