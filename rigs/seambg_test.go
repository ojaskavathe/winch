package rigs

import (
	"strings"
	"testing"
)

// TestSeamGroundMatchesBorder: the seam glyph and the border cell directly
// below it must sit on the SAME background.
//
// This is the assertion that was missing, and its absence hid the bug for
// days. Every earlier rig compared foregrounds — and the foregrounds always
// matched, because the daemon pins both to one colour on purpose. What nobody
// compared was the ground underneath.
//
// tmux draws a pane border with `pane-border-style`, which sets no background,
// so the cell falls through to the TERMINAL's. The pad, meanwhile, opens with
// the sidebar's own ground — a step darker than the terminal by design — and
// padCell used to force that ground onto the glyph too, on the reasoning that
// the cell "belongs to the pad". It does not: that column is the border
// column. On catppuccin the two grounds are #181825 and #1e1e2e, so the corner
// carried one cell of discontinuity in every focus state, which is why nothing
// about active-vs-inactive ever explained it.
//
// Stated as an equality between two measured cells rather than against an
// expected colour, so the test cannot inherit whatever mistaken idea of the
// right answer the code has.
func TestSeamGroundMatchesBorder(t *testing.T) {
	r := New(t)

	// Bar at the top, as in the real config, so the status row is row 0 and the
	// border it continues is row 1 — adjacent, and both in one capture.
	r.T("set-option", "-g", "status-position", "top")
	// A terminal ground that is NOT the sidebar's, which is the whole point.
	// Without this the rig's default palette can make the bug invisible.
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
	sleep(600)

	content := ""
	for _, ln := range strings.Split(r.T("list-panes", "-t", r.Side().Win, "-F", "#{pane_id} #{pane_left}"), "\n") {
		if f := strings.Fields(ln); len(f) == 2 && f[1] != "0" {
			content = f[0]
		}
	}
	r.Chk("found the content pane", content != "")

	// Accumulated rather than re-sampled: one redraw does not always repaint
	// the border column, and a blank there is the model's problem, not the
	// daemon's. Waiting for both cells to carry the glyph cannot mask a
	// mismatch — a wrong ground stays wrong.
	//
	// "never painted" is reported separately from "painted wrong". Conflating
	// them is what made the old foreground-only rig flake under parallel
	// load: a redraw that had not reached the border column yet read as a
	// colour bug. statusScreenUntil is what makes the distinction honest —
	// re-sampling into a FRESH screen used to discard the frame that carried
	// the cell, so patience alone never fixed this.
	type cell struct{ fg, bg string }
	painted := func(s *screen) bool { return s.grid[0][w] == '│' && s.grid[1][w] == '│' }
	seam := func(what string) (glyph, border cell, ok bool) {
		s := statusScreenUntil(r, painted)
		g := cell{s.fg[0][w], s.bg[0][w]}
		b := cell{s.fg[1][w], s.bg[1][w]}
		t.Logf("  %s: glyph=%q fg=%q bg=%q | border=%q fg=%q bg=%q painted=%v",
			what, s.grid[0][w], g.fg, g.bg, s.grid[1][w], b.fg, b.bg, painted(s))
		if !painted(s) {
			return cell{}, cell{}, false
		}
		return g, b, true
	}

	r.T("select-pane", "-t", sp)
	sleep(700)
	g, b, ok := seam("sidebar focused")
	r.Chk("both cells painted, sidebar focused", ok)
	r.Chk("seam matches the border's colour, sidebar focused", ok && g.fg == b.fg)
	r.Chk("seam sits on the border's ground, sidebar focused", ok && g.bg == b.bg)

	r.T("select-pane", "-t", content)
	sleep(700)
	g2, b2, ok2 := seam("content focused")
	r.Chk("both cells painted, content focused", ok2)
	r.Chk("seam matches the border's colour, content focused", ok2 && g2.fg == b2.fg)
	r.Chk("seam sits on the border's ground, content focused", ok2 && g2.bg == b2.bg)

	// The whole edge holds ONE colour through focus changes — the point of the
	// per-pane pin plus pane-border-indicators off. A dim cell in the status
	// row reads as no seam at all.
	r.Chk("the seam is the same colour whatever has focus", ok && ok2 && g.fg == g2.fg)

	// And the corner joins up HORIZONTALLY too: the strip, the glyph and the
	// border below now share one ground.
	//
	// This used to demand the OPPOSITE — pad ground != border ground — because
	// the border was whatever the terminal painted and the glyph was matched to
	// it, which left the glyph one lighter column between the strip and the
	// status bar. It read as a gap beside the first window cell. Both borders
	// are now grounded at window scope (desiredOpts), so there is nothing left
	// to trade and the old assertion was pinning the trade in place.
	s := statusScreenUntil(r, func(s *screen) bool { return s.bg[0][0] != "" && s.bg[0][w] != "" })
	r.Chk("the strip sits on the sidebar's ground", s.bg[0][0] != "")
	r.Chk("and the seam glyph sits on that SAME ground — no gap beside the bar",
		s.bg[0][0] != "" && s.bg[0][0] == s.bg[0][w])
	if s.bg[0][0] != s.bg[0][w] {
		t.Logf("  strip %q vs glyph %q — one column of discontinuity", s.bg[0][0], s.bg[0][w])
	}

	r.T("set-option", "-g", "status-position", "bottom")
	r.Undock() // the keyboard is on the content pane; M-s there focuses first
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}
