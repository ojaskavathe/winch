package main

import "testing"

// The ranking contract the sidebar filter relies on. These are the cases that
// would silently rot if someone "simplified" the scorer to a plain subsequence
// check — every one of them passes a naive subsequence test, so only the
// SCORE ordering discriminates them.
func TestFuzzyRanking(t *testing.T) {
	cases := []struct {
		name   string
		q      string
		hi, lo string // hi must outscore lo
	}{
		{
			// The motivating case: p starts a word in "provider", buried
			// mid-word in "develop". Boundary bonus must beat a mid-word hit.
			name: "word-start beats mid-word",
			q:    "dvpr", hi: "dev-provider", lo: "develop-runner",
		},
		{
			// A contiguous run beats the same runes scattered.
			name: "consecutive beats gapped",
			q:    "prov", hi: "provider", lo: "p-r-o-v-x",
		},
		{
			// A prefix match beats one that starts deeper in.
			name: "prefix beats interior",
			q:    "dev", hi: "dev-tools", lo: "under-dev",
		},
		{
			// A tight match in a short name beats a scattered one padded out
			// by a long tail of gaps.
			name: "short tight beats long scattered",
			q:    "abc", hi: "abc", lo: "a-x-b-x-c-xxxxxxxxxx",
		},
	}
	for _, c := range cases {
		hi, okHi := fuzzyScore(c.q, c.hi)
		lo, okLo := fuzzyScore(c.q, c.lo)
		if !okHi || !okLo {
			t.Errorf("%s: expected both to match, got ok(hi)=%v ok(lo)=%v", c.name, okHi, okLo)
			continue
		}
		if hi <= lo {
			t.Errorf("%s: q=%q want score(%q)=%.4f > score(%q)=%.4f", c.name, c.q, c.hi, hi, c.lo, lo)
		}
	}
}

func TestFuzzyNonMatch(t *testing.T) {
	if _, ok := fuzzyScore("xyz", "dev-provider"); ok {
		t.Error(`"xyz" must not match "dev-provider"`)
	}
	if _, ok := fuzzyScore("devx", "dev"); ok {
		t.Error("query longer than a full subsequence must not match")
	}
	if _, ok := fuzzyScore("", "anything"); !ok {
		t.Error("empty query must match everything")
	}
}

// Case folding: a lowercase query matches a capitalised name.
func TestFuzzyCaseFold(t *testing.T) {
	if _, ok := fuzzyScore("dev", "Dev-Provider"); !ok {
		t.Error("lowercase query must match capitalised target")
	}
	// A camelCase hump is a boundary: "sp" should rank "servicePool" (hump p)
	// above "spool-x" only if the hump bonus fires — assert it at least matches.
	if _, ok := fuzzyScore("sp", "servicePool"); !ok {
		t.Error("camelCase subsequence must match")
	}
}
