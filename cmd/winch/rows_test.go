package main

import (
	"strings"
	"testing"
)

// The name row is the card's identity, so it has to DEGRADE rather than
// vanish. The old layout dropped the title whole whenever the line
// overflowed, which at any real conversation length was always — that is
// exactly how five claude panes came to render as five copies of their
// session name.
func TestFitTokens(t *testing.T) {
	const title = "Rendering retry mechanism and per-phase model config"
	for _, c := range []struct {
		max  int
		want string
	}{
		{52, title}, // exact fit, untouched
		{60, title}, // room to spare
		// Whole words go first, and it keeps the LONGEST prefix that fits
		// rather than the first one under the limit.
		{40, "Rendering retry mechanism and per-phase…"},
		{30, "Rendering retry mechanism and…"},
		{12, "Rendering…"},
	} {
		got := fitTokens(title, c.max)
		if got != c.want {
			t.Errorf("fitTokens(max=%d) = %q, want %q", c.max, got, c.want)
		}
		if len([]rune(got)) > c.max {
			t.Errorf("fitTokens(max=%d) returned %d columns", c.max, len([]rune(got)))
		}
	}

	// A first word too long to fit has no whole-token answer; cutting it is
	// better than returning nothing, since this row is the identity.
	if got := fitTokens("Supercalifragilistic", 8); len([]rune(got)) != 8 || !strings.HasSuffix(got, "…") {
		t.Errorf("unbreakable first word = %q, want an 8-column elision", got)
	}
	if got := fitTokens("anything", 0); got != "" {
		t.Errorf("no room = %q, want empty", got)
	}
}

// herdr's `tab` token is a name you chose or a NUMBER, and it is ELIDED when
// it carries nothing: show_tab = multi_tab || !tab.is_auto_named(). tmux
// auto-renames windows to the running command, which is the same "we made
// this up" state with a noisier value — every agent window here is
// `.claude-wrapped`, which says nothing and repeats row three.
func TestTabLabelPrefersIndexOverCommandNoise(t *testing.T) {
	st := &store{
		sessions: map[string]session{"$1": {ID: "$1", Name: "main"}},
		windows: map[string]window{
			"@1": {ID: "@1", SessionID: "$1", Index: 2, Name: ".claude-wrapped"},
			"@2": {ID: "@2", SessionID: "$1", Index: 3, Name: "review"},
			"@3": {ID: "@3", SessionID: "$1", Index: 4, Name: "nvim"},
			"@4": {ID: "@4", SessionID: "$1", Index: 5, Name: ""},
		},
		panes: map[string]pane{
			"%1": {ID: "%1", WindowID: "@1", SessionID: "$1", Command: ".claude-wrapped"},
			"%2": {ID: "%2", WindowID: "@2", SessionID: "$1", Command: ".claude-wrapped"},
			// tmux names a window after its ACTIVE pane, which may be the
			// editor sitting beside the agent — still command noise, so the
			// match has to consider every pane in the window, not just the
			// agent's.
			"%3": {ID: "%3", WindowID: "@3", SessionID: "$1", Command: ".claude-wrapped"},
			"%4": {ID: "%4", WindowID: "@3", SessionID: "$1", Command: "nvim"},
		},
	}
	for _, c := range []struct{ win, want string }{
		{"@1", "2"},      // named after its own command
		{"@2", "review"}, // deliberately named
		{"@3", "4"},      // named after a sibling pane's command
		{"@4", "5"},      // unnamed
	} {
		if got := st.tabLabel("$1", c.win); got != c.want {
			t.Errorf("tabLabel(%s) = %q, want %q", c.win, got, c.want)
		}
	}
}

// A session with ONE window has nothing to distinguish, so the index — which
// would read the same on every card — is dropped. A name someone chose still
// shows: they named it for a reason, and that reason outlives the count.
func TestTabLabelElidedWhenItSaysNothing(t *testing.T) {
	lone := func(name, cmd string) *store {
		return &store{
			sessions: map[string]session{"$1": {ID: "$1", Name: "solo"}},
			windows:  map[string]window{"@1": {ID: "@1", SessionID: "$1", Index: 1, Name: name}},
			panes:    map[string]pane{"%1": {ID: "%1", WindowID: "@1", SessionID: "$1", Command: cmd}},
		}
	}
	if got := lone(".claude-wrapped", ".claude-wrapped").tabLabel("$1", "@1"); got != "" {
		t.Errorf("single auto-named window = %q, want it elided", got)
	}
	if got := lone("review", ".claude-wrapped").tabLabel("$1", "@1"); got != "review" {
		t.Errorf("single deliberately-named window = %q, want %q", got, "review")
	}
}

