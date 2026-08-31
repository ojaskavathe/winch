package rigs

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestAgent: milestone 3 end to end. A fake `claude` binary (built, since
// pane_current_command is the image name and macOS kills relocated platform
// binaries) is classified from its title (tier 1) and screen (tier 2, TOML
// manifest engine), states flow as pane diffs, completions in unwatched
// windows become "done" until visited, blocked notifies other clients, the
// sidebar renders glyphs plus the agents section, and @winch_agents keeps
// the statusline counts.
func TestAgent(t *testing.T) {
	r := New(t)

	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\nimport \"time\"\nfunc main(){time.Sleep(time.Hour)}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "claude")
	if out, err := exec.Command("go", "build", "-o", fake, src).CombinedOutput(); err != nil {
		t.Fatalf("build fake claude: %v %s", err, out)
	}

	// A cross-session -d split emits NO tmux notification: only the
	// detection tick's own discovery finds this pane (up to 2s) before its
	// 3s startup grace even starts.
	ap := r.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", r.W3, fake+" 100000")
	sleep(1700) // discovery + startup grace + a tick (WINCH_TEST_FAST scale)
	r.T("select-pane", "-T", "⠧ Cooking up a thing", "-t", ap)
	r.Chk("spinner title -> working", r.WaitUntil(300, func() bool {
		return r.LogHas("agent claude pane=.* state=.*->working")
	}))
	r.Chk("statusline counts working", strings.Contains(r.ShowOpt("-gqv", "@winch_agents"), "✻"))

	// ✳ title = idle — but the client is looking at beta, not gamma, so
	// the completion lands as DONE and sticks until gamma is visited.
	r.T("select-pane", "-T", "✳ Ready for input", "-t", ap)
	r.Chk("unwatched completion -> done", r.WaitUntil(300, func() bool {
		return r.LogHas("agent claude pane=.* state=working->done")
	}))
	r.T("select-window", "-t", r.W3)
	r.Chk("visiting clears done", r.WaitUntil(300, func() bool {
		return r.LogHas(`state=done->idle \(seen\)`)
	}))
	r.T("select-window", "-t", r.W2)

	// Sidebar: working glyph on gamma's row plus the agents section.
	r.T("select-pane", "-T", "⠧ Cooking again", "-t", ap)
	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	s := r.Side()
	// State dots differ by hue (herdr's language): working = yellow ●
	// (catppuccin RGB; tmux may re-serialize with : separators).
	workingDot := regexp.MustCompile(`249[;:]226[;:]175m(?:\x1b\[[0-9;:]*m)*●`)
	r.Chk("working glyph in list", r.WaitUntil(200, func() bool {
		raw, _ := r.TQ("capture-pane", "-p", "-e", "-t", s.Pane)
		return workingDot.MatchString(raw)
	}))
	r.Chk("agents section listed", r.WaitUntil(200, func() bool {
		cap := r.Capture(s.Pane)
		return strings.Contains(cap, "agents") && strings.Contains(cap, "Cooking again")
	}))
	r.Chk("sessions heading on top", strings.Contains(r.Capture(s.Pane), "sessions"))

	// Drag the divider up 4 rows: the rule must follow and stick.
	sepRow := func() int {
		for i, l := range strings.Split(r.Capture(s.Pane), "\n") {
			if strings.Contains(l, "─ agents") {
				return i
			}
		}
		return -1
	}
	sep := sepRow()
	r.Chk("divider found", sep > 4)
	if sep > 4 {
		r.Mouse(s.Pane, 0, 2, sep+1, true)  // grab the rule
		r.Mouse(s.Pane, 32, 2, sep-3, true) // drag (motion, button held)
		r.Mouse(s.Pane, 0, 2, sep-3, false) // release
		r.Chk("divider dragged up", r.WaitUntil(200, func() bool {
			moved := sepRow()
			return moved != sep && moved >= sep-5 && moved <= sep-2
		}))
	}

	// A second agent pane showing a permission prompt: screen tier says
	// blocked, blocked outranks working in the aggregate, and clients not
	// looking at gamma get notified.
	bp := r.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", r.W3,
		"sh -c 'printf \"  Do you want to proceed?\\n❯ 1. Yes\\n  2. No, and tell Claude what to do differently (esc)\\n\"; exec "+fake+" 100000'")
	r.Chk("permission screen -> blocked", r.WaitUntil(700, func() bool {
		return r.LogHas("agent claude pane=.* state=.*->blocked")
	}))
	r.Chk("blocked notification sent", r.LogHas("notify blocked"))
	blockedDot := regexp.MustCompile(`243[;:]139[;:]168m(?:\x1b\[[0-9;:]*m)*●`)
	r.Chk("blocked glyph outranks working", r.WaitUntil(200, func() bool {
		raw, _ := r.TQ("capture-pane", "-p", "-e", "-t", s.Pane)
		return blockedDot.MatchString(raw)
	}))
	r.Chk("blocked state on agent row", r.WaitUntil(300, func() bool {
		return strings.Contains(r.Capture(s.Pane), "permission prompt")
	}))

	// The reason this layout changed. ap and bp are in the SAME window of
	// the same session, so every field the card used to lead with —
	// workspace, tab, state, agent kind — is identical for both. Only the
	// name row tells them apart, and it used to be the first thing dropped
	// when the line overflowed. Two agents, two distinguishable cards.
	r.Chk("two agents in one window are distinguishable", r.WaitUntil(500, func() bool {
		cap := r.Capture(s.Pane)
		return strings.Contains(cap, "Cooking again") && strings.Contains(cap, "permission prompt")
	}))
	// And the stale pre-prompt title must not be what identifies the
	// blocked one: it describes what it WAS doing, not why it stopped.
	r.Chk("the blocked card does not lead with a stale title",
		!strings.Contains(r.Capture(s.Pane), "Cooking up a thing"))
	r.Chk("statusline counts blocked", strings.Contains(r.ShowOpt("-gqv", "@winch_agents"), "!"))

	// Border continuity: in browse mode the list paints its own │ column;
	// glyph rows once left the cursor at col 1 and dropped their border
	// cell. Every surface row must carry │ at col 41.
	r.D("browse", r.CL)
	r.Chk("separator unbroken on glyph rows", r.WaitUntil(400, func() bool {
		lines := strings.Split(r.Capture(s.Pane), "\n")
		if len(lines) < 10 {
			return false
		}
		for _, l := range lines {
			rs := []rune(l)
			if len(rs) <= sideW || rs[sideW] != '│' {
				return false
			}
		}
		return true
	}))
	r.SendKeys(s.Pane, "q")
	r.await(5000, "scrub ended", func() bool { return !r.Zoomed(r.Side().Win) })

	// M-a: the agent switcher. First tap pins the top-attention agent
	// (blocked outranks working); a quick second tap cycles onward. The
	// selection fill spans both rows of the two-row entry, so the text on
	// the continuation line must sit on a bg-filled line.
	esc := regexp.MustCompile("\x1b\\[[0-9;:]*m")
	filled := func(sub string) bool {
		raw, _ := r.TQ("capture-pane", "-p", "-e", "-t", r.Side().Pane)
		for _, ln := range strings.Split(raw, "\n") {
			// SGR codes interleave the text in -e captures: match the
			// substring on a stripped copy, the fill bg on the raw line.
			if !strings.Contains(esc.ReplaceAllString(ln, ""), sub) {
				continue
			}
			if strings.Contains(ln, "49;50;68") || strings.Contains(ln, "49:50:68") {
				return true
			}
		}
		return false
	}
	// Undock first: the first tap has to work from a CLOSED sidebar, which
	// is the only way anyone actually reaches for the switcher and the one
	// path this test used to skip. It spawns a TUI, and the hello-list
	// replay that follows used to overwrite the agent pick with the dock's
	// bare window, so the tap landed on a session row instead of an agent.
	// Docked-only coverage saw none of it: no spawn, no hello, no replay.
	//
	// The client is on beta here, which holds no agent, so the anchor finds
	// nothing and the ranking decides: blocked outranks working.
	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })

	r.D("agents", r.CL)
	r.await(5000, "the switcher docked", func() bool { return r.Side().Pane != "" })
	r.Chk("from outside an agent, the switcher pins the blocked one", r.WaitUntil(1500, func() bool {
		return filled("permission prompt")
	}))

	// ...and pinning it must not TELEPORT. The agent is in gamma, the client
	// is on beta, so the selection lands on a row for another window — and
	// the TUI asks for that window's frame, which the daemon turns into a
	// scrub: sidebar zoomed to the full window, billboard of somewhere else,
	// status bar rewritten to name the other session. Nothing has actually
	// moved, which is precisely why it is so disorienting.
	//
	// Opening the sidebar is not navigation. The row is selected; Enter is
	// what goes there.
	sleep(700) // the frame request trails the select
	r.Chk("pinning a remote agent does not zoom", !r.Zoomed(r.Side().Win))
	r.Chk("pinning a remote agent leaves the client where it was", r.ClientWin() == r.W2)

	// Second press CLOSES, like M-s. It used to cycle, which made the pick
	// depend on how recently the key was last pressed — the same keystroke
	// in the same place gave a different agent for ten seconds afterwards.
	r.D("agents", r.CL)
	r.Chk("second press closes the sidebar", r.WaitUntil(2000, func() bool {
		return r.WinchPanes("-a") == 0
	}))

	// Anchoring: opening from INSIDE an agent starts on that agent, even
	// though a blocked one outranks it. Being flung to an unrelated pane
	// reads as random no matter how sound the ranking behind it is.
	//
	// ap is the working agent, bp the blocked one, both in gamma. The
	// distinction only exists when the anchor and the ranking disagree, so
	// the test parks on ap and demands the LOWER-ranked pick.
	r.T("select-window", "-t", r.W3)
	r.T("select-pane", "-t", ap)

	r.D("agents", r.CL)
	r.await(5000, "the switcher docked", func() bool { return r.Side().Pane != "" })
	r.Chk("opening from inside an agent pins THAT agent, not the blocked one",
		r.WaitUntil(1500, func() bool { return filled("Cooking again") }))

	// And it does NOT zoom on the way in: the pinned agent is in the window
	// the sidebar just docked into, so there is nothing to scrub to. Opening
	// through browseOpen forced a zoom-and-capture here, which is the whole
	// reason M-a felt slower than M-s.
	r.Chk("opening on a local agent does not zoom", !r.Zoomed(r.Side().Win))

	r.D("agents", r.CL)
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })

	// Kill the agents: states must leave the world (glyphs gone). Back to
	// beta first — the anchor test parked the client on gamma, and the
	// layout assertion below wants gamma with nothing docked in it.
	r.T("select-window", "-t", r.W2)
	r.TQ("kill-pane", "-t", ap)
	r.TQ("kill-pane", "-t", bp)
	r.Chk("gamma layout intact", r.WaitUntil(300, func() bool {
		return r.Layout(r.W3) == tail(r.LW3)
	}))
	r.Chk("statusline cleared", r.WaitUntil(200, func() bool {
		return r.ShowOpt("-gqv", "@winch_agents") == ""
	}))

	// No agents left: the switcher declines to dock and just says so.
	r.D("agents", r.CL)
	sleep(800)
	r.Chk("no agents: switcher declines to dock", r.Side().Pane == "")
}
