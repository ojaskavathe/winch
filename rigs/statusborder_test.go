package rigs

import (
	"strings"
	"testing"
)

// TestStatusPadBorder: the status pad (dockSessionCmds) shifts the bar past
// the sidebar by width+1 invisible columns — the sidebar's own columns plus
// the pane border column. Painting that last column blank leaves the
// sidebar's │ stopping dead at the status row, and on the real setup there is
// no seam at all to make up for it: the sidebar's ground, the pad, and
// status-style's background are all #181825, so the bar simply runs across
// the top of the sidebar.
//
// So the pad's last column must carry the border glyph, continuing the border
// tmux itself draws in that same column one row over.
//
// Screen-level on purpose: the status line is not a pane, so capture-pane
// cannot see it at all. The claim is about what the client's terminal holds.
func TestStatusPadBorder(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	w := r.Side().Width
	sleep(600)

	r.StartRecord()
	r.T("refresh-client", "-t", r.CL) // no flags: redraw the whole client
	sleep(700)
	chunks := r.StopRecordT()

	s := newScreen(r.prof.rows, r.prof.cols)
	for _, c := range chunks {
		s.write(c.Data)
	}
	// The rig's tmux runs -f /dev/null, so status-position is the stock
	// default: bottom. The status row is the last one.
	st := r.prof.rows - 1
	row := string(s.grid[st])

	// Precondition. Without it every assertion below would read the model's
	// blank starting grid and report success for a redraw that never came.
	// Window names, not the session name: the pad has already replaced
	// status-left, which is the only place #S would have appeared.
	r.Chk("status row was painted by the redraw", strings.Contains(row, "beta"))

	r.Chk("pad still blank across the sidebar", strings.TrimSpace(string(s.grid[st][:w])) == "")

	below := s.grid[st-1][w] // tmux's real pane border, same column
	glyph := s.grid[st][w]
	r.Chk("pane border occupies the column the pad ends at", below == '│')
	r.Chk("status pad ends in the border glyph", glyph == '│')
	if glyph != '│' {
		t.Logf("  status row col %d is %q; the row below it holds %q", w, glyph, below)
	}

	r.D("toggle", r.CL)
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// TestStatusPadBorderZoomed: a scrub zooms the sidebar to the full width, so
// the border it was continuing no longer exists — but the pad is still W+1
// wide, because nothing re-runs dockSessionCmds on a zoom. A glyph left in
// that column lands in the middle of the sidebar's own top edge, pointing at
// nothing. Blank columns hid this; a visible character does not.
func TestStatusPadBorderZoomed(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	w := r.Side().Width
	sleep(600)

	scrubAway(r, sp)
	r.await(4000, "zoomed", func() bool { return r.Side().Width == r.prof.cols })

	r.StartRecord()
	r.T("refresh-client", "-t", r.CL)
	sleep(700)
	s := newScreen(r.prof.rows, r.prof.cols)
	for _, c := range r.StopRecordT() {
		s.write(c.Data)
	}
	st := r.prof.rows - 1
	got := s.grid[st][w]
	r.Chk("no glyph while the sidebar is zoomed over the border", got == ' ')
	if got != ' ' {
		t.Logf("  status row col %d holds %q with no border beneath it", w, got)
	}

	r.SendKeys(sp, "q")
	sleep(600)
	r.D("toggle", r.CL)
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// TestStatusPadBorderNotAdjacent: the glyph only continues the border when the
// padded row actually touches the pane area. status-left lives on
// status-format[0], and with the bar at the TOP a second status row lands
// between it and the panes — a glyph there would point at the row below it,
// not at a border. Guarded in the format, not in Go, so it must react to the
// options changing under a sidebar that is already docked.
func TestStatusPadBorderNotAdjacent(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	w := r.Side().Width
	r.T("set-option", "-g", "status-position", "top")
	sleep(700)

	glyphAt := func(row int) rune {
		r.StartRecord()
		r.T("refresh-client", "-t", r.CL)
		sleep(700)
		s := newScreen(r.prof.rows, r.prof.cols)
		for _, c := range r.StopRecordT() {
			s.write(c.Data)
		}
		return s.grid[row][w]
	}

	r.Chk("one row at the top still gets the glyph", glyphAt(0) == '│')

	r.T("set-option", "-g", "status", "2")
	sleep(700)
	got := glyphAt(0)
	r.Chk("a second row between bar and panes drops the glyph", got == ' ')
	if got != ' ' {
		t.Logf("  status row 0 col %d is %q, want a blank", w, got)
	}

	r.T("set-option", "-g", "status", "on")
	r.T("set-option", "-g", "status-position", "bottom")
	r.D("toggle", r.CL)
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}
