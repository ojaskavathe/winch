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

// TestCtrlJKReachTheSidebar: C-j/C-k must step the sidebar's selection through
// sessions AND agents, driven through the ROOT BINDING the way a keypress
// actually arrives.
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

	start := selected()
	r.Chk("something is selected to begin with", start != "")

	// C-j through the bind: 0x0a is what the terminal sends for ctrl-j.
	r.Type("\x0a")
	r.Chk("C-j moved the selection", r.WaitUntil(3000, func() bool {
		return selected() != start && selected() != ""
	}))
	r.Chk("C-j did NOT move the keyboard out of the sidebar", r.ClientPane() == sp)
	down := selected()
	r.Chk("C-j crossed into the agents section", strings.Contains(down, "claude"))

	// C-k comes back the other way.
	r.Type("\x0b")
	r.Chk("C-k moved back", r.WaitUntil(3000, func() bool {
		s := selected()
		return s != "" && s != down
	}))
	r.Chk("C-k did NOT move the keyboard out either", r.ClientPane() == sp)
	r.Chk("and landed back among the sessions", !strings.Contains(selected(), "claude"))

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
	r.Chk("something is selected", start != "")

	r.Type("\x17") // C-w
	r.Chk("the user's own down key moves the selection", r.WaitUntil(3000, func() bool {
		s := navSelected(r)
		return s != "" && s != start
	}))
	r.Chk("and does not move the keyboard out", r.ClientPane() == sp)
	down := navSelected(r)

	r.Type("\x05") // C-e
	r.Chk("the user's own up key moves it back", r.WaitUntil(3000, func() bool {
		s := navSelected(r)
		return s != "" && s != down
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

	// UP first: the selection opens on the client's own session, which sorts
	// last here, so "down" has nowhere to go and a working key would look
	// broken.
	start := navSelected(r)
	r.Chk("something is selected", start != "")
	r.Type("\x10") // C-p
	r.Chk("the configured up key works", r.WaitUntil(3000, func() bool {
		s := navSelected(r)
		return s != "" && s != start
	}))
	up := navSelected(r)

	r.Type("\x0e") // C-n
	r.Chk("the configured down key works", r.WaitUntil(3000, func() bool {
		s := navSelected(r)
		return s != "" && s != up
	}))

	// C-w is bound in tmux and forwards, but the option displaced it. Aim it
	// UP-ward's opposite from the top so a no-op is distinguishable from a
	// clamp: park at the top first, where "down" (C-w's direction) would move
	// if it were still live.
	r.Type("\x10") // C-p, back to the top
	sleep(500)
	top := navSelected(r)
	r.Type("\x17") // C-w — detected, but overridden
	sleep(800)
	r.Chk("the detected-but-overridden key does nothing", navSelected(r) == top)

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
