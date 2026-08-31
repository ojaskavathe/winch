package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Desktop notifications: the sidebar's whole reason for existing is telling
// you an agent needs you, and until now it only did that INSIDE tmux
// (display-message to other clients). If you were not looking at tmux, winch
// had nothing to say.
//
// tmux is not a route to the outer terminal. input.c intercepts OSC 9 and
// understands only the ConEmu-style `9;4` progress form — anything else is
// logged "bad OSC 9;4" and dropped (input_osc_9) — so a notification printed
// from a pane is swallowed, not forwarded. The DCS passthrough
// (`\ePtmux;...`) would get through, but only from a pane winch owns, which
// means only while docked; the notification you most want is the one that
// arrives when you are not looking at the sidebar at all.
//
// So the daemon writes to the client's own tty (#{client_tty}) — the same
// device tmux is writing frames to. Three things make that safe:
//
//   - One write() syscall per notification. The kernel will not interleave
//     another writer inside a single write, so the sequence cannot be torn.
//   - The payload paints nothing. A notification OSC moves no cursor and
//     touches no cell, so landing between two of tmux's frames is invisible;
//     tmux's model of the screen stays true.
//   - O_NOCTTY. The daemon is detached, and a detached session leader that
//     opens a terminal without it would ACQUIRE that terminal as its
//     controlling tty — which would make winch a background job of the
//     user's shell.
//
// It also works over ssh for free: the client's tty is local to the machine
// the client is on, so the bytes go down the connection to whatever terminal
// is really there.

const (
	optNotify      = "@winch-notify"       // off | blocked | all
	optNotifyOSC   = "@winch-notify-osc"   // 777 | 9 | 99
	optNotifyDelay = "@winch-notify-delay" // milliseconds; the flap guard
)

// notifyDefaultDelay is herdr's re-check idea: an agent that blocks and
// unblocks inside the window never earns a notification. A prompt you answer
// before you could possibly have read a toast did not need one, and agents
// flicker through blocked on their own often enough that firing on the
// transition alone is noise.
const notifyDefaultDelay = 1000 * time.Millisecond

type notifyCfg struct {
	blocked bool // notify when an agent stops for input
	done    bool // notify when an agent finishes a turn
	osc     string
	delay   time.Duration
}

// parseNotifyMode reads @winch-notify. Unset means blocked-only: the default
// has to be the notification you would have asked for, and "your agent is
// waiting on you" is that one. `all` adds turn-end, which is useful and much
// chattier — one per turn, per agent.
func parseNotifyMode(s string) (blocked, done bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "none", "0":
		return false, false
	case "all", "both", "done":
		return true, true
	default: // "", "blocked", "on", anything unrecognised
		return true, false
	}
}

// parseNotifyOSC picks the sequence. Terminals disagree about which OSC means
// "tell the desktop", and the disagreement is not resolvable by detection —
// several accept more than one, and being told twice is worse than being told
// in the wrong dialect.
//
//	777  ESC ] 777 ; notify ; TITLE ; BODY ST   kitty, wezterm, ghostty, urxvt
//	9    ESC ] 9 ; TEXT ST                      iTerm2, kitty, Windows Terminal
//	99   kitty's own protocol, title and body as separate payloads
//
// 777 is the default because it is the widest one that carries a BODY, and
// the body is where the session and the reason go — a title-only
// notification saying "claude needs you" with four claudes running is a
// notification you have to go and investigate anyway.
func parseNotifyOSC(s string) string {
	switch strings.TrimSpace(s) {
	case "9":
		return "9"
	case "99":
		return "99"
	default:
		return "777"
	}
}

func parseNotifyDelay(s string) time.Duration {
	ms, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || ms < 0 {
		return notifyDefaultDelay
	}
	return time.Duration(ms) * time.Millisecond
}

// loadNotifyCfg reads all three options in ONE round trip. optStr would cost
// three, and each of them takes the error path when its option is unset —
// which is the normal case. A format reference answers "" for an unset user
// option instead of failing, so this is both cheaper and quieter.
func loadNotifyCfg(ctl *control) notifyCfg {
	c := notifyCfg{osc: parseNotifyOSC(""), delay: notifyDefaultDelay}
	c.blocked, c.done = parseNotifyMode("")
	lines, err := ctl.run("display-message -p " +
		f("#{"+optNotify+"}", "#{"+optNotifyOSC+"}", "#{"+optNotifyDelay+"}"))
	if err != nil || len(lines) != 1 {
		return c
	}
	p := strings.Split(lines[0], sep)
	if len(p) != 3 {
		return c
	}
	c.blocked, c.done = parseNotifyMode(p[0])
	c.osc, c.delay = parseNotifyOSC(p[1]), parseNotifyDelay(p[2])
	return c
}

