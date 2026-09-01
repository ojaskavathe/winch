package main

import (
	"strings"
	"testing"
)

// doctor's spacer checks run in two directions and fail for opposite reasons.
// A stray spacer is litter nobody will collect; a carve whose spacer has GONE
// is a promise winch can no longer keep, and it stays invisible until the
// undock replays a saved layout against a window that no longer has the pane
// count it was saved with — `have N panes but need M` in the log.
//
// Only the first direction was checked. This is the second, tested here
// rather than in a rig because reapEmptyCarves repairs the condition within a
// tick, so no rig can catch the check firing in situ; it can only watch the
// recovery. A check that is never seen to fire is indistinguishable from a
// check that does not work — which is exactly how the first version of the
// rig for this passed while asserting nothing.
func TestLostSpacers(t *testing.T) {
	live := map[string]bool{"%1": true, "%2": true}

	if got := lostSpacers(nil, live); got != nil {
		t.Errorf("undocked = %v, want nothing to report", got)
	}

	// Every carve intact, including the sidebar's own window, which
	// legitimately has no spacer because the sidebar itself fills the slot.
	ok := &dockState{carved: map[string]*carveState{
		"@1": {spacer: "%1"},
		"@2": {spacer: "%2"},
		"@3": {spacer: ""},
	}}
	if got := lostSpacers(ok, live); got != nil {
		t.Errorf("healthy dock reported %v", got)
	}

	// One spacer gone. The report has to name BOTH the window (what is stuck
	// held open) and the pane (what to look for), because the whole point is
	// turning "something looks wrong" into one command.
	broken := &dockState{carved: map[string]*carveState{
		"@1": {spacer: "%1"},
		"@2": {spacer: "%99"},
		"@3": {spacer: ""},
	}}
	got := lostSpacers(broken, live)
	if len(got) != 1 {
		t.Fatalf("dead spacer = %v, want exactly one finding", got)
	}
	if !strings.Contains(got[0], "@2") || !strings.Contains(got[0], "%99") {
		t.Errorf("finding %q names neither the window nor the pane", got[0])
	}

	// Several, in a stable order: map iteration would otherwise reshuffle the
	// report between two runs of a command whose whole job is comparison.
	many := &dockState{carved: map[string]*carveState{
		"@9": {spacer: "%97"},
		"@2": {spacer: "%98"},
		"@5": {spacer: "%99"},
	}}
	first := lostSpacers(many, live)
	if len(first) != 3 {
		t.Fatalf("three dead spacers = %v", first)
	}
	for i := 0; i < 20; i++ {
		again := lostSpacers(many, live)
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("run %d reordered the report: %v vs %v", i, again, first)
			}
		}
	}
}
