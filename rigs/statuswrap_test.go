package rigs

import (
	"regexp"
	"strings"
	"testing"
)

// The pad used to be installed by REPLACING the session's status-left, which
// made three of the user's config decisions load-bearing: that their format
// interpolates status-left at all, that they have nothing of their own in it,
// and that every client on the session wants the shift. Wrapping the status
// FORMAT instead — prefixing the pad to each rendered row, gated on the window
// that actually holds the sidebar — removes all three. These rigs pin that.

// statusScreen replays a full client redraw so the status rows can be read.
// The status line is not a pane, so capture-pane cannot see it.
func statusScreen(r *Rig) *screen {
	r.StartRecord()
	r.T("refresh-client", "-t", r.CL)
	sleep(700)
	s := newScreen(r.prof.rows, r.prof.cols)
	for _, c := range r.StopRecordT() {
		s.write(c.Data)
	}
	return s
}

// statusScreenUntil feeds successive refreshes into ONE screen until cond
// holds, or patience runs out.
//
// A terminal's screen is state, not a frame: a repaint rewrites only what
// changed. statusScreen builds an EMPTY screen from a single recording
// window, so a cell tmux did not repaint inside that window is
// indistinguishable from a cell it never drew — and retrying by calling
// statusScreen again DISCARDS the frame that did carry it, which is worse
// than not retrying at all.
//
// That is measurable rather than theoretical: the first capture after a
// focus change missed the border column on every single run, and enough
// parallel load made the second miss it too. Accumulating is what a terminal
// does, so a blank cell here means "never painted in any window", which is
// the thing the caller actually wants to assert.
func statusScreenUntil(r *Rig, cond func(*screen) bool) *screen {
	s := newScreen(r.prof.rows, r.prof.cols)
	for i := 0; i < 12; i++ {
		r.StartRecord()
		r.T("refresh-client", "-t", r.CL)
		sleep(350)
		for _, c := range r.StopRecordT() {
			s.write(c.Data)
		}
		if cond(s) {
			return s
		}
	}
	return s
}

// runeCol is the COLUMN a substring sits at in a grid row. strings.Index would
// answer in bytes, and the border glyph immediately left of the content is
// three of them.
func runeCol(row []rune, sub string) int {
	i := strings.Index(string(row), sub)
	if i < 0 {
		return -1
	}
	return len([]rune(string(row)[:i]))
}

var styleRe = regexp.MustCompile(`#\[[^]]*\]`)

// visible strips tmux style markup, leaving what would occupy columns.
func visible(s string) string { return styleRe.ReplaceAllString(s, "") }

// TestStatusLeftSurvivesDock: a powerline-style status-left is the user's own
// content. Clobbering it meant docking silently deleted a chunk of their bar
// for as long as the sidebar was open. It must survive, and be shifted rather
// than overwritten.
func TestStatusLeftSurvivesDock(t *testing.T) {
	r := New(t)
	r.T("set-option", "-g", "status-left", "PWRLINE|")
	r.T("set-option", "-g", "status-left-length", "20")
	sleep(300)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	w := r.Side().Width
	sleep(500)

	row := statusScreen(r).grid[r.prof.rows-1]
	at := runeCol(row, "PWRLINE")
	r.Chk("status-left survives the dock", at >= 0)
	r.Chk("and starts just past the sidebar border", at == w+1)
	if at != w+1 {
		t.Logf("  PWRLINE at column %d, want %d; row=%q", at, w+1, string(row[:60]))
	}

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
	row = statusScreen(r).grid[r.prof.rows-1]
	r.Chk("undock puts it back at the left edge", runeCol(row, "PWRLINE") == 0)
}

// TestStatusFormatRewrittenShifts: themes and powerline configs sometimes
// rewrite status-format outright and never interpolate status-left. Setting
// status-left then did nothing at all — the bar simply refused to shift, with
// no indication why. Wrapping the format cannot miss.
func TestStatusFormatRewrittenShifts(t *testing.T) {
	r := New(t)
	r.T("set-option", "-g", "status-format[0]", "#[align=left]CUSTOMBAR")
	sleep(300)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	w := r.Side().Width
	sleep(500)

	row := statusScreen(r).grid[r.prof.rows-1]
	at := runeCol(row, "CUSTOMBAR")
	r.Chk("a format that ignores status-left still shifts", at == w+1)
	if at != w+1 {
		t.Logf("  CUSTOMBAR at column %d, want %d", at, w+1)
	}

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// TestStatusPadOnlyOnSidebarWindow: status options are per-SESSION but a
// sidebar is per-client, so a pad installed for the whole session punched a
// hole in the bar of any other client on it. Gating the pad on the window that
// holds the sidebar makes it right for every combination without the daemon
// tracking clients: same window, both padded; different window, neither.
func TestStatusPadOnlyOnSidebarWindow(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	w := r.Side().Width
	sleep(500)

	fmt0 := strings.TrimSpace(r.ShowOpt("-t", r.ClientSess(), "-v", "status-format[0]"))
	r.Chk("pad is installed on the session format", fmt0 != "")

	lead := func(win string) int {
		out := visible(r.T("display-message", "-p", "-t", win, fmt0))
		return len(out) - len(strings.TrimLeft(out, " "))
	}
	other := r.W3
	if r.Side().Win == r.W3 {
		other = r.W1
	}
	on, off := lead(r.Side().Win), lead(other)
	r.Chk("the sidebar's window is padded", on >= w)
	r.Chk("a window without the sidebar is not", off < w)
	if off >= w {
		t.Logf("  window %s indented %d columns with no sidebar in it", other, off)
	}

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// TestStatusMultiRowShifts: status-left appears on status-format[0] only, so a
// two-row bar had its first row shifted and its second starting at column 0,
// straight across the sidebar. Every rendered row has to move as a block.
func TestStatusMultiRowShifts(t *testing.T) {
	r := New(t)
	r.T("set-option", "-g", "status-format[1]", "#[align=left]SECONDROW")
	r.T("set-option", "-g", "status", "2")
	sleep(400)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	w := r.Side().Width
	sleep(500)

	s := statusScreen(r)
	second := s.grid[r.prof.rows-1] // status-format[1], the lower row
	at := runeCol(second, "SECONDROW")
	r.Chk("the second status row shifts too", at == w+1)
	if at != w+1 {
		t.Logf("  SECONDROW at column %d, want %d", at, w+1)
	}

	r.T("set-option", "-g", "status", "on")
	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}
