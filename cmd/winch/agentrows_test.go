package main

import (
	"strings"
	"testing"
)

func rowStore() *store {
	st := &store{
		sessions: map[string]session{"$1": {ID: "$1", Name: "sr"}},
		windows:  map[string]window{"@1": {ID: "@1", SessionID: "$1", Index: 1, Name: "api"}},
		panes:    map[string]pane{},
	}
	return st
}

func agentPane(state, reason string) pane {
	return pane{
		ID: "%1", WindowID: "@1", SessionID: "$1", Command: "claude",
		Agent: "claude", AgentState: state, AgentReason: reason,
		Title: agentTaskTitle("✳ Fix the OAuth revoke 400s"),
	}
}

func headOf(t *testing.T, r agentRows, p pane) string {
	t.Helper()
	st := rowStore()
	vals := r.values(r.rows[0], st, p)
	label, _ := fitAgentRow(vals, 60, true, pal.subtext)
	return strings.TrimSpace(label)
}

// The default layout must render exactly as it did before rows became
// configurable for the states the colour ladder already carries — otherwise
// "configurable" silently means "changed for everyone".
func TestDefaultRowsAreUnchangedForLadderStates(t *testing.T) {
	d := defaultAgentRows()
	for _, state := range []string{"working", "idle", "done"} {
		if got := headOf(t, d, agentPane(state, "")); got != "sr · api · claude" {
			t.Errorf("%s: head = %q, want the old %q", state, got, "sr · api · claude")
		}
	}
}

// ...and must say the word for the two the glyph cannot explain.
func TestDefaultRowsNameTheUnobviousStates(t *testing.T) {
	d := defaultAgentRows()
	if got := headOf(t, d, agentPane("background", "shell still running")); got != "background · sr · api · claude" {
		t.Errorf("background head = %q", got)
	}
	if got := headOf(t, d, agentPane("blocked", "permission prompt")); got != "blocked · sr · api · claude" {
		t.Errorf("blocked head = %q", got)
	}
}

// An explicit layout is not second-guessed: ask for the word and you get it
// on every state, including the ones the default suppresses.
func TestExplicitRowsAlwaysShowTheState(t *testing.T) {
	r, why := parseAgentRows("state_text workspace tab agent | title")
	if why != "" || !r.explicit {
		t.Fatalf("parse: why=%q explicit=%v", why, r.explicit)
	}
	if got := headOf(t, r, agentPane("working", "")); got != "working · sr · api · claude" {
		t.Errorf("explicit working head = %q", got)
	}
}

func TestParseAgentRows(t *testing.T) {
	if r, why := parseAgentRows("workspace | title"); why != "" || len(r.rows) != 2 {
		t.Errorf("two rows: why=%q rows=%v", why, r.rows)
	}
	// A typo must not render a card with a hole in it.
	r, why := parseAgentRows("workspace | titel")
	if why == "" {
		t.Error("typo accepted silently")
	}
	if r.explicit {
		t.Error("fallback marked explicit; it would stop suppressing redundant state")
	}
	if len(r.rows) != len(defaultAgentRows().rows) {
		t.Errorf("fallback is not the default: %v", r.rows)
	}
	// Empty means default, not empty.
	if r, why := parseAgentRows("   "); why != "" || len(r.rows) == 0 || r.explicit {
		t.Errorf("blank option: why=%q rows=%v explicit=%v", why, r.rows, r.explicit)
	}
}

