package rigs

import (
	"strings"
	"testing"
)

// spacerStart is the spacer pane's start command. A literal on purpose, like
// scrubMark and padMark: the rigs are a separate module, and if the daemon's
// signature ever drifts from this one the startup sweep and the doctor both
// go blind, which is precisely what this test should notice.
const spacerStart = "sleep 100000001"

// TestDoctorReportsACarveWithNoSpacer: the converse direction.
//
// doctor already checks that every spacer it finds is held by a carve —
// litter nobody will collect. The opposite failure is worse and was
// unchecked: a carve whose spacer has GONE. That is not litter, it is a
// promise winch can no longer keep, and it stays invisible until the undock,
// where the release replays a saved layout against a window that no longer
// has the pane count it was saved with. `have N panes but need M` in the
// daemon log is that moment.
//
// Found the hard way, on a live server: a spacer got killed out from under a
// live carve and `winch doctor` went on reporting all checks passed
// throughout. A report that says "healthy" while holding dead panes is worse
// than no report, because it is what you check BEFORE deciding nothing is
// wrong.
func TestDoctorReportsACarveWithNoSpacer(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })

	// Move the sidebar to another window: the one it leaves keeps its slot
	// open with a spacer, which is the carve this test then breaks.
	r.D("nav", "next", r.CL)
	var spacer, win string
	r.await(6000, "a spacer was carved", func() bool {
		for _, ln := range strings.Split(
			r.T("list-panes", "-a", "-F", "#{pane_id} #{window_id} [#{pane_start_command}]"), "\n") {
			f := strings.Fields(ln)
			if len(f) >= 3 && strings.Contains(ln, spacerStart) {
				spacer, win = f[0], f[1]
				return true
			}
		}
		return false
	})
	r.Chk("found a carved spacer", spacer != "")
	if spacer == "" {
		t.Logf("  panes:\n%s", r.T("list-panes", "-a", "-F", "  #{pane_id} #{window_id} [#{pane_start_command}]"))
		return
	}

	rep := r.D("doctor")
	r.Chk("clean while the carve is intact", strings.Contains(rep, "all checks passed"))
	r.Chk("the new check is reported at all", strings.Contains(rep, "every carve still has its spacer"))
	r.Chk("the window is held before the kill", heldLine(rep) != "" && strings.Contains(heldLine(rep), win))
	if !strings.Contains(rep, "all checks passed") {
		t.Logf("report before the kill:\n%s", rep)
	}

	// Kill the spacer out from under the live carve.
	r.T("kill-pane", "-t", spacer)

	// What must happen is a RECOVERY, not a complaint: reapEmptyCarves drops
	// a carve whose spacer went and hands the window's options back
	// (dock.go:1356). So the assertion is that the window leaves `held` —
	// which is also what proves doctor's new check stayed quiet because
	// there was nothing to report, rather than because it does not work.
	var after string
	healed := r.WaitUntil(6000, func() bool {
		after = r.D("doctor")
		return !strings.Contains(heldLine(after), win)
	})
	r.Chk("the carve was reaped, not stranded", healed)
	r.Chk("and the report is still clean", strings.Contains(after, "all checks passed"))
	if !healed {
		t.Logf("still holding %s after its spacer %s died:\n%s", win, spacer, after)
	}
}

// heldLine pulls doctor's "held" line out of the report — the daemon's own
// list of windows it believes it is holding open.
func heldLine(rep string) string {
	for _, ln := range strings.Split(rep, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "held ") {
			return ln
		}
	}
	return ""
}
