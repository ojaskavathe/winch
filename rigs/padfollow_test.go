package rigs

import (
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