// baseCmd exists because this setup runs everything through nix wrappers, so
// the window name and the pane command agree only after the decoration comes
// off. Getting this wrong shows the noise instead of the index.
func TestBaseCmdStripsNixWrapping(t *testing.T) {
	for in, want := range map[string]string{
		".claude-wrapped": "claude",
		"claude":          "claude",
		".nvim-wrapped":   "nvim",
		"zsh":             "zsh",
		"":                "",
	} {
		if got := baseCmd(in); got != want {
			t.Errorf("baseCmd(%q) = %q, want %q", in, got, want)
		}
	}
}

// `n` prefills from the selected session's working directory, so the
// suggestion has to be that directory's name — not the whole path.
func TestBaseNameSuggestsTheDirectory(t *testing.T) {
	t.Setenv("HOME", "/Users/someone")
	for in, want := range map[string]string{
		"/Users/someone/dev/winch":  "winch",
		"/Users/someone/dev/winch/": "winch",
		"/Users/someone":            "~",
		"/":                         "",
		"":                          "",
	} {
		if got := baseName(in); got != want {
			t.Errorf("baseName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The create field is a row for a session that does not exist yet. It must
// never take the selection: it owns the keyboard while it is open, so a
// selection on it would mean nothing and one moving through it would strand
// the cursor on a phantom.
func TestCreateRowIsInertAndUnderTheHeading(t *testing.T) {
	save := editWhat
	t.Cleanup(func() { editWhat = save })

	st := &store{
		sessions: map[string]session{"$1": {ID: "$1", Name: "main"}},
		windows:  map[string]window{"@1": {ID: "@1", SessionID: "$1", Index: 1, Active: true}},
		panes:    map[string]pane{"%1": {ID: "%1", WindowID: "@1", SessionID: "$1", Active: true}},
	}

	editWhat = editNone
	for _, r := range st.rows(nil) {
		if r.create {
			t.Fatal("create row present with no create in progress")
		}
	}

	editWhat = editCreate
	rows := st.rows(nil)
	if !rows[0].head {
		t.Fatalf("row 0 = %+v, want the sessions heading", rows[0])
	}
	if !rows[1].create {
		t.Fatalf("row 1 = %+v, want the create field directly under the heading", rows[1])
	}
	if !rows[1].inert() {
		t.Error("the create field is selectable")
	}
}

// A spinner frame must not reach the world.
//
// Claude re-emits its whole OSC title several times a second while working —
// measured live, ⠐ to ⠂ inside two seconds. diffWorlds compares whole pane
// structs, so an ornament left in Title made every frame a pane op: JSON down
// the socket, a row rebuild, a full paint built and then discarded by the
// byte-compare at the end of paintList. Invisible, and never free.
func TestSpinnerFrameIsNotAWorldChange(t *testing.T) {
	frame := func(orn string) world {
		return world{Panes: []pane{{
			ID: "%1", WindowID: "@1", SessionID: "$1",
			Command: ".claude-wrapped", Title: agentTaskTitle(orn + " Build herdr-like tool for tmux"),
		}}}
	}
	for _, orn := range []string{"⠂", "⠐", "⠋", "◐", "✳"} {
		if ops := diffWorlds(frame("⠐"), frame(orn)); len(ops) != 0 {
			t.Errorf("ornament %q produced %d ops, want none: %+v", orn, len(ops), ops)
		}
	}
	// The name itself changing IS a world change — that is the whole point
	// of the row, so it must still get through.
	if ops := diffWorlds(frame("⠐"), world{Panes: []pane{{
		ID: "%1", WindowID: "@1", SessionID: "$1",
		Command: ".claude-wrapped", Title: agentTaskTitle("⠐ Something else entirely"),
	}}}); len(ops) != 1 {
		t.Errorf("a real title change produced %d ops, want 1", len(ops))
	}
}

// The spinner is winch's own, on winch's own clock: herdr's ten frames,
// which trace the perimeter of the braille cell, advanced 8 times a second.
// Mirroring the agent's title instead gives neither their smoothness nor a
// rate winch controls, because the detector samples titles every 300ms.
func TestSpinnerFrames(t *testing.T) {
	if len(spinners) != 10 {
		t.Fatalf("want herdr's ten frames, got %d", len(spinners))
	}
	// No frame repeats inside one rotation, and the rotation closes.
	seen := map[string]bool{}
	for i := range spinners {
		seen[spinnerFrame(i)] = true
	}
	if len(seen) != len(spinners) {
		t.Errorf("a frame repeats within one rotation: %d distinct", len(seen))
	}
	if spinnerFrame(0) != spinnerFrame(len(spinners)) {
		t.Error("the rotation does not close")
	}
	// Wraps rather than panicking far past the frame count: the counter runs
	// for as long as an agent keeps working.
	if spinnerFrame(1_000_003) == "" {
		t.Error("a large tick produced no frame")
	}
}