func (c notifyCfg) on() bool { return c.blocked || c.done }

func (c notifyCfg) String() string {
	switch {
	case !c.on():
		return "off"
	case c.done:
		return fmt.Sprintf("blocked+done, OSC %s, %dms guard", c.osc, c.delay.Milliseconds())
	default:
		return fmt.Sprintf("blocked, OSC %s, %dms guard", c.osc, c.delay.Milliseconds())
	}
}

// notifyClean strips what would break the sequence or the terminal. Titles
// come from agent manifests and session names — user data — so this is not
// paranoia: an ESC in a title would end the OSC early and leave the rest of
// the message being interpreted as terminal commands.
//
// Semicolons go too, but only because 777 uses them as its field separator;
// a session named "a;b" would otherwise silently move half the name into the
// body.
func notifyClean(s string, semis bool) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f: // C0 and DEL, including ESC
			continue
		case r == ';' && semis:
			b.WriteRune(',')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len([]rune(out)) > 120 {
		out = string([]rune(out)[:120])
	}
	return out
}

// notifyPayload renders one notification, terminated with ST (ESC \) rather
// than BEL: both are accepted everywhere that matters and ST is the form the
// specs are written in.
func notifyPayload(osc, title, body string) string {
	const st = "\033\\"
	switch osc {
	case "9":
		// No body field exists, so fold it in rather than drop it.
		t := notifyClean(title, false)
		if b := notifyClean(body, false); b != "" {
			t += " — " + b
		}
		return "\033]9;" + t + st
	case "99":
		// d=0 means "more to come", d=1 closes the command. Two payloads,
		// one identifier, so kitty renders them as one notification.
		t := "\033]99;i=winch:d=0:p=title;" + notifyClean(title, false) + st
		return t + "\033]99;i=winch:d=1:p=body;" + notifyClean(body, false) + st
	default:
		return "\033]777;notify;" + notifyClean(title, true) +
			";" + notifyClean(body, true) + st
	}
}

// notifyRipe decides one armed notification: fire it, forget it, or leave it
// waiting. Pulled out of the tick loop because this is the entire policy —
// everything around it is bookkeeping — and because the interesting cases
// (the agent answered the prompt; the pane died; the guard has not elapsed)
// are the ones that never happen while you are watching a rig.
//
// cur is the agent's state right now, "" if the pane is gone.
func notifyRipe(p pendingNote, cur string, now time.Time, delay time.Duration) (fire, drop bool) {
	if cur != p.state {
		// Answered, moved on, or gone. It no longer needs you, so a
		// notification would arrive as a lie about the present.
		return false, true
	}
	if now.Sub(p.at) < delay {
		return false, false
	}
	return true, true
}

// cmdNotifyTest is `winch notify-test [777|9|99]`: send one notification to
// the terminal this is being run from, and say exactly what was written where.
//
// It exists because "does your terminal do desktop notifications" is not
// answerable from inside winch — it depends on the emulator, its config, and
// on macOS whether the app has been granted notification permission. One
// command that either pops a toast or does not is worth more than any
// amount of capability detection, and it needs no daemon.
func cmdNotifyTest(tmuxSock, osc string) {
	t := eqTmux{sock: tmuxSock}
	tty, err := t.out("display-message", "-p", "#{client_tty}")
	if err != nil || tty == "" {
		fmt.Fprintf(os.Stderr, "winch notify-test: no attached client to notify (%v)\n", err)
		os.Exit(1)
	}
	kind := parseNotifyOSC(osc)
	payload := notifyPayload(kind, "winch", "if you can read this, notifications work")
	if err := notifyTTY(tty, payload); err != nil {
		fmt.Fprintf(os.Stderr, "winch notify-test: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("sent OSC %s to %s\n  %q\n", kind, tty, payload)
	fmt.Printf("nothing appeared? try the other dialects:\n" +
		"  winch notify-test 9\n  winch notify-test 99\n" +
		"then set the one that works: tmux set -g " + optNotifyOSC + " <n>\n")
}

// notifyTTY delivers one payload to one terminal. Errors are the caller's to
// log and swallow: a client whose tty vanished between the world snapshot and
// this write is a race, not a fault, and a failed notification must never take
// down a detection tick.
func notifyTTY(tty, payload string) error {
	if tty == "" || !strings.HasPrefix(tty, "/dev/") {
		return fmt.Errorf("refusing to write to %q", tty)
	}
	f, err := os.OpenFile(tty, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(payload) // ONE write; see the file comment
	return err
}
