package main

import (
	"math"
	"unicode"
)

// Fuzzy subsequence scoring, a port of fzy's algorithm (Smith-Waterman with
// affine gaps and match bonuses). The query matches when every rune appears in
// the target in order; the score rewards matches that land on word boundaries
// (after a separator, a camelCase hump, a dot) and runs of consecutive matches,
// so "dvpr" ranks "dev-provider" above "develop-runner": the p starts a word in
// the first and sits mid-word in the second.
//
// Scores are only comparable for the SAME query — they are relative rankings,
// not absolute qualities. A longer target drags its own score down through gap
// penalties, which is the point: a tight match in a short name beats a scattered
// one in a long paragraph.
const (
	scoreGapLeading   = -0.005
	scoreGapTrailing  = -0.005
	scoreGapInner     = -0.01
	scoreMatchConsec  = 1.0
	scoreMatchSlash   = 0.9
	scoreMatchWord    = 0.8
	scoreMatchCapital = 0.7
	scoreMatchDot     = 0.6
)

var scoreMin = math.Inf(-1)

// isSubseq reports whether every rune of q appears in t in order (case-fold).
// It is the cheap gate: no-match is the common case when narrowing, and the DP
// below is quadratic, so reject first.
func isSubseq(q, t []rune) bool {
	if len(q) == 0 {
		return true
	}
	i := 0
	for _, tc := range t {
		if fold(q[i]) == fold(tc) {
			i++
			if i == len(q) {
				return true
			}
		}
	}
	return false
}

func fold(r rune) rune { return unicode.ToLower(r) }

// bonusAt is the score a match at position j earns from the character before
// it: the start of a word (after a separator) or a camelCase hump is where the
// eye looks first, so a query rune landing there is worth more than one buried
// mid-word.
func bonusAt(t []rune, j int) float64 {
	var prev rune = '/' // start of string reads as a slash boundary (fzy)
	if j > 0 {
		prev = t[j-1]
	}
	cur := t[j]
	switch prev {
	case '/':
		return scoreMatchSlash
	case '-', '_', ' ', '·', ':':
		return scoreMatchWord
	case '.':
		return scoreMatchDot
	}
	if unicode.IsUpper(cur) && unicode.IsLower(prev) {
		return scoreMatchCapital
	}
	return 0
}

// fuzzyScore returns the match score and whether q is a subsequence of t. An
// empty query matches everything with a flat score, so the corpus shows in its
// default order until the user types.
func fuzzyScore(query, target string) (float64, bool) {
	q := []rune(query)
	t := []rune(target)
	if len(q) == 0 {
		return 0, true
	}
	if len(q) > len(t) || !isSubseq(q, t) {
		return scoreMin, false
	}
	n, m := len(q), len(t)

	bonus := make([]float64, m)
	for j := range t {
		bonus[j] = bonusAt(t, j)
	}

	// D[j]: best score for query[:i+1] ending in a match AT target j.
	// M[j]: best score for query[:i+1] within target[:j+1] (match may end earlier).
	// Kept one row at a time (prevD/prevM), fzy's rolling arrays.
	prevD := make([]float64, m)
	prevM := make([]float64, m)
	curD := make([]float64, m)
	curM := make([]float64, m)

	for i := 0; i < n; i++ {
		prevScore := scoreMin
		gap := scoreGapInner
		if i == n-1 {
			gap = scoreGapTrailing
		}
		for j := 0; j < m; j++ {
			if fold(q[i]) == fold(t[j]) {
				score := scoreMin
				if i == 0 {
					score = float64(j)*scoreGapLeading + bonus[j]
				} else if j > 0 {
					score = math.Max(prevM[j-1]+bonus[j], prevD[j-1]+scoreMatchConsec)
				}
				curD[j] = score
				prevScore = math.Max(score, prevScore+gap)
				curM[j] = prevScore
			} else {
				curD[j] = scoreMin
				prevScore = prevScore + gap
				curM[j] = prevScore
			}
		}
		prevD, curD = curD, prevD
		prevM, curM = curM, prevM
	}
	return prevM[m-1], true
}