// The ambient rule paintList depends on: a leading ambient token emits no
// colour of its own, so the row inherits active/selected styling; the same
// token later has to restore it, because something ahead of it set a colour.
func TestAmbientTokenOnlyCostsACodeWhenItMust(t *testing.T) {
	st := rowStore()
	d := defaultAgentRows()

	// working: state suppressed, so workspace leads and must be bare.
	vals := d.values(d.rows[0], st, agentPane("working", ""))
	_, styled := fitAgentRow(vals, 60, true, pal.text)
	if !strings.HasPrefix(styled, "   sr") {
		t.Errorf("leading workspace carries a colour: %q", styled)
	}

	// background: the state word leads and is coloured, so the workspace
	// after it must restore the ambient or it inherits peach.
	vals = d.values(d.rows[0], st, agentPane("background", ""))
	_, styled = fitAgentRow(vals, 60, true, pal.text)
	if !strings.Contains(styled, pal.peach+"background") {
		t.Errorf("state word not in its colour: %q", styled)
	}
	if !strings.Contains(styled, pal.text+"sr") {
		t.Errorf("workspace did not restore ambient: %q", styled)
	}
}

// Head rows drop trailing context; identity rows truncate instead. A card
// that loses its conversation name to a narrow sidebar has lost the thing
// the card is for.
func TestNarrowRowsDropTailButKeepIdentity(t *testing.T) {
	st := rowStore()
	d := defaultAgentRows()
	p := agentPane("background", "")

	head, _ := fitAgentRow(d.values(d.rows[0], st, p), 14, true, pal.subtext)
	if strings.Contains(head, "claude") {
		t.Errorf("narrow head kept the tail: %q", head)
	}
	if !strings.Contains(head, "background") {
		t.Errorf("narrow head dropped the state: %q", head)
	}

	body, _ := fitAgentRow(d.values(d.rows[1], st, p), 14, false, pal.subtext)
	if strings.TrimSpace(body) == "" {
		t.Error("identity row vanished on a narrow sidebar")
	}
}

func TestStateColoursMatchHerdr(t *testing.T) {
	// herdr ui/status.rs:237 — blocked red, working yellow, done teal,
	// idle green. background is ours and takes the glyph's peach.
	for state, want := range map[string]string{
		"blocked": pal.red, "working": pal.yellow, "done": pal.teal,
		"idle": pal.green, "background": pal.peach,
	} {
		if got := stateColour(state); got != want {
			t.Errorf("stateColour(%s) = %q want %q", state, got, want)
		}
	}
	if stateColour("nonsense") != "" {
		t.Error("unknown state got a colour")
	}
}

// A title that itself contains " · " must render whole. Claude's --resume
// picker sets the terminal title to literally "claude · resume"; the identity
// row used to join its tokens with " · ", fit, then split back on " · ",
// which sliced that title in two and dropped the tail — the card showed a
// bare "claude" and the unpainted remainder bled stale cells. Regression:
// the rendered identity row must be the full title, at any width that fits.
func TestIdentityRowKeepsTitleContainingSeparator(t *testing.T) {
	st := rowStore()
	p := agentPane("idle", "")
	p.Title = agentTaskTitle("claude · resume")

	d := defaultAgentRows()
	// row 1 of the default layout is the identity (title) row.
	vals := d.values(d.rows[1], st, p)
	_, styled := fitAgentRow(vals, 60, false, pal.subtext)
	got := strings.TrimSpace(stripSGR(styled))
	if got != "claude · resume" {
		t.Fatalf("identity row sliced a title on its own separator: got %q, want %q", got, "claude · resume")
	}
}

// The same round-trip must not corrupt a MULTI-token identity row whose title
// component contains " · ". With tokens [tab, title] and title "claude ·
// resume", the old split produced [tabval, claude, resume] and the clamp
// dropped "resume"; both the tab and the whole title must survive.
func TestMultiTokenIdentityRowWithSeparatorTitle(t *testing.T) {
	st := rowStore()
	p := agentPane("idle", "")
	p.Title = agentTaskTitle("claude · resume")

	r := agentRows{rows: [][]string{{tokAgent, tokTitle}}, explicit: true}
	vals := r.values(r.rows[0], st, p)
	_, styled := fitAgentRow(vals, 60, false, pal.subtext)
	got := strings.TrimSpace(stripSGR(styled))
	if !strings.Contains(got, "claude · resume") {
		t.Fatalf("multi-token identity row lost a separator-bearing title: got %q", got)
	}
}
