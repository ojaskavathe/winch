package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
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
	optNotifyOSC   = "@winch-notify-osc"   // unset = auto from TERM; 777 | 9 | 99 pins it
	optNotifyVia   = "@winch-notify-via"   // terminal | system | both
	optNotifyDelay = "@winch-notify-delay" // milliseconds; the flap guard
)

// parseNotifyVia picks who gets asked to draw the notification.
//
//	terminal  write the OSC to the client's tty; the terminal decides
//	system    ask the OS notification service directly
//	both      belt and braces, for a machine where you are not sure yet
//
// `terminal` is the default everywhere, because it is the one that works
// over ssh — the notification follows you to whatever machine you are
// actually sitting at.
//
// macOS caveat, recorded because finding it cost hours: some terminals never
// register with the OS notification service at all. When that happens no
// dialect helps and nothing reports an error — the notification is simply
// never delivered, silently, forever.
//
// kitty from nixpkgs is one such terminal. It does not appear in System
// Settings -> Notifications, meaning it has never successfully asked for
// authorization, so there is no permission available to grant.
//
// What it is NOT, each ruled out by a controlled comparison rather than by
// reasoning: the ad-hoc code signature (nixpkgs' terminal-notifier carries
// the identical defect — `codesign -dv` says Signature=adhoc, Info.plist=not
// bound — and registers fine); the process tree the notifier runs in (the
// same binary works from a login shell and from a tmux run-shell child); and
// the OSC dialect (777, 9 and 99 all behave the same). terminal-notifier
// prompts on first use and works once allowed; kitty never gets that far.
//
// `system` is the way out on such a machine.
func parseNotifyVia(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "system", "os", "desktop":
		return "system"
	case "both", "all":
		return "both"
	default:
		return "terminal"
	}
}

// notifySystemCmd builds the fallback: ask the OS itself, with no terminal
// involved. bundle is the app a click should raise, "" for none.
//
// The argv form is not a style preference, it is the escaping boundary.
// Titles and bodies are user data — session names, window names, whatever an
// agent wrote into its terminal title — and building an AppleScript SOURCE
// string out of them would make a quote in a session name into script
// injection. Passing them as arguments means the script text is a constant
// and the data never gets parsed as code.
func notifySystemCmd(title, body, bundle string) (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "osascript", []string{
			"-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
			"-e", "end run",
			"--", title, body,
		}
	default:
		return "notify-send", []string{"--", title, body}
	}
}

// notifyNotifierCmd is the PREFERRED macOS path when terminal-notifier is
// installed. herdr prefers it for one reason and it is a good one: it can
// `-activate` the hosting terminal, so clicking the notification takes you to
// the agent instead of merely telling you about it. osascript's cannot.
//
// Returns ok=false when there is nothing to gain — not macOS, or no bundle we
// can name confidently. A wrong bundle id would leave a notification that
// looks clickable and does nothing.
func notifyNotifierCmd(title, body, bundle string) (string, []string, bool) {
	if runtime.GOOS != "darwin" || bundle == "" {
		return "", nil, false
	}
	return "terminal-notifier", []string{
		"-title", title, "-message", body, "-activate", bundle,
	}, true
}

// notifySystem delivers one notification without the terminal's help,
// preferring the clickable route when it is available.
// notifyApp is winch-notify.app, baked in by the flake on darwin. Empty
// elsewhere, and empty on a non-nix darwin build, in which case the
// terminal-notifier and osascript fallbacks below still apply.
var notifyApp = ""

