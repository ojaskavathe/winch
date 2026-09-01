package rigs

import (
	"strings"
	"testing"
)

// permissionPrompt is the screen that makes the fake agent read as blocked:
// the manifest's screen tier matches it, the title tier cannot.
const permissionPrompt = `sh -c 'printf "  Do you want to proceed?\n❯ 1. Yes\n  2. No, and tell Claude what to do differently (esc)\n"; exec %s 100000'`

// TestBlockedAgentNotifiesTheTerminal: a blocked agent must reach the
// terminal, not just tmux.
//
// This is asserted on the BYTES the client's terminal actually received,
// rather than on a log line saying we sent them. The whole feature is a
// claim about escaping tmux — tmux swallows OSC 9 itself (input_osc_9 only
// understands the ConEmu `9;4` progress form), so "the daemon wrote a
// notification" and "a notification left the building" are genuinely
// different facts, and only one of them is the feature.
//
// The rig's fake client is a real pty: tmux holds the slave, the harness
// reads the master. A write to client_tty therefore surfaces in exactly the
// same stream as tmux's frames, which is the point — it is the same device.
func TestBlockedAgentNotifiesTheTerminal(t *testing.T) {
	r := New(t)
	fake := buildFakeAgent(t)

	// gamma, which the client is NOT looking at (it sits on beta). An agent
	// you can already see is deliberately not notified about.
	r.StartRecord()
	r.T("split-window", "-d", "-t", r.W3, strings.Replace(permissionPrompt, "%s", fake, 1))
	r.Chk("agent went blocked", r.WaitUntil(8000, func() bool {
		return r.LogHas("agent claude pane=.* state=.*->blocked")
	}))
	r.Chk("notification fired", r.WaitUntil(4000, func() bool {
		return r.LogHas("notify blocked")
	}))
	got := string(r.StopRecord())

	// The default dialect, with a body: the body is where the session and
	// the reason go, and a title-only toast saying "claude needs you" with
	// four claudes running is one you have to investigate anyway.
	r.Chk("OSC 777 reached the terminal", strings.Contains(got, "\033]777;notify;"))
	r.Chk("it says which agent", strings.Contains(got, "claude needs you"))
	r.Chk("it says where, and why", strings.Contains(got, "work · gamma") &&
		strings.Contains(got, "permission prompt"))
	if !strings.Contains(got, "\033]777;notify;") {
		if i := strings.Index(got, "777"); i >= 0 {
			t.Logf("  nearest 777 in stream: %q", got[max(0, i-40):min(len(got), i+80)])
		}
		t.Logf("  daemon log said: desktop=? — %v", r.LogHas("desktop=1"))
	}
}

// TestNotifyDialectIsConfigurable: someone whose terminal only speaks the
// iTerm2 dialect has to be able to say so, and the option has to be read
// late enough that saying so takes effect without restarting the daemon.
func TestNotifyDialectIsConfigurable(t *testing.T) {
	r := New(t)
	fake := buildFakeAgent(t)
	r.T("set-option", "-g", "@winch-notify-osc", "9")

	r.StartRecord()
	r.T("split-window", "-d", "-t", r.W3, strings.Replace(permissionPrompt, "%s", fake, 1))
	r.Chk("notification fired", r.WaitUntil(9000, func() bool {
		return r.LogHas("notify blocked")
	}))
	got := string(r.StopRecord())

	r.Chk("OSC 9 reached the terminal", strings.Contains(got, "\033]9;claude needs you"))
	// OSC 9 has no body field, so the context is folded into the one string
	// it does have rather than dropped on the floor.
	r.Chk("the body was folded in, not lost", strings.Contains(got, "claude needs you — ") &&
		strings.Contains(got, "work · gamma"))
	r.Chk("and not ALSO sent as 777", !strings.Contains(got, "\033]777;"))
}

// TestFlappingAgentNotifiesNobody: the guard herdr added for exactly this
// reason. Agents flicker through blocked on their own, and a notification
// you cannot act on before it is stale is worse than silence.
//
// The flap here is a pane that dies inside the guard window — deterministic,
// unlike waiting for a real agent to change its mind, and it exercises the
// same branch: at the re-check the state is no longer what armed it.
func TestFlappingAgentNotifiesNobody(t *testing.T) {
	r := New(t)
	fake := buildFakeAgent(t)
	// A guard that comfortably outlasts the kill below (two tmux round trips,
	// tens of ms) while keeping the test short. A BROKEN guard fires within
	// one 300ms detection tick of the state change — long before the kill —
	// so the margin here is what makes the assertion discriminating rather
	// than merely patient.
	r.T("set-option", "-g", "@winch-notify-delay", "1000")

	p := r.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", r.W3,
		strings.Replace(permissionPrompt, "%s", fake, 1))
	r.Chk("agent went blocked", r.WaitUntil(8000, func() bool {
		return r.LogHas("agent claude pane=.* state=.*->blocked")
	}))
	r.T("kill-pane", "-t", p)

	// Past the guard, with the reason for the notification gone.
	r.Chk("no notification for an agent that stopped needing one",
		!r.WaitUntil(1800, func() bool { return r.LogHas("notify blocked") }))
}
