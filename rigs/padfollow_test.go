package rigs

import (
	"fmt"
	"strings"
	"testing"
)

// TestPadFollowsWindow: the pad is gated on @winch_win, so it is only correct
// for as long as that option tracks the window the sidebar is actually in. The
// sidebar moves constantly — every commit, and every unrouted switch the
// daemon follows — and a gate left pointing at the window it started in shows
// the shift once and never again.
func TestPadFollowsWindow(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	w := r.Side().Width
	sleep(600)

	padded := func(what string) bool {
		row := statusScreen(r).grid[r.prof.rows-1]
		blank := strings.TrimSpace(string(row[:w])) == ""
		t.Logf("  %s: win=%s @winch_win=%s col%d=%q blankPad=%v",
			what, r.Side().Win, r.ShowOpt("-t", r.ClientSess(), "-v", "@winch_win"),
			w, row[w], blank)
		return blank && row[w] == '│'
	}

	r.Chk("padded on the window it opened in", padded("opened"))

	// An unrouted switch: the user hits tmux's own key, the daemon follows.
	r.T("select-window", "-t", r.W3)
	r.await(5000, "sidebar followed", func() bool { return r.Side().Win == r.W3 })
	sleep(700)
	r.Chk("still padded after following to another window", padded("followed"))

	// And back.
	r.T("select-window", "-t", r.W2)
	r.await(5000, "sidebar followed back", func() bool { return r.Side().Win == r.W2 })
	sleep(700)
	r.Chk("still padded after coming back", padded("returned"))

	r.D("toggle", r.CL)
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// TestPadFollowsWindowTopBar is TestPadFollowsWindow against the real config's
// bar position. The rig's tmux runs -f /dev/null, so everything else here
// tests `status-position bottom` — which is the stock default and NOT what the
// bar being described is set to.
func TestPadFollowsWindowTopBar(t *testing.T) {
	r := New(t)
	r.T("set-option", "-g", "status-position", "top")
	sleep(300)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	w := r.Side().Width
	sleep(600)

	glyph := func(what string) rune {
		row := statusScreen(r).grid[0] // top bar: the status row is row 0
		t.Logf("  %s: win=%s @winch_win=%s panes=%s zoom=%s col%d=%q",
			what, r.Side().Win, r.ShowOpt("-t", r.ClientSess(), "-v", "@winch_win"),
			r.T("display-message", "-p", "-t", r.Side().Win, "#{window_panes}"),
			r.T("display-message", "-p", "-t", r.Side().Win, "#{window_zoomed_flag}"),
			w, row[w])
		return row[w]
	}

	r.Chk("glyph on the window it opened in", glyph("opened") == '│')

	r.T("select-window", "-t", r.W3)
	r.await(5000, "sidebar followed", func() bool { return r.Side().Win == r.W3 })
	sleep(700)
	r.Chk("glyph survives following to another window", glyph("followed") == '│')

	r.T("select-window", "-t", r.W2)
	r.await(5000, "sidebar followed back", func() bool { return r.Side().Win == r.W2 })
	sleep(700)
	r.Chk("glyph survives coming back", glyph("returned") == '│')

	r.T("set-option", "-g", "status-position", "bottom")
	r.D("toggle", r.CL)
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// TestGlyphMatchesBorder is the invariant that actually matters, and the only
// one a user can see: the glyph must be painted the SAME colour as the border
// cell it continues. Whether that colour is the active or the inactive one is
// tmux's business — copying its rule is a means, not the goal, and a glyph
// that disagrees with the border one row away reads as broken however
// defensible the rule behind it was.
//
// Bar at the top, as in the real config. The sidebar pane carries a pinned
// border style, so the seam holds one colour whatever has focus — tmux would
// otherwise dim the divider whenever the sidebar is not the active pane.
//
// Known limit: the pin is attributed to the sidebar only while the column
// opposite it is a single pane. Split that column and tmux hands the top
// border cell to the content pane instead, and the seam follows it again.
func TestGlyphMatchesBorder(t *testing.T) {
	r := New(t)
	r.T("set-option", "-g", "status-position", "top")
	sleep(300)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	side := r.Side()
	w := side.Width
	sleep(500)

	// Find the content pane opposite the sidebar.
	content := ""
	for _, ln := range strings.Split(r.T("list-panes", "-t", side.Win, "-F", "#{pane_id} #{pane_left}"), "\n") {
		if f := strings.Fields(ln); len(f) == 2 && f[1] != "0" {
			content = f[0]
		}
	}
	r.Chk("found the content pane", content != "")

	t.Logf("  pinned on %s: border=%q active=%q", side.Pane,
		r.ShowOpt("-p", "-t", side.Pane, "-v", "pane-border-style"),
		r.ShowOpt("-p", "-t", side.Pane, "-v", "pane-active-border-style"))

	// Top bar: status is row 0, so the border cell it continues is row 1.
	// Retried: a single redraw does not always repaint the border column, so
	// the model can hold a blank there through no fault of the daemon. Waiting
	// for both cells to be painted cannot hide a colour mismatch.
	seam := func(what string) string {
		var last string
		for i := 0; i < 10; i++ {
			s := statusScreen(r)
			last = fmt.Sprintf("glyph=%q fg=%q | border=%q fg=%q",
				s.grid[0][w], s.fg[0][w], s.grid[1][w], s.fg[1][w])
			if s.grid[0][w] == '│' && s.grid[1][w] == '│' {
				t.Logf("  %s: %s (attempt %d)", what, last, i+1)
				if s.fg[0][w] != s.fg[1][w] {
					return "MISMATCH"
				}
				return s.fg[0][w]
			}
			sleep(400)
		}
		t.Logf("  %s: %s — never painted", what, last)
		return "MISMATCH"
	}

	// split-window takes focus, so put it back before claiming otherwise.
	r.T("select-pane", "-t", side.Pane)
	sleep(700)
	onSidebar := seam("sidebar focused")
	r.Chk("matches with the sidebar focused", onSidebar != "MISMATCH")

	r.T("select-pane", "-t", content)
	sleep(700)
	onContent := seam("content focused")
	r.Chk("matches with the content focused", onContent != "MISMATCH")

	// The point of pinning the sidebar's border per-pane: the whole edge is
	// ONE colour, so it neither dims nor brightens as focus moves. Without the
	// pin tmux dims the divider whenever the sidebar is not focused, and a
	// single dim cell in the status row reads as no seam at all.
	r.Chk("the seam is the same colour whatever has focus", onSidebar == onContent)
	if onSidebar != onContent {
		t.Logf("  sidebar-focused %q vs content-focused %q", onSidebar, onContent)
	}

	r.T("set-option", "-g", "status-position", "bottom")
	r.D("toggle", r.CL)
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// TestPadFollowsCommit: the same question for the path the user actually
// takes — scrub the list and press Enter. Crossing a session boundary restores
// the session being left and pads the one being entered, so both halves have
// to land, and coming back has to pad again.
func TestPadFollowsCommit(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	w := r.Side().Width
	sleep(600)

	// The glyph too, not just the shift: a commit is the common case, and it
	// is the one where the pad is rewritten by the scrub restore rather than
	// left alone.
	padded := func(what string) bool {
		row := statusScreen(r).grid[r.prof.rows-1]
		blank := strings.TrimSpace(string(row[:w])) == ""
		t.Logf("  %s: sess=%s win=%s @winch_win=%s panes=%s zoom=%s col%d=%q blankPad=%v",
			what, r.ClientSess(), r.Side().Win,
			r.ShowOpt("-t", r.ClientSess(), "-v", "@winch_win"),
			r.T("display-message", "-p", "-t", r.Side().Win, "#{window_panes}"),
			r.T("display-message", "-p", "-t", r.Side().Win, "#{window_zoomed_flag}"),
			w, row[w], blank)
		return blank && row[w] == '│'
	}

	r.Chk("padded on open", padded("opened"))

	// beta(work) -> ... -> a window in play: a cross-session commit.
	r.SendKeys(sp, "k", "k", "k")
	sleep(900)
	r.SendKeys(sp, "Enter")
	r.await(5000, "landed in play", func() bool { return r.ClientSess() == "play" })
	sleep(900)
	r.Chk("padded after a cross-session commit", padded("committed to play"))

	// and back to work
	r.SendKeys(r.Side().Pane, "j", "j", "j")
	sleep(900)
	r.SendKeys(r.Side().Pane, "Enter")
	r.await(5000, "back in work", func() bool { return r.ClientSess() == "work" })
	sleep(900)
	r.Chk("padded again after coming back", padded("returned to work"))

	r.D("toggle", r.CL)
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// TestGlyphMatchesBorderAfterCommit: the same invariant across the action that
// broke it, with real border colours rather than the stock `default` — a
// commit moves focus off the sidebar and onto a content pane, and that is
// exactly the transition the old rule got backwards. Measured against the
// border cell rather than against an expectation, so the test cannot inherit
// whatever mistaken idea of tmux's rule the code has.
func TestGlyphMatchesBorderAfterCommit(t *testing.T) {
	r := New(t)
	r.T("set-option", "-g", "status-position", "top")
	r.T("set-option", "-g", "status-style", "bg=#181825,fg=#cdd6f4")
	r.T("set-option", "-gw", "pane-border-style", "fg=#6c7086")
	r.T("set-option", "-gw", "pane-active-border-style", "fg=#b4befe")
	sleep(300)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	w := r.Side().Width
	sp := r.Side().Pane
	sleep(600)

	// Retried rather than slept at: a commit reflows panes and repaints, and
	// under parallel load that takes longer than any fixed sleep worth
	// writing. Retrying cannot mask a real mismatch — a wrong colour stays
	// wrong — it only stops the rig from reading a half-finished transition.
	check := func(what string) bool {
		var last string
		for i := 0; i < 10; i++ {
			s := statusScreen(r)
			gl, bd := s.fg[0][w], s.fg[1][w] // top bar: status row 0, border row 1
			last = fmt.Sprintf("glyph=%q fg=%q | border=%q fg=%q",
				s.grid[0][w], gl, s.grid[1][w], bd)
			if s.grid[0][w] == '│' && s.grid[1][w] == '│' && gl == bd {
				t.Logf("  %s: %s | match=true (attempt %d)", what, last, i+1)
				return true
			}
			sleep(400)
		}
		t.Logf("  %s: %s | match=false", what, last)
		return false
	}

	r.Chk("matches on open", check("opened"))

	r.SendKeys(sp, "k", "k", "k")
	sleep(900)
	r.SendKeys(sp, "Enter")
	r.await(5000, "committed", func() bool { return r.ClientSess() == "play" })
	sleep(1200)
	r.Chk("matches after a commit moves focus to the content", check("after commit"))

	r.T("set-option", "-g", "status-position", "bottom")
	r.D("toggle", r.CL)
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}
