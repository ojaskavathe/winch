package rigs

import (
	"testing"
)

// TestWindowOptionsSurviveCrash is the capability the owned-option registry
// added, stated as the bug it fixes.
//
// The dock freezes automatic-rename and turns pane-border-indicators off on
// every window it holds. Both are the USER's options. Before the registry the
// only record of what they used to hold was a field in the daemon's memory, so
// a daemon killed while docked — a crash, or the pkill in a deploy — left the
// window frozen for good. No sweep could have found it either: `off` is a
// perfectly ordinary thing for a person to have set, so there was nothing to
// recognise, and the old sweeps did not even try.
//
// Each claim is now recorded in a @winch_saved_<option> user option on the
// window itself, which outlives any daemon. The next one puts back exactly what
// was there, whatever it was.
func TestWindowOptionsSurviveCrash(t *testing.T) {
	r := New(t)

	// Distinctive values at WINDOW scope, so a passing restore means "put back
	// what was here" and not "unset it and let the global answer".
	r.T("set-option", "-w", "-t", r.W2, "automatic-rename", "on")
	r.T("set-option", "-w", "-t", r.W2, "pane-border-indicators", "arrows")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(600)

	wopt := func(name string) string {
		return r.ShowOpt("-w", "-t", r.W2, "-v", name)
	}
	r.Chk("rename frozen while docked", wopt("automatic-rename") == "off")
	r.Chk("border indicator off while docked", wopt("pane-border-indicators") == "off")
	r.Chk("rename claim recorded on the window",
		wopt("@winch_saved_automatic_rename") != "")
	r.Chk("indicator claim recorded on the window",
		wopt("@winch_saved_pane_border_indicators") != "")

	r.KillDaemon() // mid-dock: nothing restores anything

	r.Chk("options outlive the daemon", wopt("automatic-rename") == "off" &&
		wopt("pane-border-indicators") == "off")

	r.D("ls") // respawn: the startup sweep runs
	r.await(5000, "window thawed", func() bool {
		return wopt("automatic-rename") == "on" && wopt("pane-border-indicators") == "arrows"
	})
	r.Chk("rename restored to the user's value", wopt("automatic-rename") == "on")
	r.Chk("indicator restored to the user's value", wopt("pane-border-indicators") == "arrows")
	r.Chk("rename mark cleared", wopt("@winch_saved_automatic_rename") == "")
	r.Chk("indicator mark cleared", wopt("@winch_saved_pane_border_indicators") == "")
}

// TestUnsetOptionsRestoreToUnset: the same path for a window that had none of
// these options of its own, which is the common case. Restoring the EFFECTIVE
// value would pin the window to whatever the global happened to say at claim
// time — a silent, permanent divergence from the user's tmux.conf that only
// shows up when they later change the global and one window ignores them.
func TestUnsetOptionsRestoreToUnset(t *testing.T) {
	r := New(t)

	// Nothing set at window scope; a global the restore must NOT freeze in.
	r.T("set-option", "-gw", "pane-border-indicators", "both")
	own := func() string {
		return r.ShowOpt("-w", "-t", r.W2, "-v", "pane-border-indicators")
	}
	r.Chk("nothing of its own to begin with", own() == "")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(600)
	r.Chk("claimed while docked", own() == "off")

	r.KillDaemon()
	r.D("ls")
	r.await(5000, "window released", func() bool { return own() == "" })

	r.Chk("window has none of its own again", own() == "")
	r.Chk("mark cleared", r.ShowOpt("-w", "-t", r.W2, "-v", "@winch_saved_pane_border_indicators") == "")
	// And it follows the global again, which is the point of an unset.
	r.T("set-option", "-gw", "pane-border-indicators", "arrows")
	r.Chk("follows the global again",
		r.T("display-message", "-p", "-t", r.W2, "#{pane-border-indicators}") == "arrows")

	r.T("set-option", "-gw", "-u", "pane-border-indicators")
}
