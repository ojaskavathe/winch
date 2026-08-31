package rigs

import (
	"regexp"
	"strings"
	"testing"
)

// TestSpinnerAnimates: the dot beside a working agent turns on winch's own
// clock, not the agent's.
//
// The agent's title is set ONCE and never touched again. Anything that moves
// after that is winch animating; a passthrough of the agent's own spinner
// would sit still here, which is exactly the bug this replaced — the
// detector samples titles every 300ms, so mirroring them gave a subsampled,
// irregular 3fps instead of a smooth 8.
func TestSpinnerAnimates(t *testing.T) {
	r := New(t)

	fake := buildFakeAgent(t)
	ap := r.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", r.W2, fake+" 100000")
	sleep(1700) // discovery + startup grace

	// One title, one frame in it, never updated again.
	r.T("select-pane", "-T", "⠋ Spinning up", "-t", ap)
	r.await(5000, "agent working", func() bool {
		return r.LogHas("agent claude pane=.* state=.*->working")
	})

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	sleep(600)

	// herdr's ten, tracing the braille cell's perimeter.
	braille := regexp.MustCompile(`[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]`)
	frameNow := func() string {
		for _, ln := range strings.Split(r.Capture(sp), "\n") {
			if !strings.Contains(ln, "Spinning up") {
				continue
			}
			// The dot sits on the row ABOVE the name (the card leads with
			// workspace+tab), so scan the strip rather than this one line.
			break
		}
		if m := braille.FindString(r.Capture(sp)); m != "" {
			return m
		}
		return ""
	}

	r.Chk("the working dot is a spinner frame, not a static dot",
		r.WaitUntil(2000, func() bool { return frameNow() != "" }))

	// Sample faster than the 125ms cadence and collect what turns up. Three
	// distinct frames is proof of motion without depending on scheduling.
	seen := map[string]bool{}
	for i := 0; i < 40; i++ {
		if f := frameNow(); f != "" {
			seen[f] = true
		}
		if len(seen) >= 3 {
			break
		}
		sleep(60)
	}
	r.Chk("the dot advances on its own, with the title held still", len(seen) >= 3)
	if len(seen) < 3 {
		t.Logf("  saw %d distinct frames in ~2.4s: %v", len(seen), keysOf(seen))
	}

	// Idle stops it: a spinner that keeps turning for an agent that has
	// finished is a lie, and the static dot is the honest answer.
	r.T("select-pane", "-T", "✳ Done spinning", "-t", ap)
	r.Chk("idle restores the static dot", r.WaitUntil(5000, func() bool {
		cap := r.Capture(sp)
		return strings.Contains(cap, "Done spinning") && !braille.MatchString(cap)
	}))

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
