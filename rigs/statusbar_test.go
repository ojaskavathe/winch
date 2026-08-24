package rigs

import (
	"strings"
	"testing"
)

// TestScrubStatus: while scrubbing, the status line must describe the
// TARGET — its session's window list with the scrub target marked current,
// via the daemon's status-format override — not the origin the client is
// still parked in. And it must snap back exactly on q.
func TestScrubStatus(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL) // dock on beta (work session)
	sleep(800)
	sp := r.Side().Pane
	// beta(5) -> w1(4) -> work(3) -> ptwo(2): a cross-session scrub
	r.SendKeys(sp, "k", "k", "k")
	sleep(900)

	pt := r.T("display-message", "-p", "-t", "play:ptwo", "#{window_id}")
	over, _ := r.TQ("show-options", "-t", "work", "status-format")
	r.Chk("override installed on origin", strings.Contains(over, pt))
	rendered := r.T("display-message", "-p", "-t", "work:", "#{T:status-format[0]}")
	r.Chk("status renders the target session's windows", strings.Contains(rendered, "ptwo"))
	r.Chk("origin windows gone from the bar", !strings.Contains(rendered, "beta"))

	r.SendKeys(sp, "q")
	sleep(700)
	over, _ = r.TQ("show-options", "-t", "work", "status-format")
	// Not "empty": the sidebar is still docked, so the row is still wrapped
	// in the pad. What must be gone is the target the scrub pointed it at.
	r.Chk("override removed on q", !strings.Contains(over, pt))
	r.Chk("the pad outlives the scrub", strings.Contains(over, "@winch_win"))
	rendered = r.T("display-message", "-p", "-t", "work:", "#{T:status-format[0]}")
	r.Chk("status back to the origin's windows", strings.Contains(rendered, "beta"))
}

// TestScrubStatusSurvivesCrash: the restore lives in daemon memory, so a
// daemon killed mid-scrub (crash, or a deploy's pkill) leaves the origin
// session's bar pinned to the scrub target — permanently, for every client
// that attaches, and tmux's own session switcher cannot shake it. The next
// daemon must sweep it.
func TestScrubStatusSurvivesCrash(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL) // dock on beta (work session)
	sleep(800)
	sp := r.Side().Pane
	r.SendKeys(sp, "k", "k", "k") // cross-session scrub onto play:ptwo
	sleep(900)

	pt := r.T("display-message", "-p", "-t", "play:ptwo", "#{window_id}")
	over, _ := r.TQ("show-options", "-t", "work", "status-format")
	r.Chk("override installed before the crash", strings.Contains(over, pt))

	r.KillDaemon() // mid-scrub: nothing restores anything

	over, _ = r.TQ("show-options", "-t", "work", "status-format")
	r.Chk("override outlives the daemon", strings.Contains(over, pt))

	r.D("ls") // respawn: the startup sweep runs
	r.await(3000, "override swept", func() bool {
		o, _ := r.TQ("show-options", "-t", "work", "status-format")
		return strings.TrimSpace(o) == ""
	})
	rendered := r.T("display-message", "-p", "-t", "work:", "#{T:status-format[0]}")
	r.Chk("bar describes the session it belongs to", strings.Contains(rendered, "beta"))
	r.Chk("target's windows gone from the bar", !strings.Contains(rendered, "ptwo"))
}
