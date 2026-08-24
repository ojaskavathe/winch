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

// TestStatusPadBorderStyle: tmux colours a border segment with
// pane-active-border-style only where it TOUCHES the active pane. The glyph
// must follow the same rule, or it reads as a permanently-focused edge.
//
// Two ways to fail the touch test, and both are covered: focus on a pane that
// does not border the sidebar column at all, and focus on one that does but at
// the other end of the divider — a stacked split leaves the bar-adjacent
// segment belonging to the other pane.
//
// The style is checked by re-rendering the daemon's own pad string against a
// chosen pane, because the screen model records runes and not colour. Using
// the option tmux actually holds, not a copy of the format, is what keeps this
// a test of the daemon rather than of the test.
func TestStatusPadBorderStyle(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	side := r.Side()
	sleep(600)

	// Distinguishable stand-ins for the two styles.
	r.T("set-option", "-gw", "pane-active-border-style", "fg=red")
	r.T("set-option", "-gw", "pane-border-style", "fg=blue")

	// Stack the content column: top and bottom both touch the border, but
	// with the bar at the bottom only the lower one owns the segment next
	// to it.
	content := ""
	for _, ln := range strings.Split(r.T("list-panes", "-t", side.Win, "-F", "#{pane_id} #{pane_left}"), "\n") {
		if f := strings.Fields(ln); len(f) == 2 && f[1] != "0" {
			content = f[0]
		}
	}
	r.Chk("found the content pane", content != "")
	r.T("split-window", "-v", "-t", content)
	sleep(500)

	var top, bottom string
	for _, ln := range strings.Split(r.T("list-panes", "-t", side.Win, "-F", "#{pane_id} #{pane_left} #{pane_top}"), "\n") {
		f := strings.Fields(ln)
		if len(f) != 3 || f[1] == "0" {
			continue
		}
		if f[2] == "0" {
			top = f[0]
		} else {
			bottom = f[0]
		}
	}
	r.Chk("content column is stacked", top != "" && bottom != "")

	pad := strings.TrimSpace(r.ShowOpt("-t", r.ClientSess(), "-v", "status-format[0]"))
	r.Chk("pad is installed", strings.Contains(pad, "pane-active-border-style"))
	styleFor := func(pane string) string {
		out := r.T("display-message", "-p", "-t", pane, pad)
		switch {
		case strings.Contains(out, "red"):
			return "active"
		case strings.Contains(out, "blue"):
			return "inactive"
		}
		return "neither(" + out + ")"
	}

	r.Chk("sidebar focused: active", styleFor(side.Pane) == "active")
	r.Chk("bar-adjacent content pane: active", styleFor(bottom) == "active")
	got := styleFor(top)
	r.Chk("pane at the far end of the divider: inactive", got == "inactive")
	if got != "inactive" {
		t.Logf("  top content pane rendered %s, want inactive", got)
	}

	r.T("kill-pane", "-t", bottom)
	sleep(400)
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
