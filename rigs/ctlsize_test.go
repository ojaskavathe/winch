package rigs

import (
	"strconv"
	"strings"
	"testing"
)

// TestControlClientDoesNotResizeSessions: merely connecting must not change
// the shape of anyone's tmux.
//
// A control client is still a client. It carries a size — the 80x24
// default-size, because nothing ever tells it otherwise — and tmux's default
// `window-size latest` sizes a session to its most recently attached client.
// So the daemon attaching shrank whichever session it landed on to 80
// columns, on a live machine, with no interaction at all. Worse, it
// persisted: the sidebar measured itself inside the shrunken window and
// wrote that width back to @winch-width, so the damage survived restarts.
//
// Asserted two ways on purpose. The flag is what the fix DOES and pins the
// mechanism; the session width is what the user SEES and would still catch
// this if some future tmux stopped honouring the flag.
func TestControlClientDoesNotResizeSessions(t *testing.T) {
	r := New(t)
	want := r.prof.cols

	// play has no real client of its own, which is exactly the session a
	// stray control client is free to resize. work is held up by the fake
	// client and would hide the bug.
	var ctl string
	for _, ln := range strings.Split(
		r.T("list-clients", "-F", "#{client_control_mode} #{client_flags}"), "\n") {
		if f := strings.SplitN(ln, " ", 2); len(f) == 2 && f[0] == "1" {
			ctl = f[1]
		}
	}
	r.Chk("found winch's control client", ctl != "")
	r.Chk("it is attached with ignore-size", strings.Contains(ctl, "ignore-size"))
	if !strings.Contains(ctl, "ignore-size") {
		t.Logf("  control client flags: %q", ctl)
	}

	// Windows, not sessions: #{session_width} is empty in tmux 3.7b, so
	// asserting on it silently asserts nothing — which is exactly what the
	// first version of this test did.
	rows := 0
	for _, ln := range strings.Split(
		r.T("list-windows", "-a", "-F", "#{session_name}:#{window_index} #{window_width}"), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		got, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		rows++
		r.Chk(f[0]+" kept its width", got == want)
		if got != want {
			t.Logf("  %s is %d columns, want %d — a client resized it", f[0], got, want)
		}
	}
	r.Chk("windows were actually measured", rows > 0)
}
