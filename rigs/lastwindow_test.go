package rigs

import (
	"strings"
	"testing"
)

// sessionNames lists live sessions, comma-joined.
func sessionNames(r *Rig) string {
	out, _ := r.TQ("list-sessions", "-F", "#{session_name}")
	return strings.ReplaceAll(out, "\n", ",")
}

func hasSession(r *Rig, name string) bool {
	for _, s := range strings.Split(sessionNames(r), ",") {
		if s == name {
			return true
		}
	}
	return false
}

// TestSidebarDoesNotHoldADeadWindowOpen: the sidebar is a pane, so a window it
// is docked in stops obeying tmux's "last pane closes the window" rule. Close
// your only real split in a one-window session and the session should end —
// instead the sidebar stretched to fill the whole window and the session sat
// there as a full-width TUI, in a session tmux would have destroyed.
//
// Reported as "closing the last remaining window in a session breaks stuff".
func TestSidebarDoesNotHoldADeadWindowOpen(t *testing.T) {
	r := New(t)
	// Mirrors the live config (tmux.nix). tmux's default is `on`, which
	// detaches the client outright when its session is destroyed — verified
	// to happen with winch entirely uninvolved, so it is not winch's to fix,
	// but with `off` the client lands in another session and the daemon's
	// release drain is not cut short mid-flight.
	r.T("set-option", "-g", "detach-on-destroy", "off")

	r.T("new-session", "-d", "-s", "solo")
	solo := r.T("display-message", "-p", "-t", "solo:", "#{window_id}")
	own := r.T("display-message", "-p", "-t", "solo:", "#{pane_id}")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	r.T("switch-client", "-c", r.CL, "-t", "solo")
	r.await(5000, "sidebar followed into solo", func() bool { return r.Side().Win == solo })
	sleep(600)
	r.Chk("sidebar is docked in solo's only window", r.Side().Win == solo)

	// The user closes the one pane they own.
	r.T("kill-pane", "-t", own)

	r.await(6000, "solo goes away", func() bool { return !hasSession(r, "solo") })
	r.Chk("the session is gone, as tmux would have closed it", !hasSession(r, "solo"))
	if hasSession(r, "solo") {
		panes, _ := r.TQ("list-panes", "-t", solo, "-F", "#{pane_id} #{pane_width} #{pane_current_command}")
		t.Logf("  solo survives with panes: %q", panes)
	}

	// The sidebar went with it rather than being left running against a dead
	// window, and nothing of winch's is left on the server.
	r.await(4000, "no winch panes", func() bool { return r.WinchPanes("-a") == 0 })
	r.Chk("sidebar pane is gone", r.WinchPanes("-a") == 0)
	// The spacer left behind in the window the sidebar started in is released
	// on a timer (releaseSettle), so this has to wait rather than sample.
	r.await(4000, "spacers released", func() bool { return r.Spacers() == 0 })
	r.Chk("no spacers leaked", r.Spacers() == 0)

	// The client is carried to another session rather than dumped to a shell.
	r.Chk("the client is still alive", r.realClient() != "")
	r.Chk("and landed in a surviving session", r.ClientSess() != "" && r.ClientSess() != "solo")
}

// The same rule must not fire while the sidebar is legitimately the only
// VISIBLE pane. Scrubbing zooms it to full width with the real panes still
// there; reaping then would kill the sidebar mid-gesture.
func TestScrubIsNotMistakenForAnEmptyWindow(t *testing.T) {
	r := New(t)

	r.D("browse", r.CL) // dock and zoom straight into scrubbing
	r.await(5000, "scrubbing", func() bool { return r.Side().Pane != "" && r.Zoomed(r.Side().Win) })
	sleep(1200) // long enough for several re-lists to pass through checkDock

	r.Chk("sidebar survived the scrub", r.Side().Pane != "")
	r.Chk("still zoomed", r.Zoomed(r.Side().Win))
	r.Chk("client alive", r.realClient() != "")

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// Killing the last window of a session the client is NOT in has always been
// clean; this pins it so the reap above cannot regress it.
func TestKillingAnotherSessionsLastWindowIsClean(t *testing.T) {
	r := New(t)

	r.T("new-session", "-d", "-s", "solo")
	solo := r.T("display-message", "-p", "-t", "solo:", "#{window_id}")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sideWin := r.Side().Win

	r.T("kill-window", "-t", solo)
	r.await(5000, "solo gone", func() bool { return !hasSession(r, "solo") })

	r.Chk("solo dropped out", !hasSession(r, "solo"))
	r.Chk("client did not move", r.ClientSess() == "work")
	r.Chk("sidebar stayed put", r.Side().Win == sideWin)
	r.Chk("client alive", r.realClient() != "")

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}
