package rigs

import (
	"regexp"
	"strings"
	"testing"
)

// navPattern is the regex tmux.nix binds C-h/C-j/C-k/C-l behind. Copied
// verbatim on purpose: this test exists to catch it drifting out of step with
// what the sidebar pane actually reports as its command.
const navPattern = `#{m/r:^(view|l?n?vim?x?|fzf|winch)(diff)?(-wrapped)?$,#{pane_current_command}}`

// TestCtrlJKReachTheSidebar: the pane-navigation keys must JUMP between the
// sidebar's two regions — sessions and agents — driven through the ROOT
// BINDING the way a keypress actually arrives.
//
// One press, not one row. With several sessions listed, stepping row by row
// takes a dozen presses to cross into the agents; C-j is the key that moves to
// the split below, so in the sidebar it moves to the section below.
//
// Every other sidebar-key test sends keys straight to the pane, which skips
// tmux's key table entirely. That hides the failure mode this guards: the bind
// forwards to the pane only when pane_current_command matches navPattern, and
// if it ever does not — a rename, a nix wrapper turning `winch` into
// `.winch-wrapped` the way it does `.claude-wrapped` — the fallback fires and
// C-j moves the tmux focus OUT of the sidebar instead of moving the selection.
// Identical symptom, opposite half of the system.
func TestCtrlJKReachTheSidebar(t *testing.T) {
	r := New(t)
	fake := buildFakeAgent(t)

	// The navigator binds as shipped.
	r.T("bind-key", "-n", "C-j", "if-shell", "-F", navPattern, "send-keys C-j", "select-pane -D")
	r.T("bind-key", "-n", "C-k", "if-shell", "-F", navPattern, "send-keys C-k", "select-pane -U")

	ap := r.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", r.W3, fake+" 100000")
	sleep(1700)
	r.T("select-pane", "-T", "⠧ Cooking again", "-t", ap)
	r.await(6000, "agent detected", func() bool {
		return r.LogHas("agent claude pane=.* state=.*->working")
	})

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	sleep(800)

	// The keyboard has to be IN the sidebar: that is the state where the bind
	// has to forward rather than move focus.
	r.T("select-pane", "-t", sp)
	r.await(3000, "keyboard in the sidebar", func() bool { return r.ClientPane() == sp })

	// The command the bind matches on, spelled out — if this stops being
	// `winch` the pattern above needs to learn the new spelling.
	cmd := r.T("display-message", "-p", "-t", sp, "#{pane_current_command}")
	r.Chk("the sidebar pane matches the navigator pattern (got "+cmd+")",
		regexp.MustCompile(`^(view|l?n?vim?x?|fzf|winch)(diff)?(-wrapped)?$`).MatchString(cmd))

	esc := regexp.MustCompile("\x1b\\[[0-9;:]*m")
	selected := func() string {
		raw, _ := r.TQ("capture-pane", "-p", "-e", "-t", r.Side().Pane)
		for _, ln := range strings.Split(raw, "\n") {
			if strings.Contains(ln, "49;50;68") || strings.Contains(ln, "49:50:68") {
				return strings.TrimSpace(esc.ReplaceAllString(ln, ""))
			}
		}
		return ""
	}

	// Park at the TOP of the sessions list, as far from the agents as the
	// list gets — a jump has to cross the whole distance, where a row-step
	// would only reach the neighbouring session.
	for i := 0; i < 10; i++ {
		r.SendKeys(sp, "k")
	}
	sleep(500)
	start := selected()
	r.Chk("parked on a session at the top", start != "" && !strings.Contains(start, "claude"))

	// C-j through the bind: 0x0a is what the terminal sends for ctrl-j.
	r.Type("\x0a")
	r.Chk("one C-j reaches the agents section", r.WaitUntil(3000, func() bool {
		return strings.Contains(selected(), "claude")
	}))
	r.Chk("C-j did NOT move the keyboard out of the sidebar", r.ClientPane() == sp)

	// A second C-j does nothing: there is no region below the agents, and
	// tmux does not wrap to the top pane either.
	//
	// Asserted on the REGION rather than the row text: a working agent's row
	// carries a spinner that advances several times a second, so comparing
	// the rendered line would fail on the animation and say "wrapped".
	r.Type("\x0a")
	sleep(700)
	r.Chk("a second C-j does not wrap back to the sessions",
		strings.Contains(selected(), "claude"))

	// C-k comes back the other way, to where it left.
	r.Type("\x0b")
	r.Chk("C-k returns to the sessions", r.WaitUntil(3000, func() bool {
		s := selected()
		return s != "" && !strings.Contains(s, "claude")
	}))
	r.Chk("C-k did NOT move the keyboard out either", r.ClientPane() == sp)
	r.Chk("and landed back where it left the list", selected() == start)

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// TestNavKeysFollowTheUsersBinds: the sidebar answers to whatever keys THIS
// tmux moves between panes with, not to a hardcoded C-hjkl.
//
// Detection reads what a binding DOES (select-pane -U/-D) rather than which
// plugin wrote it, so an unusual choice works without configuring winch at
// all. C-w/C-e here precisely because nobody's navigator ships them.
func TestNavKeysFollowTheUsersBinds(t *testing.T) {
	r := New(t)
	fake := buildFakeAgent(t)

	r.T("bind-key", "-n", "C-w", "if-shell", "-F", navPattern, "send-keys C-w", "select-pane -D")
	r.T("bind-key", "-n", "C-e", "if-shell", "-F", navPattern, "send-keys C-e", "select-pane -U")

	ap := r.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", r.W3, fake+" 100000")
	sleep(1700)
	r.T("select-pane", "-T", "⠧ Cooking again", "-t", ap)
	r.await(6000, "agent detected", func() bool {
		return r.LogHas("agent claude pane=.* state=.*->working")
	})

	// Config is read at attach, so the daemon has to meet the binds.
	r.KillDaemon()
	r.D("ls")
	r.await(5000, "nav detected", func() bool {
		return r.LogHas(`nav keys .*down=C-w.*`)
	})
	r.Chk("detection found the user's down key", r.LogHas(`down=C-w`))
	r.Chk("detection found the user's up key", r.LogHas(`up=C-e`))

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	r.T("select-pane", "-t", sp)
	r.await(3000, "keyboard in the sidebar", func() bool { return r.ClientPane() == sp })
	sleep(600)

	start := navSelected(r)
	r.Chk("starts among the sessions", start != "" && !strings.Contains(start, "claude"))

	r.Type("\x17") // C-w
	r.Chk("the user's own down key reaches the agents", r.WaitUntil(3000, func() bool {
		return strings.Contains(navSelected(r), "claude")
	}))
	r.Chk("and does not move the keyboard out", r.ClientPane() == sp)

	r.Type("\x05") // C-e
	r.Chk("the user's own up key returns to the sessions", r.WaitUntil(3000, func() bool {
		s := navSelected(r)
		return s != "" && !strings.Contains(s, "claude")
	}))

	// C-j is nothing here — it is not bound at all in this tmux, so it must
	// not still be wired in from the old hardcoded pair.
	before := navSelected(r)
	r.Type("\x0a")
	sleep(700)
	r.Chk("C-j does nothing when this tmux does not use it", navSelected(r) == before)

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// TestNavKeysExplicitOverride: @winch-nav-keys beats detection.
func TestNavKeysExplicitOverride(t *testing.T) {
	r := New(t)
	fake := buildFakeAgent(t)

	// An agent, so there is a second region for the keys to jump to.
	ap := r.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", r.W3, fake+" 100000")
	sleep(1700)
	r.T("select-pane", "-T", "⠧ Cooking again", "-t", ap)

	// Binds say C-w/C-e; the option says C-n/C-p. The option wins.
	r.T("bind-key", "-n", "C-w", "if-shell", "-F", navPattern, "send-keys C-w", "select-pane -D")
	r.T("bind-key", "-n", "C-e", "if-shell", "-F", navPattern, "send-keys C-e", "select-pane -U")
	r.T("bind-key", "-n", "C-n", "if-shell", "-F", navPattern, "send-keys C-n", "select-pane -D")
	r.T("bind-key", "-n", "C-p", "if-shell", "-F", navPattern, "send-keys C-p", "select-pane -U")
	r.T("set-option", "-g", "@winch-nav-keys", "C-h,C-n,C-p,C-l")

	r.KillDaemon()
	r.D("ls")
	r.await(5000, "option honoured", func() bool { return r.LogHas(`down=C-n`) })
	r.Chk("the option's down key is in force", r.LogHas(`down=C-n`))
	r.Chk("and detection did not win", !r.LogHas(`nav keys .*down=C-w`))

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	r.T("select-pane", "-t", sp)
	r.await(3000, "keyboard in the sidebar", func() bool { return r.ClientPane() == sp })
	sleep(600)

	start := navSelected(r)
	r.Chk("starts among the sessions", start != "" && !strings.Contains(start, "claude"))

	r.Type("\x0e") // C-n
	r.Chk("the configured down key reaches the agents", r.WaitUntil(3000, func() bool {
		return strings.Contains(navSelected(r), "claude")
	}))

	r.Type("\x10") // C-p
	r.Chk("the configured up key returns", r.WaitUntil(3000, func() bool {
		s := navSelected(r)
		return s != "" && !strings.Contains(s, "claude")
	}))

	// C-w is bound in tmux and forwards, but the option displaced it — so
	// from the sessions it must NOT jump to the agents.
	inSess := navSelected(r)
	r.Type("\x17") // C-w — detected, but overridden
	sleep(800)
	r.Chk("the detected-but-overridden key does nothing", navSelected(r) == inSess)

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// navSelected returns the text of the filled (selected) row.
func navSelected(r *Rig) string {
	esc := regexp.MustCompile("\x1b\\[[0-9;:]*m")
	raw, _ := r.TQ("capture-pane", "-p", "-e", "-t", r.Side().Pane)
	for _, ln := range strings.Split(raw, "\n") {
		if strings.Contains(ln, "49;50;68") || strings.Contains(ln, "49:50:68") {
			return strings.TrimSpace(esc.ReplaceAllString(ln, ""))
		}
	}
	return ""
}
