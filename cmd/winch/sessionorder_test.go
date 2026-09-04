package main

import (
	"strings"
	"testing"
)

func sessNames(ss []session) string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return strings.Join(out, ",")
}

// TestSortSessionsCreationOrderNotAlphabetical: with no custom order, the base
// order is creation time — names are chosen so alphabetical would give a
// different result, which is the whole point (the user did not want alphabetic).
func TestSortSessionsCreationOrderNotAlphabetical(t *testing.T) {
	defer func() { uiSessionOrder = nil }()
	uiSessionOrder = nil
	ss := []session{
		{ID: "$1", Name: "zebra", Created: 10},
		{ID: "$2", Name: "alpha", Created: 20},
		{ID: "$3", Name: "mango", Created: 30},
	}
	sortSessions(ss)
	if got := sessNames(ss); got != "zebra,alpha,mango" {
		t.Fatalf("creation order: got %q, want zebra,alpha,mango (alphabetical would be alpha,mango,zebra)", got)
	}
}

// TestSortSessionsExplicitFirstThenCreation: names in uiSessionOrder lead, in
// that order; everything else follows in creation order.
func TestSortSessionsExplicitFirstThenCreation(t *testing.T) {
	defer func() { uiSessionOrder = nil }()
	uiSessionOrder = []string{"mango", "zebra"}
	ss := []session{
		{ID: "$1", Name: "zebra", Created: 10},
		{ID: "$2", Name: "alpha", Created: 20}, // unordered
		{ID: "$3", Name: "mango", Created: 30},
		{ID: "$4", Name: "kiwi", Created: 5}, // unordered, oldest
	}
	sortSessions(ss)
	// mango, zebra (explicit order), then kiwi, alpha (creation order).
	if got := sessNames(ss); got != "mango,zebra,kiwi,alpha" {
		t.Fatalf("got %q, want mango,zebra,kiwi,alpha", got)
	}
}

// TestSortSessionsIgnoresUnknownOrderedName: a name in the order that no longer
// exists is simply skipped; the real sessions still sort correctly.
func TestSortSessionsIgnoresUnknownOrderedName(t *testing.T) {
	defer func() { uiSessionOrder = nil }()
	uiSessionOrder = []string{"ghost", "beta"}
	ss := []session{
		{ID: "$1", Name: "alpha", Created: 10},
		{ID: "$2", Name: "beta", Created: 20},
	}
	sortSessions(ss)
	if got := sessNames(ss); got != "beta,alpha" {
		t.Fatalf("got %q, want beta,alpha (beta pinned first, alpha by creation)", got)
	}
}
