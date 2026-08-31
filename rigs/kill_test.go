package rigs

import (
	"strings"
	"testing"
)

// TestKillSessionFromSidebar: x on a session row arms a confirm, y closes the
// session, and anything else does not.
//
// x arms rather than fires because there is no undo behind it — a session and
// everything running in it goes at once.
func TestKillSessionFromSidebar(t *testing.T) {
	r := New(t)

	// A session to kill that the client is NOT in, so this test is only
	// about the confirm; being carried out is TestKillOwnSessionCarriesClient.
	r.T("new-session", "-d", "-s", "doomed")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	sleep(700)

	// Walk the selection onto the doomed row. Sessions sort by name, so
	// `doomed` is the first card under the heading.
	r.await(4000, "doomed listed", func() bool { return strings.Contains(r.Capture(sp), "doomed") })
	selectSessionRow(r, sp, "doomed")

	// Arm, then decline.
	r.SendKeys(sp, "x")
	r.Chk("the prompt names the target", r.WaitUntil(2000, func() bool {
		return strings.Contains(r.Capture(sp), "kill doomed? y/n")
	}))
	r.SendKeys(sp, "n")
	r.Chk("n dismisses the prompt", r.WaitUntil(2000, func() bool {
		return !strings.Contains(r.Capture(sp), "kill doomed? y/n")
	}))
	sleep(500)
	r.Chk("and n killed nothing", hasSession(r, "doomed"))

	// Arm, then escape.
	r.SendKeys(sp, "x")
	r.await(2000, "armed again", func() bool {
		return strings.Contains(r.Capture(sp), "kill doomed? y/n")
	})
	r.SendKeys(sp, "Escape")
	r.Chk("esc dismisses too", r.WaitUntil(2000, func() bool {
		return !strings.Contains(r.Capture(sp), "kill doomed? y/n")
	}))
	sleep(500)
	r.Chk("and esc killed nothing", hasSession(r, "doomed"))

	// Arm and confirm.
	r.SendKeys(sp, "x")
	r.await(2000, "armed", func() bool {
		return strings.Contains(r.Capture(sp), "kill doomed? y/n")
	})
	r.SendKeys(sp, "y")
	r.await(5000, "session killed", func() bool { return !hasSession(r, "doomed") })
	r.Chk("y closed the session", !hasSession(r, "doomed"))
	r.Chk("the sidebar survived it", r.Side().Pane != "")
	r.Chk("the client is untouched", r.ClientSess() == "work" && r.realClient() != "")
	r.Chk("the row went with it", r.WaitUntil(3000, func() bool {
		return !strings.Contains(r.Capture(sp), "doomed")
	}))

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// Killing the session you are sitting in must not cost you your terminal.
// tmux's detach-on-destroy default would throw the client out to a shell, and
// the sidebar pane would die with the window it is docked in — so winch moves
// both somewhere safe before pulling the trigger.
func TestKillOwnSessionCarriesClient(t *testing.T) {
	r := New(t)
	// Deliberately left at tmux's default: the point is that winch does not
	// depend on the user having changed it.
	r.Chk("detach-on-destroy is at its default", r.T("show-options", "-g", "-v", "detach-on-destroy") == "on")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	sleep(700)
	r.Chk("client starts in work", r.ClientSess() == "work")

	selectSessionRow(r, sp, "work")
	r.SendKeys(sp, "x")
	r.await(3000, "armed on own session", func() bool {
		return strings.Contains(r.Capture(sp), "kill work? y/n")
	})
	r.SendKeys(sp, "y")

	r.await(6000, "work is gone", func() bool { return !hasSession(r, "work") })
	r.Chk("the session closed", !hasSession(r, "work"))
	r.Chk("the client survived", r.realClient() != "")
	r.Chk("carried into the surviving session", r.WaitUntil(4000, func() bool {
		return r.ClientSess() == "play"
	}))
	r.Chk("and the sidebar came along", r.WaitUntil(4000, func() bool { return r.Side().Pane != "" }))
	if s := r.Side(); s.Pane != "" {
		r.Chk("the sidebar is in the surviving session", r.ClientWin() != "" && s.Win != "")
	}
}

// x on an agent row closes only that agent's window; the session it lived in
// carries on.
func TestKillAgentWindowFromSidebar(t *testing.T) {
	r := New(t)
	fake := buildFakeAgent(t)

	// An agent in its own window of `play`, which has two windows — so
	// killing this one leaves the session standing.
	r.T("new-window", "-d", "-t", "play:", "-n", "agentwin")
	aw := r.T("display-message", "-p", "-t", "play:agentwin", "#{window_id}")
	ap := r.T("display-message", "-p", "-t", "play:agentwin", "#{pane_id}")
	r.T("respawn-pane", "-k", "-t", ap, fake+" 100000")
	sleep(1700)
	r.T("select-pane", "-T", "⠧ Working on it", "-t", ap)
	r.await(4000, "agent detected", func() bool {
		return r.LogHas("agent claude pane=.* state=.*->working")
	})

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	r.await(4000, "agents section shows it", func() bool {
		return strings.Contains(r.Capture(sp), "agentwin")
	})

	// Walk down to the agent row — the agents section is below the sessions,
	// so the agent's card is reachable by pressing j until the prompt names
	// the agent's window rather than a session.
	armed := false
	for i := 0; i < 40 && !armed; i++ {
		r.SendKeys(sp, "x")
		if r.WaitUntil(700, func() bool {
			return strings.Contains(r.Capture(sp), "kill agentwin? y/n")
		}) {
			armed = true
			break
		}
		r.SendKeys(sp, "Escape")
		sleep(80)
		r.SendKeys(sp, "j")
		sleep(80)
	}
	r.Chk("reached the agent row and armed it", armed)
	if !armed {
		t.Logf("  sidebar:\n%s", r.Capture(sp))
		return
	}

	r.SendKeys(sp, "y")
	r.await(6000, "agent window closed", func() bool {
		out, _ := r.TQ("list-windows", "-a", "-F", "#{window_id}")
		return !containsID(out, aw)
	})
	out, _ := r.TQ("list-windows", "-a", "-F", "#{window_id}")
	r.Chk("the window is gone", !containsID(out, aw))
	r.Chk("its session survived", hasSession(r, "play"))
	r.Chk("the client never moved", r.ClientSess() == "work")
	r.Chk("the sidebar survived", r.Side().Pane != "")

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// selectSessionRow walks the selection to a named session's card by arming the
// confirm and reading who it names — the only way from outside to know which
// row the TUI considers selected.
//
// It goes to the TOP first. Sessions sort by name while the selection opens on
// the client's own session, which is as likely to be the last card as the
// first; scanning down from wherever it started silently never reaches
// anything above it.
func selectSessionRow(r *Rig, sp, name string) {
	r.t.Helper()
	want := "kill " + name + "? y/n"
	for i := 0; i < 30; i++ {
		r.SendKeys(sp, "k")
	}
	sleep(400)
	for i := 0; i < 30; i++ {
		r.SendKeys(sp, "x")
		if r.WaitUntil(700, func() bool { return strings.Contains(r.Capture(sp), want) }) {
			r.SendKeys(sp, "Escape")
			r.WaitUntil(1000, func() bool { return !strings.Contains(r.Capture(sp), want) })
			return
		}
		r.SendKeys(sp, "Escape")
		sleep(80)
		r.SendKeys(sp, "j")
		sleep(80)
	}
	r.t.Fatalf("never reached session row %q; sidebar:\n%s", name, r.Capture(sp))
}
