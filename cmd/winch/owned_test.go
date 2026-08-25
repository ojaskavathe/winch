package main

import (
	"strings"
	"testing"
)

// These used to be rig tests. Every one of them spawned a tmux server and a pty
// client, drove the sidebar around, and then read a terminal model to decide
// whether an option had been set — which is a very expensive way to ask a
// question about a string. The policy is a pure function now, so they aren't.
//
// What still needs a rig is whether tmux RENDERS the format the way the policy
// assumes; that is a different claim and rigs/ still makes it.

// fakeReader stands in for the tmux server: it answers a claim with whatever
// the option is supposed to have held, and records what was asked.
type fakeReader struct {
	vals map[optKey][]string
	saw  []optKey
}

func (f *fakeReader) read(keys []optKey) [][]string {
	out := make([][]string, len(keys))
	for i, k := range keys {
		f.saw = append(f.saw, k)
		out[i] = f.vals[k]
	}
	return out
}

func hasCmd(cmds []string, sub string) bool {
	for _, c := range cmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func countCmd(cmds []string, sub string) int {
	n := 0
	for _, c := range cmds {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

// ------------------------------------------------------------------
// Policy: what winch wants to own
// ------------------------------------------------------------------

// Every rendered row gets the pad, not just row 0. A multi-row bar shifts as a
// block or it looks torn; a row the user left empty is skipped rather than
// filled with a pad and nothing else.
func TestDesiredPadsEveryRenderedRow(t *testing.T) {
	want := desiredOpts(optIntent{
		sess:  "$1",
		win:   "@4",
		held:  []string{"@4"},
		width: 26,
		rows:  []string{"row-zero", "", "row-two"},
	})
	var rows []string
	for _, w := range want {
		if w.key.name == "status-format" {
			rows = w.args
		}
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows set (0 and 2), got %d: %q", len(rows), rows)
	}
	if !strings.HasPrefix(rows[0], "status-format[0] ") || !strings.HasPrefix(rows[1], "status-format[2] ") {
		t.Fatalf("wrong rows written: %q", rows)
	}
	for _, r := range rows {
		if !strings.Contains(r, padWin) {
			t.Errorf("row not gated on the sidebar's window, so every other client\n"+
				"on the session gets a hole punched in its bar: %q", r)
		}
	}
	if !strings.Contains(rows[0], "row-zero") || !strings.Contains(rows[1], "row-two") {
		t.Errorf("the user's own format did not survive the wrap: %q", rows)
	}
}

// A scrub points row 0 at the billboard target and leaves every other row
// alone — the bar describes somewhere else, but it still starts past the
// sidebar, so the override is padded like anything else.
func TestDesiredScrubTakesRowZeroOnly(t *testing.T) {
	want := desiredOpts(optIntent{
		sess: "$1", win: "@4", held: []string{"@4"}, width: 26,
		rows:      []string{"row-zero", "row-one"},
		scrubWin:  "@9",
		scrubSess: "$2",
	})
	var rows []string
	for _, w := range want {
		if w.key.name == "status-format" {
			rows = w.args
		}
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %q", rows)
	}
	if strings.Contains(rows[0], "row-zero") {
		t.Errorf("row 0 still says what the session says during a scrub: %q", rows[0])
	}
	if !strings.Contains(rows[0], scrubFmtMark) {
		t.Errorf("row 0 is not the scrub override: %q", rows[0])
	}
	if !strings.Contains(rows[0], padWin) {
		t.Errorf("the override lost the pad, so the bar slides back under the sidebar: %q", rows[0])
	}
	if !strings.Contains(rows[1], "row-one") {
		t.Errorf("row 1 should be untouched by a scrub: %q", rows[1])
	}
}

// Window options apply to every window winch is HOLDING, not just the one the
// sidebar is in. A spacer-held window is still wearing the dock's shape.
// Duplicates collapse, because the caller assembles the list by concatenation.
func TestDesiredHoldsEveryHeldWindow(t *testing.T) {
	want := desiredOpts(optIntent{
		sess: "$1", win: "@4", width: 26, rows: []string{"x"},
		held: []string{"@4", "@2", "@4", ""},
	})
	seen := map[string]int{}
	for _, w := range want {
		if w.key.scope == scopeWindow {
			seen[w.key.target]++
		}
	}
	if seen["@4"] != 2 || seen["@2"] != 2 {
		t.Fatalf("want automatic-rename and pane-border-indicators on each of @2/@4, got %v", seen)
	}
	if len(seen) != 2 {
		t.Fatalf("empty or duplicate window ids leaked into the plan: %v", seen)
	}
}

// Not docked is not "own nothing new", it is "own nothing" — which is what
// makes an undock a plan rather than a special case.
func TestDesiredNothingWhenUndocked(t *testing.T) {
	if got := desiredOpts(optIntent{win: "@4", width: 26, rows: []string{"x"}}); got != nil {
		t.Fatalf("wanted options with no session: %v", got)
	}
}

// ------------------------------------------------------------------
// Mechanism: claim, install, restore
// ------------------------------------------------------------------

// A claim reads the option once and records what it found in tmux itself, so a
// daemon that dies leaves its successor everything needed to undo it.
func TestPlanClaimsOnceAndMarks(t *testing.T) {
	o := newOwner()
	k := optKey{scopeSession, "$1", "status-format"}
	r := &fakeReader{vals: map[optKey][]string{k: {`status-format[0] "theirs"`}}}
	want := []optWant{{k, []string{"status-format[0] 'ours'"}}}

	install, restore, commit := o.plan(r.read, want)
	commit()
	if len(restore) != 0 {
		t.Fatalf("a first claim gave something back: %v", restore)
	}
	if !hasCmd(install, markName("status-format")) {
		t.Fatalf("claim wrote no mark, so a crash here is unrecoverable: %v", install)
	}
	if !hasCmd(install, "'ours'") {
		t.Fatalf("claim did not install the value: %v", install)
	}
	if len(r.saw) != 1 {
		t.Fatalf("read %d times for one option: %v", len(r.saw), r.saw)
	}

	// Same want again: nothing to do, and above all no second read — re-reading
	// an option winch has already written captures winch's own value as the
	// original, which is how a pad ends up saved as the user's bar.
	install, restore, commit = o.plan(r.read, want)
	commit()
	if len(install) != 0 || len(restore) != 0 {
		t.Fatalf("re-planning an unchanged want was not a no-op: %v / %v", install, restore)
	}
	if len(r.saw) != 1 {
		t.Fatalf("re-planning re-read the option: %v", r.saw)
	}
}

// The bug this whole file exists for: the dock moves to another session, and
// the one it left has to be handed back. Nothing asks for that — it falls out
// of the old session no longer being wanted.
func TestPlanRestoresWhatItStopsWanting(t *testing.T) {
	o := newOwner()
	old := optKey{scopeSession, "$1", "status-format"}
	r := &fakeReader{vals: map[optKey][]string{old: {`status-format[0] "theirs"`}}}

	_, _, commit := o.plan(r.read, []optWant{{old, []string{"status-format[0] 'ours'"}}})
	commit()

	// Now the dock lives on $2 and nobody mentions $1 again.
	newK := optKey{scopeSession, "$2", "status-format"}
	install, restore, commit := o.plan(r.read, []optWant{{newK, []string{"status-format[0] 'ours'"}}})
	commit()

	if !hasCmd(restore, "set-option -uq -t '$1' status-format") {
		t.Fatalf("the session left behind was never unwrapped: %v", restore)
	}
	if !hasCmd(restore, `status-format[0] "theirs"`) {
		t.Fatalf("the user's own format was not replayed: %v", restore)
	}
	if !hasCmd(restore, "-uq -t '$1' "+markName("status-format")) {
		t.Fatalf("the mark outlived the claim, so a later sweep would 'restore' it again: %v", restore)
	}
	if !hasCmd(install, "-t '$2'") {
		t.Fatalf("the arriving session was not wrapped: %v", install)
	}
	if o.owns(old) {
		t.Errorf("still holding the session it gave back")
	}
}

// A claim on an option the object had nothing of its own for restores to an
// UNSET, never to the effective value: writing the effective value back pins
// the object to whatever the global happened to say at claim time, and the
// global is where a tmux.conf setting lives.
func TestPlanRestoresUnsetAsUnset(t *testing.T) {
	o := newOwner()
	k := optKey{scopeWindow, "@4", "pane-border-indicators"}
	r := &fakeReader{vals: map[optKey][]string{}} // unset at window scope

	_, _, commit := o.plan(r.read, []optWant{{k, []string{"pane-border-indicators off"}}})
	commit()
	restore := o.releaseAll()

	if !hasCmd(restore, "set-option -w -uq -t '@4' pane-border-indicators") {
		t.Fatalf("no unset emitted: %v", restore)
	}
	if countCmd(restore, "pane-border-indicators") != 1 {
		t.Fatalf("an unset option was restored to a value: %v", restore)
	}
	if !hasCmd(restore, markName("pane-border-indicators")) {
		t.Fatalf("mark not cleared: %v", restore)
	}
}

// A plan is only true once its batch lands. Recording it first and finding out
// afterwards is how a session ends up believed-released with winch's write
// still on it — nothing will ever put it back, because the registry thinks it
// already did.
func TestPlanUncommittedChangesNothing(t *testing.T) {
	o := newOwner()
	k := optKey{scopeSession, "$1", "status-format"}
	r := &fakeReader{vals: map[optKey][]string{k: {`status-format[0] "theirs"`}}}

	_, _, commit := o.plan(r.read, []optWant{{k, []string{"status-format[0] 'ours'"}}})
	commit()

	// A cross-session move whose batch fails: planned, never committed.
	other := optKey{scopeSession, "$2", "status-format"}
	_, restore, _ := o.plan(r.read, []optWant{{other, []string{"status-format[0] 'ours'"}}})
	if !hasCmd(restore, "-t '$1'") {
		t.Fatalf("expected the plan to propose giving $1 back: %v", restore)
	}
	if !o.owns(k) {
		t.Fatalf("an uncommitted plan released $1 — its pad is now stranded")
	}
	if o.owns(other) {
		t.Fatalf("an uncommitted plan claimed $2")
	}

	// And the dock is still where it was, so re-planning the unchanged state is
	// a no-op rather than a re-claim.
	install, restore, _ := o.plan(r.read, []optWant{{k, []string{"status-format[0] 'ours'"}}})
	if len(install) != 0 || len(restore) != 0 {
		t.Fatalf("recovering from a failed plan was not a no-op: %v / %v", install, restore)
	}
}

// Changing what an owned option should SAY is an install, not a claim — the
// saved original has to survive a scrub, a width drag, and a window follow.
func TestPlanRewriteKeepsTheOriginal(t *testing.T) {
	o := newOwner()
	k := optKey{scopeSession, "$1", "status-format"}
	r := &fakeReader{vals: map[optKey][]string{k: {`status-format[0] "theirs"`}}}

	_, _, commit := o.plan(r.read, []optWant{{k, []string{"status-format[0] 'pad26'"}}})
	commit()
	install, restore, commit := o.plan(r.read, []optWant{{k, []string{"status-format[0] 'pad40'"}}})
	commit()

	if len(restore) != 0 {
		t.Fatalf("a rewrite gave the option back: %v", restore)
	}
	if !hasCmd(install, "'pad40'") {
		t.Fatalf("rewrite not installed: %v", install)
	}
	if hasCmd(install, markName("status-format")) {
		t.Fatalf("rewrite re-marked, capturing winch's own pad as the original: %v", install)
	}
	if !hasCmd(o.releaseAll(), `status-format[0] "theirs"`) {
		t.Fatalf("the user's original did not survive the rewrite")
	}
}

// releaseAll is undock. It has to give back window options too, not just the
// session's — a window left with automatic-rename frozen never thaws.
func TestReleaseAllGivesBackEveryScope(t *testing.T) {
	o := newOwner()
	r := &fakeReader{vals: map[optKey][]string{}}
	_, _, commit := o.plan(r.read, desiredOpts(optIntent{
		sess: "$1", win: "@4", held: []string{"@4", "@7"}, width: 26, rows: []string{"x"},
	}))
	commit()

	restore := o.releaseAll()
	for _, want := range []string{
		"-uq -t '$1' status-format",
		"-uq -t '$1' @winch_docked",
		"-uq -t '$1' @winch_win",
		"-w -uq -t '@4' automatic-rename",
		"-w -uq -t '@4' pane-border-indicators",
		"-w -uq -t '@7' automatic-rename",
		"-w -uq -t '@7' pane-border-indicators",
	} {
		if !hasCmd(restore, want) {
			t.Errorf("undock left %s behind:\n%s", want, strings.Join(restore, "\n"))
		}
	}
	if len(o.own) != 0 {
		t.Errorf("still holding %d options after releaseAll", len(o.own))
	}
}

// The restore for an array option must unset the WHOLE array before replaying,
// or rows winch added past the end of the user's own format survive it.
func TestRestoreUnsetsBeforeReplaying(t *testing.T) {
	got := restoreCmds(optKey{scopeSession, "$1", "status-format"},
		[]string{`status-format[0] "theirs"`})
	if len(got) < 3 {
		t.Fatalf("want unset, mark clear, replay; got %v", got)
	}
	if !strings.Contains(got[0], "-uq") || strings.Contains(got[0], "[0]") {
		t.Errorf("first command should unset the whole array, got %q", got[0])
	}
	if idx := strings.Index(strings.Join(got, "\n"), `"theirs"`); idx < strings.Index(strings.Join(got, "\n"), "-uq") {
		t.Errorf("replay ran before the unset that would have wiped it: %v", got)
	}
}

// tmux user options cannot contain a hyphen, and every option winch owns does.
func TestMarkNameIsAValidUserOption(t *testing.T) {
	for _, k := range ownedOptions {
		got := markName(k.name)
		if !strings.HasPrefix(got, "@winch_saved_") {
			t.Errorf("%s: mark %q is not in winch's namespace", k.name, got)
		}
		for _, r := range strings.TrimPrefix(got, "@") {
			ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !ok {
				t.Errorf("%s: mark %q holds %q, which tmux will not accept", k.name, got, r)
			}
		}
	}
}

// status-format ships three entries whatever `status` says, so the row count
// has to come from `status` — padding rows tmux never renders writes formats
// that reappear the moment the user turns a second row on.
func TestStatusRowCount(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{"", 1}, {"on", 1}, {"1", 1},
		{"off", 0}, {"0", 0},
		{"2", 2}, {"5", 5},
		{" 3 ", 3},
		{"6", 1},   // out of tmux's range: treat as the default rather than trusting it
		{"yes", 1}, // not a number tmux would accept either
		{"-1", 1},
	} {
		if got := statusRowCount(c.in); got != c.want {
			t.Errorf("statusRowCount(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
