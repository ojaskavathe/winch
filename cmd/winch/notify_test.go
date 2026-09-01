package main

import (
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// The three dialects, byte for byte. Written out literally rather than built
// from the same helpers the code uses, because a test that computes its
// expectation the way the code does cannot catch the code being wrong about
// the protocol — and the protocol is the whole feature here.
func TestNotifyPayloadDialects(t *testing.T) {
	const (
		esc = "\033"
		st  = "\033\\"
	)
	for _, c := range []struct {
		osc, want string
	}{
		// kitty splits on the first ';' after `notify;`: title, then body.
		{"777", esc + "]777;notify;claude needs you;work:beta" + st},
		// No body field exists in OSC 9, so the body is folded into the text
		// rather than thrown away.
		{"9", esc + "]9;claude needs you — work:beta" + st},
		// Two payloads, one identifier: d=0 says more is coming, d=1 closes.
		{"99", esc + "]99;i=winch:d=0:p=title;claude needs you" + st +
			esc + "]99;i=winch:d=1:p=body;work:beta" + st},
	} {
		if got := notifyPayload(c.osc, "claude needs you", "work:beta"); got != c.want {
			t.Errorf("OSC %s\n got %q\nwant %q", c.osc, got, c.want)
		}
	}

	// An unknown dialect must not produce a half-formed sequence — it falls
	// back to the default, which is the one most terminals accept.
	if got := notifyPayload("garbage", "a", "b"); !strings.HasPrefix(got, "\033]777;notify;") {
		t.Errorf("unknown dialect = %q, want the 777 fallback", got)
	}
}

// Titles and bodies are USER DATA: session names, window names, and whatever
// an agent wrote into its terminal title. An ESC in any of them would close
// the OSC early and hand the rest of the string to the terminal as commands.
// This is the one place in winch where untrusted text is written straight to
// a terminal, so it gets a test of its own.
func TestNotifyPayloadCannotBeEscaped(t *testing.T) {
	evil := "claude\033]0;pwned\007 and \033[31mred"
	for _, osc := range []string{"777", "9", "99"} {
		got := notifyPayload(osc, evil, evil)
		// Exactly the escapes the payload itself is made of, and no more.
		if n := strings.Count(got, "\033"); n != wantESC(osc) {
			t.Errorf("OSC %s: %d ESC bytes in %q, want %d", osc, n, got, wantESC(osc))
		}
		if strings.ContainsRune(got, '\a') {
			t.Errorf("OSC %s: BEL survived: %q", osc, got)
		}
	}

	// 777 uses ';' as its field separator, so a session named "a;b" would
	// otherwise push half the name into the body silently.
	if got := notifyPayload("777", "a;b", "c;d"); got != "\033]777;notify;a,b;c,d\033\\" {
		t.Errorf("semicolons not neutralised for 777: %q", got)
	}
	// The other two do not split on ';', so they must NOT mangle it.
	if got := notifyPayload("9", "a;b", ""); got != "\033]9;a;b\033\\" {
		t.Errorf("OSC 9 mangled a legitimate semicolon: %q", got)
	}

	// U+009C is ST — the single-character form of the ESC \ that terminates
	// this very sequence. Filtering C0 is not enough: this is the same hole
	// one encoding along, and it survives as the two UTF-8 bytes C2 9C.
	for _, osc := range []string{"777", "9", "99"} {
		got := notifyPayload(osc, "beforeafter", "body")
		if strings.ContainsRune(got, '') || strings.Contains(got, "\xc2\x9c") {
			t.Errorf("OSC %s: C1 ST survived: %q", osc, got)
		}
	}
	// And the rest of C1 with it — none of it is text anyone meant to send.
	if got := notifyPayload("777", "abc", ""); !strings.Contains(got, "abc") {
		t.Errorf("C1 range not stripped: %q", got)
	}

	// Unbounded titles are a denial of service against the notification
	// daemon, not just ugly.
	long := strings.Repeat("x", 500)
	if got := notifyPayload("9", long, ""); len([]rune(got)) > 140 {
		t.Errorf("500-char title produced %d runes", len([]rune(got)))
	}
}

// wantESC is how many ESC bytes a well-formed payload of each dialect has:
// one to open and one for the ST, doubled for 99's two commands.
func wantESC(osc string) int {
	if osc == "99" {
		return 4
	}
	return 2
}

// The flap guard. herdr re-checks after a second precisely because agents
// flicker through blocked on their own, and a notification you cannot act on
// before it is already stale is worse than silence.
func TestNotifyRipe(t *testing.T) {
	base := time.Unix(1700000000, 0)
	armed := pendingNote{state: "blocked", at: base}
	const delay = time.Second

	for _, c := range []struct {
		name       string
		cur        string
		at         time.Time
		fire, drop bool
	}{
		{"still blocked, guard not elapsed", "blocked", base.Add(300 * time.Millisecond), false, false},
		{"still blocked, guard elapsed", "blocked", base.Add(delay), true, true},
		{"answered before the guard", "idle", base.Add(300 * time.Millisecond), false, true},
		{"answered after the guard", "idle", base.Add(2 * time.Second), false, true},
		{"pane died", "", base.Add(2 * time.Second), false, true},
		// A second turn of the SAME state is a different event and gets
		// re-armed by notifyArm; ripeness never sees it as continuous.
		{"moved on to working", "working", base.Add(2 * time.Second), false, true},
	} {
		fire, drop := notifyRipe(armed, c.cur, c.at, delay)
		if fire != c.fire || drop != c.drop {
			t.Errorf("%s: fire=%v drop=%v, want fire=%v drop=%v",
				c.name, fire, drop, c.fire, c.drop)
		}
	}

	// Zero delay is what the rigs run with: it must fire on the very first
	// look, not one tick later.
	if fire, drop := notifyRipe(armed, "blocked", base, 0); !fire || !drop {
		t.Errorf("delay=0 did not fire immediately (fire=%v drop=%v)", fire, drop)
	}
}

func TestNotifyConfigDefaults(t *testing.T) {
	// Unset means blocked-only. The default has to be the notification you
	// would have asked for, and turn-end is one per turn per agent.
	if b, d := parseNotifyMode(""); !b || d {
		t.Errorf("unset = blocked:%v done:%v, want blocked only", b, d)
	}
	if b, d := parseNotifyMode("off"); b || d {
		t.Errorf("off = blocked:%v done:%v, want neither", b, d)
	}
	if b, d := parseNotifyMode("ALL"); !b || !d {
		t.Errorf("all = blocked:%v done:%v, want both", b, d)
	}
	if got := parseNotifyOSC(" 9 "); got != "9" {
		t.Errorf("osc = %q, want 9", got)
	}
	// Unset and unrecognised both mean AUTO now — the dialect is resolved per
	// client from its TERM, since two clients can be two terminals.
	for _, in := range []string{"", "nonsense"} {
		if got := parseNotifyOSC(in); got != "auto" {
			t.Errorf("parseNotifyOSC(%q) = %q, want auto", in, got)
		}
	}
	if got := parseNotifyDelay("250"); got != 250*time.Millisecond {
		t.Errorf("delay = %v, want 250ms", got)
	}
	// Zero is legitimate (rigs, and anyone who wants no guard at all); junk
	// and negatives are not, and must not disable the guard by accident.
	if got := parseNotifyDelay("0"); got != 0 {
		t.Errorf("delay 0 = %v, want 0", got)
	}
	for _, bad := range []string{"", "soon", "-5"} {
		if got := parseNotifyDelay(bad); got != notifyDefaultDelay {
			t.Errorf("delay %q = %v, want the default", bad, got)
		}
	}
}

// The system path hands the same untrusted text to a SHELL COMMAND rather
// than a terminal, which is a different injection surface with a worse
// blast radius: on macOS the notifier is AppleScript, so a quote in a
// session name embedded in script source would be executable code.
//
// The defence is structural — the script text is a constant and the data
// travels as argv — so the test asserts the structure rather than trying to
// enumerate what needs escaping.
func TestNotifySystemPassesUserDataAsArguments(t *testing.T) {
	evil := `"; display dialog "pwned"; "`
	body := `back\slash and 'quotes'`
	name, args := notifySystemCmd(evil, body, "net.kovidgoyal.kitty")

	if name == "" || len(args) < 3 {
		t.Fatalf("notifySystemCmd = %q %q, want a command and arguments", name, args)
	}
	// The data is the last two arguments, verbatim — not escaped, not
	// mangled, and not interpolated into anything.
	if got := args[len(args)-2]; got != evil {
		t.Errorf("title argument = %q, want it passed through unchanged", got)
	}
	if got := args[len(args)-1]; got != body {
		t.Errorf("body argument = %q, want it passed through unchanged", got)
	}
	// And it appears nowhere in the script that precedes them.
	script := args[:len(args)-2]
	for _, a := range script {
		if strings.Contains(a, "pwned") || strings.Contains(a, evil) {
			t.Errorf("user data reached the script body: %q", a)
		}
	}
	// `--` has to separate the script from the data, or a title that begins
	// with a dash parses as an option to the notifier.
	end := false
	for _, a := range script {
		if a == "--" {
			end = true
		}
	}
	if !end {
		t.Error("no `--` before the user data: a title starting with - would parse as a flag")
	}
}

// The dialect is a property of the TERMINAL, not of the machine — two
// clients on one server can be two different emulators. herdr reads
// TERM_PROGRAM and KITTY_WINDOW_ID; the daemon is not the client's process
// and cannot see its environment, so TERM is what we get.
func TestOSCForTerm(t *testing.T) {
	for term, want := range map[string]string{
		"xterm-kitty":    "99", // kitty's own protocol, strictly richer
		"xterm-ghostty":  "9",
		"wezterm":        "9",
		"iterm2":         "9",
		"xterm-256color": "777", // says nothing: fall back to the widest with a body
		"screen":         "777",
		"":               "777",
	} {
		if got := oscForTerm(term); got != want {
			t.Errorf("oscForTerm(%q) = %q, want %q", term, got, want)
		}
	}

	// An explicit option always wins, so a misdetected terminal has a way out
	// that does not require winch to be right about it.
	pinned := notifyCfg{osc: "9"}
	if got := pinned.resolveOSC("xterm-kitty"); got != "9" {
		t.Errorf("explicit 9 on kitty = %q, want the pin to win", got)
	}
	auto := notifyCfg{osc: "auto"}
	if got := auto.resolveOSC("xterm-kitty"); got != "99" {
		t.Errorf("auto on kitty = %q, want 99", got)
	}
}

// The clickable path. A wrong bundle id is worse than none: the notification
// still appears but clicking it silently does nothing, so unknown terminals
// must produce no id and no attempt.
func TestTerminalBundleAndNotifierCommand(t *testing.T) {
	if got := terminalBundleID("xterm-kitty"); got != "net.kovidgoyal.kitty" {
		t.Errorf("kitty bundle = %q", got)
	}
	for _, term := range []string{"xterm-256color", "screen", "", "dumb"} {
		if got := terminalBundleID(term); got != "" {
			t.Errorf("terminalBundleID(%q) = %q, want none for a terminal we cannot name", term, got)
		}
	}

	// No bundle, no clickable attempt — on any platform.
	if _, _, ok := notifyNotifierCmd("t", "b", ""); ok {
		t.Error("offered the clickable route with no bundle to activate")
	}
	if runtime.GOOS == "darwin" {
		name, args, ok := notifyNotifierCmd("t", "b", "net.kovidgoyal.kitty")
		if !ok || name != "terminal-notifier" {
			t.Fatalf("darwin with a bundle = %q %v %v", name, args, ok)
		}
		if !slices.Contains(args, "-activate") || !slices.Contains(args, "net.kovidgoyal.kitty") {
			t.Errorf("args %v carry no -activate; the click is the entire reason to prefer it", args)
		}
	} else if _, _, ok := notifyNotifierCmd("t", "b", "net.kovidgoyal.kitty"); ok {
		t.Error("offered terminal-notifier off darwin")
	}
}

// "You can already see it" is only true if you are LOOKING. Alt-tab away and
// the agent in your current tmux window is as invisible as any other — and it
// was the only one winch stayed quiet about.
func TestNotifySuppressed(t *testing.T) {
	for _, c := range []struct {
		looking, focused, want bool
		why                    string
	}{
		{true, true, true, "on the window and looking at the terminal"},
		{true, false, false, "on the window but alt-tabbed away"},
		{false, true, false, "another window entirely"},
		{false, false, false, "another window, and not even looking"},
	} {
		if got := notifySuppressed(c.looking, c.focused); got != c.want {
			t.Errorf("%s: suppressed=%v, want %v", c.why, got, c.want)
		}
	}
}

func TestNotifyViaDefaults(t *testing.T) {
	// terminal is the default because it is the one that follows you over
	// ssh. system is the escape hatch for a machine whose terminal cannot
	// notify at all — see parseNotifyVia's comment for how that happens.
	for in, want := range map[string]string{
		"": "terminal", "terminal": "terminal", "nonsense": "terminal",
		"system": "system", "SYSTEM": "system", " os ": "system",
		"both": "both", "all": "both",
	} {
		if got := parseNotifyVia(in); got != want {
			t.Errorf("parseNotifyVia(%q) = %q, want %q", in, got, want)
		}
	}
}

// notifyTTY writes to a path taken from tmux. Refusing anything outside /dev
// keeps a malformed or hostile client_tty from turning a notification into a
// file write.
func TestNotifyTTYRefusesNonDevices(t *testing.T) {
	for _, bad := range []string{"", "/tmp/notatty", "relative/path", "/etc/passwd"} {
		if err := notifyTTY(bad, "x"); err == nil {
			t.Errorf("notifyTTY(%q) succeeded, want a refusal", bad)
		}
	}
}