// notifyAppCmd is the preferred macOS route: winch's OWN bundle, so the
// banner carries winch's name and icon rather than Script Editor's or
// terminal-notifier's, and clicking it can jump tmux to the pane.
//
// target is the pane the notification is about; it and the winch path ride
// in userInfo, because the click is delivered to a RELAUNCHED copy of the
// app that has no argv and never met the process that posted.
func notifyAppCmd(title, body, bundle, sock, pane string) (string, []string, bool) {
	if notifyApp == "" {
		return "", nil, false
	}
	exe, err := os.Executable()
	if err != nil {
		exe = "winch"
	}
	args := []string{"--title", title, "--body", body, "--winch", exe}
	if sock != "" {
		args = append(args, "--socket", sock)
	}
	if pane != "" {
		args = append(args, "--pane", pane)
	}
	if bundle != "" {
		args = append(args, "--bundle", bundle)
	}
	return notifyApp + "/Contents/MacOS/winch-notify", args, true
}

func notifySystem(title, body, bundle string) error {
	return notifySystemTo(title, body, bundle, "", "")
}

func notifySystemTo(title, body, bundle, sock, pane string) error {
	if name, args, ok := notifyAppCmd(title, body, bundle, sock, pane); ok {
		if err := exec.Command(name, args...).Run(); err == nil {
			return nil
		}
		// Fall through. An unregistered bundle exits non-zero and says so on
		// stderr; sending nothing at all would be worse than sending it
		// under somebody else's name.
	}
	if name, args, ok := notifyNotifierCmd(title, body, bundle); ok {
		if path, err := exec.LookPath(name); err == nil {
			if err := exec.Command(path, args...).Run(); err == nil {
				return nil
			}
			// Fall through: a notifier that is installed but failing is not
			// a reason to send nothing.
		}
	}
	name, args := notifySystemCmd(title, body, bundle)
	path, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	return exec.Command(path, args...).Run()
}

// cmdNotifyInstall registers winch-notify.app with LaunchServices.
//
// This is the one step a nix install cannot skip, and the reason took a whole
// afternoon to find. UNUserNotificationCenter only talks to apps
// LaunchServices knows about, and it only knows about apps in the places it
// scans — /Applications, ~/Applications — never the nix store. Unregistered,
// requestAuthorization returns "Notifications are not allowed for this
// application", the app never appears in System Settings, and every
// notification fails silently.
//
// It is also the entire explanation for a puzzle that had nothing to do with
// signing: kitty uses this API and never registers from the store, while
// terminal-notifier uses the DEPRECATED NSUserNotification API, which has no
// such requirement and works from anywhere. Identical ad-hoc signatures,
// opposite outcomes.
//
// Idempotent, so a home-manager activation script can just run it.
func cmdNotifyInstall() {
	if runtime.GOOS != "darwin" {
		fmt.Println("notify-install is macOS only; elsewhere winch notifies through the terminal")
		return
	}
	if notifyApp == "" {
		fmt.Fprintln(os.Stderr, "this winch was built without winch-notify.app\n"+
			"(the flake adds it on darwin; a plain `go build` does not)")
		os.Exit(1)
	}
	if _, err := os.Stat(notifyApp); err != nil {
		fmt.Fprintf(os.Stderr, "winch-notify.app is missing at %s: %v\n", notifyApp, err)
		os.Exit(1)
	}
	const lsregister = "/System/Library/Frameworks/CoreServices.framework/" +
		"Frameworks/LaunchServices.framework/Support/lsregister"
	out, err := exec.Command(lsregister, "-f", notifyApp).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lsregister: %v\n%s", err, out)
		os.Exit(1)
	}
	fmt.Printf("registered %s\n\n", notifyApp)
	fmt.Println("Now enable it once: System Settings > Notifications > winch.")
	fmt.Println("Then check it works:  winch notify-test system")
}

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
	via     string
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
// parseNotifyOSC reads the option. Unset means AUTO — the dialect is then
// chosen per client from its TERM (oscForTerm), because two clients can be
// different terminals and a global setting cannot be right for both. Any
// explicit value pins it and turns detection off.
func parseNotifyOSC(s string) string {
	switch strings.TrimSpace(s) {
	case "9":
		return "9"
	case "99":
		return "99"
	case "777":
		return "777"
	default:
		return "auto"
	}
}

