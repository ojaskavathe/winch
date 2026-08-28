package rigs

import (
	"strings"
	"testing"
)

// TestRestoreSurvivesDeadFocusPane: tmux aborts a command sequence at the first
// error, so anything winch needs to put back must not ride behind a command
// that can fail.
//
// dockMove's restore batch used to open with `select-pane -t <leaveFocus>` —
// the pane the user was last in on the window being left. That pane dies all
// the time: they closed it, or the window churned while the sidebar was
// elsewhere. When it does, select-pane errors, tmux drops the rest of the line,
// and every option restore behind it never runs — while the registry has
// already recorded them as given back, so nothing tries again.
//
// Live evidence, from a real daemon log:
//
//	14:55:30.786  scrub restore @47: tmux: can't find pane: %2072
//
// The session left behind kept a scrub override and spent the next ten minutes
// rendering a DIFFERENT session's window list. Closing panes in it changed
// nothing, because its bar was not describing it.
func TestRestoreSurvivesDeadFocusPane(t *testing.T) {
	r := New(t)

	// Two content panes in the window we will dock into, so killing the one
	// that was active at dock time does not take the window with it.
	r.T("split-window", "-t", r.W2, "-d")
	sleep(300)
	pre := r.T("display-message", "-p", "-t", r.W2, "#{pane_id}")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(600)
	sp := r.Side().Pane

	// The dock snapshot recorded `pre` as the window's active pane, and the
	// sidebar holds focus, so leaveFocus resolves to it. Kill it: the move is
	// now going to try to focus a pane that is gone.
	r.T("kill-pane", "-t", pre)
	sleep(400)
	r.Chk("the focus-restore target is dead", !strings.Contains(
		r.T("list-panes", "-t", r.W2, "-F", "#{pane_id}"), pre))

	sess := r.ClientSess()
	r.Chk("origin session is wrapped while docked",
		strings.Contains(r.ShowOpt("-t", sess, "status-format"), "@winch_win"))

	// Cross-session commit: beta(work) -> a window in play. This is the move
	// whose restore batch the dead pane used to abort.
	r.SendKeys(sp, "k", "k", "k")
	sleep(900)
	r.SendKeys(sp, "Enter")
	r.await(5000, "landed in play", func() bool { return r.ClientSess() == "play" })
	sleep(900)

	left := r.ShowOpt("-t", sess, "status-format")
	r.Chk("the session left behind was unwrapped", !strings.Contains(left, "@winch_win"))
	r.Chk("no scrub override stranded on it", !strings.Contains(left, "#{S:#{?#{==:#{session_id},$"))
	r.Chk("its dock flag was cleared", r.ShowOpt("-t", sess, "-v", "@winch_docked") == "")
	r.Chk("its claim marks were cleared",
		r.ShowOpt("-t", sess, "-v", "@winch_saved_status_format") == "")
	if strings.Contains(left, "@winch_win") {
		t.Logf("  stranded on %s: %.200s", sess, left)
	}

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// TestKilledSidebarGivesEverythingBack: the sidebar pane can go without an
// undock — the user closes it, kill-window takes it, an app in it exits. There
// is no batch to ride along with then, and no layout worth restoring, but every
// option winch took still has to come back.
//
// Deliberately NOT raced against a toggle: whichever of the two lands first
// changes what a toggle means, so the assertion would be about scheduling
// rather than about restores. dockClose's own batch follows the same rule as
// dockMove's — restores lead, `kill-pane -t <sidebar>` trails, because that
// kill errors exactly when the pane is already gone, which is the case where
// the pad most needs removing.
func TestKilledSidebarGivesEverythingBack(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(600)
	sess := r.ClientSess()
	win := r.Side().Win
	r.Chk("wrapped while docked",
		strings.Contains(r.ShowOpt("-t", sess, "status-format"), "@winch_win"))
	r.Chk("window frozen while docked",
		r.ShowOpt("-w", "-t", win, "-v", "automatic-rename") == "off")

	r.T("kill-pane", "-t", r.Side().Pane)

	r.await(6000, "bar unwrapped", func() bool {
		return !strings.Contains(r.ShowOpt("-t", sess, "status-format"), "@winch_win")
	})
	r.Chk("dock flag cleared", r.ShowOpt("-t", sess, "-v", "@winch_docked") == "")
	r.Chk("claim marks cleared",
		r.ShowOpt("-t", sess, "-v", "@winch_saved_status_format") == "")
	r.Chk("window thawed", r.ShowOpt("-w", "-t", win, "-v", "@winch_saved_automatic_rename") == "")
}
