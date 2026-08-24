package rigs

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestPrefetchGate: every settled keystroke warms both neighbours, so scrubbing
// back and forth re-captured, re-selfContained and re-marshalled a full frame
// per window per pass — for content that had not changed a byte. The streamed
// target already had an activity gate; prefetches did not.
//
// Now an unchanged prefetch ships a FRESH marker instead of the frame. The
// content guarantee is unchanged: the billboard must still show each window's
// own text, and must never mix two windows.
func TestPrefetchGate(t *testing.T) {
	r := NewLive(t)
	fillWindow(r, r.T("display-message", "-p", "-t", "play:", "#{window_id}"), "PLAYONE")
	fillWindow(r, r.T("display-message", "-p", "-t", "work:", "#{window_id}"), "WORKBETA")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	sleep(700)
	scrubAway(r, sp) // enter the scrub (zooms, so prefetch is live)
	sleep(900)

	// Let the first pass warm both neighbours, THEN measure: the gate can
	// only skip what has already been shipped once.
	_, back := scrubAway(r, sp)
	away := map[string]string{"j": "k", "k": "j"}[back]
	for i := 0; i < 2; i++ {
		for _, k := range []string{back, away} {
			r.SendKeys(sp, k)
			sleep(450)
		}
	}

	mark := daemonLogSize(r)
	seen := map[string]bool{}
	mixed := ""
	for i := 0; i < 4; i++ {
		for _, k := range []string{back, away} {
			r.SendKeys(sp, k)
			sleep(450)
			cap := r.Capture(sp)
			a, b := strings.Contains(cap, "PLAYONE"), strings.Contains(cap, "WORKBETA")
			if a && b && mixed == "" {
				mixed = "canvas showed PLAYONE and WORKBETA at once"
			}
			seen["PLAYONE"] = seen["PLAYONE"] || a
			seen["WORKBETA"] = seen["WORKBETA"] || b
		}
	}

	gatedBy, capturedBy := prefetchCounts(r, mark)
	gated, captured := 0, 0
	for w, n := range gatedBy {
		gated += n
		t.Logf("  %s: %d gated, %d captured", w, n, capturedBy[w])
	}
	for w, n := range capturedBy {
		captured += n
		if gatedBy[w] == 0 {
			t.Logf("  %s: 0 gated, %d captured", w, n)
		}
	}
	t.Logf("steady-state scrub: %d prefetches gated, %d still captured", gated, captured)

	// Correctness first — a gate that breaks the billboard is worthless.
	r.Chk("both windows still billboarded", seen["PLAYONE"] && seen["WORKBETA"])
	r.Chk("no stale content survives the gate", mixed == "")
	if mixed != "" {
		t.Logf("  %s", mixed)
	}

	// Then the point of the change. Per WINDOW, not in aggregate: the rig's
	// w1 runs a MARKW1 echo loop, so its activity stamp really does keep
	// moving and re-capturing it is correct. The claim is about a window
	// that is genuinely idle — it must stop being captured entirely.
	// "Mostly", not "entirely": the gate deliberately refuses to fire unless
	// the capture is a full second past the activity stamp, because
	// #{window_activity} has 1-second resolution and a same-second stamp
	// cannot rule out trailing output. Under parallel load a prefetch lands
	// inside that second often enough that demanding zero captures flakes.
	quietMostlyGated := false
	for w, n := range gatedBy {
		if n > capturedBy[w] {
			quietMostlyGated = true
			t.Logf("  idle window %s: %d gated vs %d captured", w, n, capturedBy[w])
		}
	}
	r.Chk("unchanged prefetches are gated, not re-captured", gated > 0)
	r.Chk("an idle window stops paying for most of its prefetches", quietMostlyGated)
	if gated == 0 {
		t.Logf("  no `gate prefetch` lines after offset %d — the gate never fired", mark)
	}

	r.SendKeys(sp, "q")
	sleep(600)
	r.D("toggle", r.CL)
	sleep(800)
}

// daemonLogSize is the current byte offset of the daemon log, for counting
// only what happens after a marked point.
func daemonLogSize(r *Rig) int64 {
	fi, err := os.Stat(r.Sock + ".log")
	if err != nil {
		return 0
	}
	return fi.Size()
}

var gatePrefetchRe = regexp.MustCompile(`gate prefetch win=(\S+)`)

var prefetchFrameRe = regexp.MustCompile(`frame win=(\S+) prefetch=true`)

// prefetchCounts returns how many prefetches the activity gate answered with a
// restamp versus how many paid a full capture, after byte offset off.
func prefetchCounts(r *Rig, off int64) (gated, captured map[string]int) {
	gated, captured = map[string]int{}, map[string]int{}
	b, err := os.ReadFile(r.Sock + ".log")
	if err != nil || int64(len(b)) <= off {
		return gated, captured
	}
	for _, ln := range strings.Split(string(b[off:]), "\n") {
		if m := gatePrefetchRe.FindStringSubmatch(ln); m != nil {
			gated[m[1]]++
		} else if m := prefetchFrameRe.FindStringSubmatch(ln); m != nil {
			captured[m[1]]++
		}
	}
	return gated, captured
}
