package main

import (
	"strings"
	"testing"
)

func TestNavByte(t *testing.T) {
	cases := []struct {
		name string
		want byte
		ok   bool
	}{
		{"C-j", 0x0a, true},
		{"C-k", 0x0b, true},
		{"C-h", 0x08, true},
		{"C-l", 0x0c, true},
		{"C-a", 0x01, true},
		{"C-z", 0x1a, true},
		{"c-j", 0x0a, true}, // tmux accepts either case
		{"j", 'j', true},
		{"/", '/', true},
		// Not supported, and must resolve to "no" rather than to byte 0:
		// a false positive here would bind the sidebar to a byte the user
		// never presses, or worse, to every unrecognised key at once.
		{"", 0, false},
		{"M-j", 0, false},
		{"Up", 0, false},
		{"Down", 0, false},
		{"C-", 0, false},
		{"C-1", 0, false},
		{"F1", 0, false},
	}
	for _, c := range cases {
		got, ok := navByte(c.name)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("navByte(%q) = %#x,%v; want %#x,%v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestNavHitNeverMatchesUnset(t *testing.T) {
	// The trap this guards: an unset key resolving to byte 0 and then
	// matching C-Space, or matching every byte through a zero-value compare.
	for b := 0; b < 256; b++ {
		if navHit("", byte(b)) {
			t.Fatalf("empty nav key matched byte %#x", b)
		}
		if navHit("Up", byte(b)) {
			t.Fatalf("unsupported nav key matched byte %#x", b)
		}
	}
	if !navHit("C-j", 0x0a) {
		t.Error("C-j should match 0x0a")
	}
	if navHit("C-j", 0x0b) {
		t.Error("C-j must not match 0x0b")
	}
}

func TestParseNavKeys(t *testing.T) {
	got, err := parseNavKeys("C-h,C-j,C-k,C-l")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := navKeys{Left: "C-h", Down: "C-j", Up: "C-k", Right: "C-l"}
	if got != want {
		t.Errorf("got %+v want %+v", got, want)
	}

	// Whitespace tolerated, "-" leaves a direction unbound.
	got, err = parseNavKeys(" C-h , C-j , - , C-l ")
	if err != nil {
		t.Fatalf("parse with gaps: %v", err)
	}
	if got.Up != "" {
		t.Errorf("`-` should leave up unbound, got %q", got.Up)
	}
	if got.Down != "C-j" {
		t.Errorf("down = %q", got.Down)
	}

	for _, bad := range []string{"C-h,C-j,C-k", "C-h,C-j,C-k,C-l,C-m", "", "C-h,C-j,C-k,M-l"} {
		if _, err := parseNavKeys(bad); err == nil {
			t.Errorf("parseNavKeys(%q) should have failed", bad)
		}
	}
}

func TestSplitRootBind(t *testing.T) {
	key, cmd, ok := splitRootBind(`bind-key  -T root C-j  if-shell -F "#{m/r:^winch$,#{pane_current_command}}" "send-keys C-j" "select-pane -D"`)
	if !ok || key != "C-j" {
		t.Fatalf("key = %q ok = %v", key, ok)
	}
	if want := "select-pane -D"; !contains(cmd, want) {
		t.Errorf("cmd %q should contain %q", cmd, want)
	}

	// A prefix-table line must not be mistaken for a root one.
	if _, _, ok := splitRootBind(`bind-key -T prefix j select-pane -D`); ok {
		t.Error("prefix-table bind should not parse as root")
	}
	if _, _, ok := splitRootBind(`# a comment`); ok {
		t.Error("garbage should not parse")
	}
}

// detectNavKeys' parsing half, exercised without a tmux server: the mapping
// from "what does this bind DO" to "which key is it".
func TestDetectNavKeysFromLines(t *testing.T) {
	lines := []string{
		`bind-key  -T root C-h  if-shell -F "#{m/r:^winch$,#{pane_current_command}}" "send-keys C-h" "select-pane -L"`,
		`bind-key  -T root C-j  if-shell -F "#{m/r:^winch$,#{pane_current_command}}" "send-keys C-j" "select-pane -D"`,
		`bind-key  -T root C-k  if-shell -F "#{m/r:^winch$,#{pane_current_command}}" "send-keys C-k" "select-pane -U"`,
		`bind-key  -T root C-l  if-shell -F "#{m/r:^winch$,#{pane_current_command}}" "send-keys C-l" "select-pane -R"`,
		// Noise that must not be picked up: the default mouse menu contains
		// swap-pane -D, and a prefixed binding can never reach the sidebar.
		`bind-key -T root MouseDown3Pane display-menu "Swap Down" d { swap-pane -D } Kill X { kill-pane }`,
		`bind-key -T prefix Down select-pane -D`,
	}
	got := navFromLines(lines)
	want := navKeys{Left: "C-h", Down: "C-j", Up: "C-k", Right: "C-l"}
	if got != want {
		t.Errorf("got %+v want %+v", got, want)
	}

	// A plain (non-navigator) config resolves just as well.
	got = navFromLines([]string{
		`bind-key -T root M-Up select-pane -U`,
		`bind-key -T root C-w select-pane -D`,
	})
	if got.Down != "C-w" {
		t.Errorf("down = %q, want C-w", got.Down)
	}
	if got.Up != "M-Up" {
		t.Errorf("up = %q; detection records the name, resolved() is what drops it", got.Up)
	}
	if got.resolved().Up != "" {
		t.Errorf("resolved().Up = %q, want empty (M-Up is not matchable)", got.resolved().Up)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestStyleField(t *testing.T) {
	cases := []struct{ style, name, want string }{
		{"bg=yellow,fg=black,fill=yellow", "fill", "yellow"},
		{"bg=yellow,fg=black,fill=yellow", "bg", "yellow"},
		{"fg=#94e2d5,bg=default,align=centre", "bg", ""}, // default names no colour
		{"fg=#94e2d5,bg=default,align=centre", "fill", ""},
		{" fg=black , bg=#f9e2af ", "bg", "#f9e2af"},
		{"", "bg", ""},
		{"bg=", "bg", ""},
		// bg= must not be found by a search for fill=, nor the reverse.
		{"bg=red", "fill", ""},
		{"fill=red", "bg", ""},
	}
	for _, c := range cases {
		if got := styleField(c.style, c.name); got != c.want {
			t.Errorf("styleField(%q, %q) = %q want %q", c.style, c.name, got, c.want)
		}
	}
}

// The confinement must carry exactly one fill= and one width=/align=, whatever
// the user's original said — a style that accumulated a directive per dock
// would grow without bound.
func TestMsgStyleConfinedIsIdempotent(t *testing.T) {
	base := "bg=yellow,fg=black,fill=yellow,align=centre,width=40"
	got := msgStyleConfined(base, "yellow", 200, 26)
	if got == "" {
		t.Fatal("expected a confined style")
	}
	for _, d := range []string{"fill=", "width=", "align="} {
		if n := strings.Count(got, d); n != 1 {
			t.Errorf("%q appears %d times in %q, want 1", d, n, got)
		}
	}
	if !strings.Contains(got, "width=173") { // 200 - 26 - 1
		t.Errorf("width wrong in %q", got)
	}
	if !strings.Contains(got, "fg=black") {
		t.Errorf("user's own colours dropped from %q", got)
	}

	// Feeding the result back in must not stack another set on top.
	again := msgStyleConfined(got, "yellow", 200, 26)
	for _, d := range []string{"fill=", "width=", "align="} {
		if n := strings.Count(again, d); n != 1 {
			t.Errorf("after re-confining, %q appears %d times in %q", d, n, again)
		}
	}

	// No fill resolved: the area is left uncleared rather than given a
	// meaningless directive.
	if s := msgStyleConfined("fg=red", "", 200, 26); strings.Contains(s, "fill=") {
		t.Errorf("empty fill should add no directive, got %q", s)
	}

	// Too narrow to confine: nothing is claimed at all.
	if s := msgStyleConfined(base, "yellow", 30, 26); s != "" {
		t.Errorf("want no confinement at 30 cols, got %q", s)
	}
}

// A style tmux rejects does not merely look wrong: set-option validates styles
// and tmux aborts a command sequence at the first error, and these options ride
// in the same batch as the dock — so a bad one stops the sidebar opening.
func TestConfinementNeverEmitsAnUnparseableStyle(t *testing.T) {
	// Formats are left ENTIRELY alone: their commas are not separators, and
	// tmux skips validation for anything containing #{, so a mangled one is
	// accepted at set time and fails later at draw time where nothing reports
	// it.
	fmts := []string{
		"#{?pane_in_mode,fg=red,fg=blue}",
		"fg=green,#{?client_prefix,bg=red,bg=blue}",
	}
	for _, f := range fmts {
		if got := msgStyleConfined(f, "red", 200, 26); got != "" {
			t.Errorf("msgStyleConfined(%q) = %q, want none", f, got)
		}
		if got := cmdStyleFilled(f, "red"); got != "" {
			t.Errorf("cmdStyleFilled(%q) = %q, want none", f, got)
		}
	}

	// A fill is only emitted for something certain to parse as a colour.
	for _, c := range []string{"#181825", "colour234", "42", "red", "brightwhite", "BLACK"} {
		if !isColour(c) {
			t.Errorf("isColour(%q) = false, want true", c)
		}
	}
	for _, c := range []string{"", "default", "terminal", "#18182", "#gggggg",
		"colour", "colour1234", "puce", "#{?x,a,b}"} {
		if isColour(c) {
			t.Errorf("isColour(%q) = true, want false", c)
		}
	}

	// Unresolvable fill -> no fill directive at all, which is the behaviour
	// that existed before the fill was added and is safe.
	if got := fillFor("fg=red,bg=default", ""); got != "" {
		t.Errorf("fillFor with nothing resolvable = %q, want empty", got)
	}
	if got := fillFor("fg=red,bg=default", "puce"); got != "" {
		t.Errorf("fillFor must reject a non-colour status bg, got %q", got)
	}
	// Precedence: own fill, then own bg, then the bar's.
	if got := fillFor("bg=blue,fill=green", "#181825"); got != "green" {
		t.Errorf("own fill should win, got %q", got)
	}
	if got := fillFor("bg=blue", "#181825"); got != "blue" {
		t.Errorf("own bg should be next, got %q", got)
	}
	if got := fillFor("bg=default", "#181825"); got != "#181825" {
		t.Errorf("status bg should be the fallback, got %q", got)
	}
}
