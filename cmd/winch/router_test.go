package main

import (
	"math"
	"sort"
	"testing"
)

// The switcher's tie-break decides the whole cycle order whenever the agents
// share a state, which is the common case — everything idle. Comparing pane
// IDs as STRINGS put %1572 before %4, so the order was stable but unguessable
// and the feature read as picking at random. Real ids from a live session.
func TestAgentTieBreakIsNumeric(t *testing.T) {
	ids := []string{"%77", "%1572", "%1922", "%4", "%1993"}
	sort.Slice(ids, func(i, j int) bool { return paneNum(ids[i]) < paneNum(ids[j]) })

	want := []string{"%4", "%77", "%1572", "%1922", "%1993"}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("oldest-to-newest ordering broken:\n got %v\nwant %v", ids, want)
		}
	}
}

// An unparseable id must not collide with %0 at the front of the cycle: an
// agent the switcher cannot order is one it should visit last, not first.
func TestPaneNumSortsJunkLast(t *testing.T) {
	for _, junk := range []string{"", "%", "%x", "pane-3"} {
		if got := paneNum(junk); got != math.MaxInt {
			t.Errorf("paneNum(%q) = %d, want MaxInt so it sorts last", junk, got)
		}
	}
	if paneNum("%0") != 0 {
		t.Errorf("paneNum(%%0) = %d, want 0", paneNum("%0"))
	}
}

// agentAt backs the anchor: an exact pane match is "the agent you are in",
// and the window fallback is "the agent in the window you are in". The
// window lookup must respect the sort, so a window holding two agents
// anchors on the one that ranks higher rather than whichever came first in
// the world.
func TestAgentAtPrefersPaneThenWindow(t *testing.T) {
	// Sorted as agentsOpen would leave it: blocked outranks working.
	agents := []pane{
		{ID: "%9", WindowID: "@2", AgentState: "blocked"},
		{ID: "%3", WindowID: "@1", AgentState: "working"},
		{ID: "%7", WindowID: "@2", AgentState: "idle"},
	}
	if got := agentAt(agents, "%7", ""); got != 2 {
		t.Errorf("exact pane match = %d, want 2", got)
	}
	if got := agentAt(agents, "", "@2"); got != 0 {
		t.Errorf("window match = %d, want 0 (the higher-ranked agent in @2)", got)
	}
	if got := agentAt(agents, "%404", ""); got != -1 {
		t.Errorf("unknown pane = %d, want -1 so the caller falls back", got)
	}
	if got := agentAt(agents, "", "@404"); got != -1 {
		t.Errorf("agentless window = %d, want -1 so the caller falls back", got)
	}
}