// oscForTerm picks a dialect from a client's TERM.
//
// TERM is a weaker signal than the TERM_PROGRAM/KITTY_WINDOW_ID environment
// herdr reads (detect_backend), and that is not a fixable shortcoming: the
// daemon is not the client's process and cannot see its environment. So this
// recognises the terminals that announce themselves in TERM and falls back to
// 777 for everyone else — which is the right fallback anyway, being the
// widest sequence that carries a body.
//
// kitty gets 99, its own protocol, because it is the one terminal here whose
// native form is strictly richer than the alternatives.
func oscForTerm(term string) string {
	t := strings.ToLower(term)
	switch {
	case strings.Contains(t, "kitty"):
		return "99"
	case strings.Contains(t, "ghostty"), strings.Contains(t, "wezterm"), strings.Contains(t, "iterm"):
		return "9"
	default:
		return "777"
	}
}

// resolveOSC combines the two: an explicit option always wins, so a user
// whose terminal is misdetected has a way out that does not require winch to
// be right.
func (c notifyCfg) resolveOSC(term string) string {
	if c.osc != "auto" {
		return c.osc
	}
	return oscForTerm(term)
}

// terminalBundleID maps a client's TERM to the macOS bundle its notifications
// should activate on click. Only terminals we can name confidently — a wrong
// id means terminal-notifier cannot raise the window, which is worse than not
// asking it to.
func terminalBundleID(term string) string {
	t := strings.ToLower(term)
	switch {
	case strings.Contains(t, "kitty"):
		return "net.kovidgoyal.kitty"
	case strings.Contains(t, "ghostty"):
		return "com.mitchellh.ghostty"
	case strings.Contains(t, "wezterm"):
		return "com.github.wez.wezterm"
	case strings.Contains(t, "iterm"):
		return "com.googlecode.iterm2"
	}
	return ""
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
	c := notifyCfg{osc: parseNotifyOSC(""), via: parseNotifyVia(""), delay: notifyDefaultDelay}
	c.blocked, c.done = parseNotifyMode("")
	lines, err := ctl.run("display-message -p " +
		f("#{"+optNotify+"}", "#{"+optNotifyOSC+"}", "#{"+optNotifyDelay+"}", "#{"+optNotifyVia+"}"))
	if err != nil || len(lines) != 1 {
		return c
	}
	p := strings.Split(lines[0], sep)
	if len(p) != 4 {
		return c
	}
	c.blocked, c.done = parseNotifyMode(p[0])
	c.osc, c.delay, c.via = parseNotifyOSC(p[1]), parseNotifyDelay(p[2]), parseNotifyVia(p[3])
	return c
}

func (c notifyCfg) on() bool { return c.blocked || c.done }

