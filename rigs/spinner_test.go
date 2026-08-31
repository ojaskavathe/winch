package rigs

import (
	"regexp"
	"strings"
	"testing"
)

// TestSpinnerAnimates: the dot beside a working agent turns.
//
// End to end because every link in this chain can break silently and still
// render a perfectly reasonable static dot: the detection tick has to notice
// a title whose only change is its ornament, publish it WITHOUT taking the
// state-change path (which writes the statusline and fires notifications),
// carry it through the world diff, and reach the cell paintList draws at
// column 2. A unit test proves the plumbing; only this proves it moves.
func TestSpinnerAnimates(t *testing.T) {
	r := New(t)

	fake := buildFakeAgent(t)
	ap := r.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", r.W2, fake+" 100000")
	sleep(1700) // discovery + startup grace

	r.T("select-pane", "-T", "⠋ Spinning up", "-t", ap)
	r.await(5000, "agent working", func() bool {
		return r.LogHas("agent claude pane=.* state=.*->working")
	})

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	sleep(600)

	// The frame the agent is publishing must be what the sidebar shows —
	// winch mirrors the agent's spinner rather than running a timer, so a
	// wedged agent shows a STOPPED spinner, which is information.
	frameOnRow := func() string {
		for _, ln := range strings.Split(r.Capture(sp), "\n") {
			if !strings.Contains(ln, "Spinning up") {
				continue
			}
			return ln
		}
		return ""
	}
	braille := regexp.MustCompile(`[\x{2801}-\x{28FF}]`)
	seen := map[string]bool{}
	for _, f := range []string{"⠙", "⠹", "⠸", "⠼"} {
		r.T("select-pane", "-T", f+" Spinning up", "-t", ap)
		ok := r.WaitUntil(2000, func() bool {
			// The card is three rows; the dot sits on the first, so scan the
			// whole strip for the frame rather than one line.
			return strings.Contains(r.Capture(sp), f)
		})
		if ok {
			seen[f] = true
		}
	}
	r.Chk("the dot showed every frame the agent published", len(seen) == 4)
	if len(seen) != 4 {
		t.Logf("  saw %d/4 frames; strip row was %q", len(seen), frameOnRow())
	}

	// And a working agent's dot is a braille frame, not the static ●.
	r.Chk("the working dot is a spinner frame, not a dot",
		braille.MatchString(r.Capture(sp)))

	// Back to idle: the ornament stops being a frame, so the static dot
	// returns rather than freezing on whatever glyph happened to be last.
	r.T("select-pane", "-T", "✳ Done spinning", "-t", ap)
	idleOK := r.WaitUntil(4000, func() bool {
		cap := r.Capture(sp)
		return strings.Contains(cap, "Done spinning") && !braille.MatchString(cap)
	})
	r.Chk("idle restores the static dot", idleOK)
	if !idleOK {
		for _, ln := range strings.Split(r.Capture(sp), "\n") {
			if braille.MatchString(ln) || strings.Contains(ln, "spinning") {
				t.Logf("  strip: %q", ln)
			}
		}
	}

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}
