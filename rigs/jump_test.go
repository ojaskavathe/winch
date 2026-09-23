package rigs

import "testing"

// TestJumplist: `winch jump back|fwd` walks the client's window history across
// windows and sessions, vim CTRL-O / CTRL-I style — undocked, and docked with
// the sidebar riding along. Off by default: without @winch-jumplist the
// command must not move anything.
func TestJumplist(t *testing.T) {
	r := New(t)

	// Off (the default): history is not kept, the command is a no-op.
	r.T("select-window", "-t", r.W1)
	sleep(300)
	r.T("select-window", "-t", r.W3)
	sleep(300)
	r.D("jump", "back", r.CL)
	sleep(300)
	r.Chk("off: jump back does not move", r.ClientWin() == r.W3)

	r.T("set-option", "-g", "@winch-jumplist", "on")
	r.KillDaemon()
	r.D("ls")
	sleep(300)

	// History: W3 (where the daemon found us) -> W1 -> play's P1 -> W2.
	r.T("select-window", "-t", r.W1)
	sleep(300)
	r.T("switch-client", "-c", r.CL, "-t", r.P1)
	sleep(300)
	r.T("switch-client", "-c", r.CL, "-t", r.W2)
	sleep(300)

	for _, want := range []struct{ win, what string }{
		{r.P1, "back across sessions to play"},
		{r.W1, "back again to W1"},
		{r.W3, "back to the oldest entry"},
		{r.W3, "back past the oldest stays put"},
	} {
		r.D("jump", "back", r.CL)
		r.Chk(want.what, r.WaitUntil(2000, func() bool { return r.ClientWin() == want.win }))
	}
	r.D("jump", "fwd", r.CL)
	r.Chk("fwd to W1", r.WaitUntil(2000, func() bool { return r.ClientWin() == r.W1 }))
	r.D("jump", "fwd", r.CL)
	r.Chk("fwd across sessions to play", r.WaitUntil(2000, func() bool {
		return r.ClientWin() == r.P1 && r.ClientSess() == "play"
	}))

	// Docked: the sidebar rides the jump like it rides routed nav.
	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(600)
	r.D("jump", "back", r.CL)
	r.Chk("docked: jump back lands on W1", r.WaitUntil(3000, func() bool { return r.ClientWin() == r.W1 }))
	r.Chk("docked: the sidebar came along", r.WaitUntil(3000, func() bool { return r.Side().Win == r.W1 }))
	r.Chk("docked: keyboard in the main area, not the sidebar", r.ClientPane() != r.Side().Pane)

	r.Undock()
	sleep(500)
}