func (c notifyCfg) String() string {
	switch {
	case !c.on():
		return "off"
	case c.done:
		return fmt.Sprintf("blocked+done, via %s (OSC %s), %dms guard", c.via, c.osc, c.delay.Milliseconds())
	default:
		return fmt.Sprintf("blocked, via %s (OSC %s), %dms guard", c.via, c.osc, c.delay.Milliseconds())
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
		case r >= 0x80 && r <= 0x9f:
			// C1 controls. U+009C is ST — the single-character form of the
			// ESC \ that TERMINATES this very sequence, so a title carrying
			// one would end the OSC early and hand the rest to the terminal
			// as commands. Exactly the ESC hole, one encoding along, and the
			// original filter missed it because it only thought about C0.
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

// notifySuppressed decides whether one client should be spared a
// notification, given that it is looking at the agent's window and whether
// its terminal actually has the OS focus.
//
// "You can already see it" is only true if you are LOOKING. Alt-tab to a
// browser and the agent in your current tmux window is exactly as invisible
// to you as one in another session — and it was the only agent winch stayed
// quiet about. herdr draws the same distinction
// (active_tab_suppresses_notifications).
//
// focused arrives true when tmux has no better idea, which is the case
// whenever `focus-events` is off — tmux never asks the terminal to report
// focus, so the flag never moves. That degrades to the old behaviour rather
// than to silence, which is the right way round.
func notifySuppressed(lookingAtWindow, focused bool) bool {
	return lookingAtWindow && focused
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
	// `notify-test system` skips tmux entirely — no client, no tty, nothing
	// to resolve. On a machine where the terminal cannot notify (see
	// parseNotifyVia), this is the one that has to be tried.
	t := eqTmux{sock: tmuxSock}
	if parseNotifyVia(osc) == "system" {
		// The bundle only matters for the clickable route, and it comes from
		// the same TERM the dialect does — so report what was actually used,
		// including which of the two commands ran.
		bundle := terminalBundleID(notifyTestTerm(t))
		const body = "if you can see this, notifications work"
		if err := notifySystem("winch", body, bundle); err != nil {
			fmt.Fprintf(os.Stderr, "winch notify-test: %v\n", err)
			os.Exit(1)
		}
		if name, args, ok := notifyNotifierCmd("winch", body, bundle); ok {
			if _, err := exec.LookPath(name); err == nil {
				fmt.Printf("asked %s (click activates %s): %q\n", name, bundle, args)
				return
			}
		}
		name, args := notifySystemCmd("winch", body, bundle)
		fmt.Printf("asked the OS directly: %s %q\n", name, args)
		if runtime.GOOS == "darwin" && bundle != "" {
			fmt.Printf("install terminal-notifier for notifications that click\n" +
				"through to your terminal; winch prefers it when present\n")
		}
		fmt.Printf("saw it? make it the default: tmux set -g %s system\n", optNotifyVia)
		return
	}
	// display-message resolves "the current client" from the terminal that
	// invoked it, which is empty when this is run from OUTSIDE tmux — and
	// running it from outside is the normal case for a one-shot check. Fall
	// back to whichever real client is attached, since notifying is what the
	// daemon does to all of them anyway.
	tty, _ := t.out("display-message", "-p", "#{client_tty}")
	if tty == "" {
		out, _ := t.out("list-clients", "-F", "#{client_control_mode}"+sep+"#{client_tty}")
		for _, ln := range strings.Split(out, "\n") {
			if p := strings.Split(ln, sep); len(p) == 2 && p[0] != "1" && p[1] != "" {
				tty = p[1]
				break
			}
		}
	}
	if tty == "" {
		fmt.Fprintln(os.Stderr, "winch notify-test: no attached client to notify")
		os.Exit(1)
	}
	term := notifyTestTerm(t)
	kind := notifyCfg{osc: parseNotifyOSC(osc)}.resolveOSC(term)
	payload := notifyPayload(kind, "winch", "if you can read this, notifications work")
	if err := notifyTTY(tty, payload); err != nil {
		fmt.Fprintf(os.Stderr, "winch notify-test: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("sent OSC %s to %s (TERM=%s)\n  %q\n", kind, tty, term, payload)
	fmt.Printf("nothing appeared? try the other dialects, then the OS itself:\n" +
		"  winch notify-test 9\n  winch notify-test 99\n  winch notify-test system\n" +
		"then set what worked: tmux set -g " + optNotifyOSC + " <n>" +
		"  /  tmux set -g " + optNotifyVia + " system\n")
}

// notifyTestTerm is the attached client's TERM, by the same
// current-client-then-any-client rule the tty lookup uses. "" is fine: every
// consumer falls back to the safe default.
func notifyTestTerm(t eqTmux) string {
	if term, _ := t.out("display-message", "-p", "#{client_termname}"); term != "" {
		return term
	}
	out, _ := t.out("list-clients", "-F", "#{client_control_mode}"+sep+"#{client_termname}")
	for _, ln := range strings.Split(out, "\n") {
		if p := strings.Split(ln, sep); len(p) == 2 && p[0] != "1" && p[1] != "" {
			return p[1]
		}
	}
	return ""
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
