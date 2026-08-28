package rigs

import (
	"strings"
	"testing"
)

// scrubMark is the shape a scrub override is recognised by — the filtered
// session loop, which nothing a theme writes looks like. Spelled out here
// rather than imported: the rigs are a separate module, and a literal is the
// point anyway (if the daemon's version drifts from this one, the sweep and the
// doctor both go blind and this test should say so).
const scrubMark = "#{S:#{?#{==:#{session_id},$"

// padMark is the same for the pad: it gates on a winch option.
const padMark = "#{@winch_win}"

// TestDoctorReportsALeak: `winch doctor` exists so that "something looks wrong"
// becomes one command instead of twenty show-options calls and a photograph.
// What makes it worth having is that it compares the two sides — what winch
// wrote against what winch recorded writing — because that divergence is
// invisible from either side alone and is exactly what a dropped restore looks
// like.
func TestDoctorReportsALeak(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(700)

	rep := r.D("doctor")
	r.Chk("clean while docked", strings.Contains(rep, "all checks passed"))
	r.Chk("reports the dock", !strings.Contains(rep, "not docked"))
	r.Chk("reports the pad conditionals", strings.Contains(rep, "padFlush="))
	r.Chk("reports the seam", strings.Contains(rep, "seam style"))
	if !strings.Contains(rep, "all checks passed") {
		t.Logf("report while docked:\n%s", rep)
	}

	// A pad on a session the daemon never claimed: what a restore dropped
	// mid-sequence leaves behind. Nothing else in winch will ever notice it,
	// because as far as the registry is concerned that session was handed back.
	r.T("set-option", "-t", "play", "status-format[0]",
		"#{?#{==:#{window_id},"+padMark+"},#[bg=#181825]      ,}#[align=left]x")
	rep = r.D("doctor")
	r.Chk("unmarked pad detected", strings.Contains(rep, "with no claim mark"))
	r.Chk("names the session it is on", strings.Contains(rep, "play"))
	r.Chk("report fails overall", strings.Contains(rep, "check(s) failed"))
	if !strings.Contains(rep, "with no claim mark") {
		t.Logf("report with a planted pad:\n%s", rep)
	}

	// A stranded scrub override is worse than a pad — the bar stops describing
	// the session at all — so the report has to say WHICH session it is
	// rendering, or the symptom reads as "the status line is just wrong".
	// scrubMark already ends in the '$' that opens the session id, so the id
	// is spliced in without its own.
	other := r.T("display-message", "-p", "-t", "work", "#{session_id}")
	r.T("set-option", "-t", "play", "status-format[0]",
		"#[align=left]"+scrubMark+strings.TrimPrefix(other, "$")+"},#{W:x},}}")
	rep = r.D("doctor")
	r.Chk("scrub override detected", strings.Contains(rep, "scrub override"))
	r.Chk("says what the bar is actually rendering",
		strings.Contains(rep, "rendering session "+other))
	if !strings.Contains(rep, "rendering session "+other) {
		t.Logf("report with a planted override:\n%s", rep)
	}

	r.T("set-option", "-uq", "-t", "play", "status-format")
	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// TestDoctorChangesNothing: a tool you reach for when something looks broken
// must not change what you were about to look at. Every read it does is a
// show-options or a display-message, and this is the assertion that keeps it
// that way when someone adds "and while we're here, fix it".
func TestDoctorChangesNothing(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(700)

	snap := func() string {
		var b strings.Builder
		for _, s := range strings.Split(r.T("list-sessions", "-F", "#{session_id}"), "\n") {
			b.WriteString(r.ShowOpt("-t", s))
			b.WriteByte('\n')
		}
		for _, w := range strings.Split(r.T("list-windows", "-a", "-F", "#{window_id}"), "\n") {
			b.WriteString(r.ShowOpt("-w", "-t", w))
			b.WriteByte('\n')
		}
		b.WriteString(r.T("list-panes", "-a", "-F", "#{pane_id} #{pane_width}x#{pane_height}"))
		return b.String()
	}

	before := snap()
	r.D("doctor")
	sleep(300)
	after := snap()
	r.Chk("doctor left every option and pane exactly as it found them", before == after)
	if before != after {
		t.Logf("diverged after running doctor")
	}

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}
