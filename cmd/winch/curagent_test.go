package main

import "testing"

// The sidebar has to say which agent you are IN, not merely which one the
// cursor sits on: herdr hangs its whole ladder off is_active_pane
// (src/ui/sidebar.rs:1503), and without it five agent cards render
// identically no matter where you are.
//
// Reading tmux's two flags is the easy half. The hard half is that DOCKING
// STEALS THE FOCUS — winch's sidebar is a real pane, so the instant you open
// the list the window's active pane is winch itself, and a live reading
// blanks the highlight at exactly the moment you are looking at it. So the
// value is sticky, and every case below is a way stickiness can go wrong:
// too sticky (follows you into another session), not sticky enough (dies on
// dock), or sticky about the wrong pane (an agent in a window you are not on).
func TestCurrentAgentIsStickyWhileTheSidebarHasFocus(t *testing.T) {
	saveSess, saveAgent := curSess, curAgent
	t.Cleanup(func() { curSess, curAgent = saveSess, saveAgent })

	st := &store{
		sessions: map[string]session{
			"$1": {ID: "$1", Name: "work"},
			"$2": {ID: "$2", Name: "play"},
		},
		windows: map[string]window{
			"@1": {ID: "@1", SessionID: "$1", Index: 1, Active: true},
			"@2": {ID: "@2", SessionID: "$1", Index: 2},
			"@3": {ID: "@3", SessionID: "$2", Index: 1, Active: true},
		},
		panes: map[string]pane{
			// The agent you are in.
			"%1": {ID: "%1", WindowID: "@1", SessionID: "$1", Active: true,
				Agent: "claude", AgentState: "idle"},
			// The sidebar, in the same window — no agent, and it is what
			// takes Active away from %1 the moment you dock.
			"%2": {ID: "%2", WindowID: "@1", SessionID: "$1", Command: "winch"},
			// Active in ITS window, but that window is not the session's
			// current one. Being active in a window nobody is looking at
			// means nothing.
			"%3": {ID: "%3", WindowID: "@2", SessionID: "$1", Active: true,
				Agent: "claude", AgentState: "idle"},
			// Active, current window — but another session.
			"%4": {ID: "%4", WindowID: "@3", SessionID: "$2", Active: true,
				Agent: "codex", AgentState: "working"},
		},
	}

	curSess, curAgent = "$1", ""
	st.rows(nil)
	if curAgent != "%1" {
		t.Fatalf("agent in the session's current window = %q, want %%1", curAgent)
	}

	// Dock: winch takes the focus for its own pane, so NO agent is active
	// anywhere in the session. tmux records where the focus came from, and
	// that record is the only thing standing between this feature and a
	// highlight that never appears on a first dock — which is exactly how
	// the rig caught it.
	p := st.panes["%1"]
	p.Active, p.Last = false, true
	st.panes["%1"] = p
	p = st.panes["%2"]
	p.Active = true
	st.panes["%2"] = p

	curAgent = "" // a FIRST dock: there is no earlier value to be sticky with
	st.rows(nil)
	if curAgent != "%1" {
		t.Errorf("first dock, agent is only pane_last = %q, want %%1", curAgent)
	}

	// And with nothing to fall back to either — the user moved on to some
	// third pane — the last known answer stands rather than blinking out.
	p = st.panes["%1"]
	p.Last = false
	st.panes["%1"] = p
	st.rows(nil)
	if curAgent != "%1" {
		t.Errorf("neither active nor last = %q, want %%1 to persist", curAgent)
	}

	// Sticky is not the same as permanent. Moving to another session drops
	// it, and the new session's own agent claims it on the next build.
	setCurSess("$2")
	if curAgent != "" {
		t.Errorf("crossing into another session left %q behind", curAgent)
	}
	st.rows(nil)
	if curAgent != "%4" {
		t.Errorf("agent in the new session = %q, want %%4", curAgent)
	}

	// Back to $1, where the only ACTIVE agent (%3) sits in a window the
	// session is not on. That is not where you are, so nothing claims the
	// highlight. This is the case that fails if the window check is dropped.
	setCurSess("$1")
	st.rows(nil)
	if curAgent != "" {
		t.Errorf("agent in a non-current window claimed the highlight: %q", curAgent)
	}
}

// Two agents in one window is a real shape — it is the `support` session
// that caused the kill-the-wrong-agent bug — and there the active pane and
// the last pane are BOTH agents. Without an explicit preference, Go's map
// order decides, which means the highlight would wander between repaints of
// an otherwise unchanged world.
func TestActiveAgentBeatsLastAgent(t *testing.T) {
	saveSess, saveAgent := curSess, curAgent
	t.Cleanup(func() { curSess, curAgent = saveSess, saveAgent })

	st := &store{
		sessions: map[string]session{"$1": {ID: "$1", Name: "support"}},
		windows:  map[string]window{"@1": {ID: "@1", SessionID: "$1", Index: 1, Active: true}},
		panes: map[string]pane{
			"%1": {ID: "%1", WindowID: "@1", SessionID: "$1", Last: true,
				Agent: "claude", AgentState: "idle"},
			"%2": {ID: "%2", WindowID: "@1", SessionID: "$1", Active: true,
				Agent: "claude", AgentState: "idle"},
		},
	}
	curSess, curAgent = "$1", ""
	for i := 0; i < 20; i++ {
		st.rows(nil)
		if curAgent != "%2" {
			t.Fatalf("build %d picked %q, want the ACTIVE agent %%2", i, curAgent)
		}
	}
}

// The active card and the cursor are different things wearing different
// backgrounds — herdr keeps selection_bg and active_row_bg as separate
// tokens precisely so the cursor stays legible ON the active row (0.8.2
// added the distinction for that reason). Now that agent cards take the
// active fill too, collapsing them would leave no way to tell the row you
// are on from the row the cursor is on.
//
// Only the RGB themes are held to this. ANSI-16 has exactly one spare
// background (bright black) and herdr concedes the same point — its
// `terminal` palette sets selection_bg to Reset and documents the fallback
// to active_row_bg (app/state.rs:162). Ours collapses them identically, on
// purpose, and the brightness ladder carries the distinction there instead.
func TestSelectionAndActiveFillsAreDistinct(t *testing.T) {
	p := themes["catppuccin"]
	if p.fill == p.actFill {
		t.Errorf("selection and active fills are both %q", p.fill)
	}
	if themes["terminal"].fill != themes["terminal"].actFill {
		t.Log("the terminal theme now separates them; the comment above is stale")
	}
}
