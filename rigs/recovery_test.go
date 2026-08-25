package rigs

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecovery: hosting window killed, user splits while docked, daemon
// restart sweeping leaked spacers (restart LAST: its reattach gap would
// flake anything racing it).
func TestRecovery(t *testing.T) {
	r := New(t)

	// I: kill hosting window recovers
	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	r.T("kill-window", "-t", r.ClientWin())
	r.Chk("dock state cleaned", r.WaitUntil(300, func() bool {
		return r.ShowOpt("-t", "play", "-v", "@winch_docked") == "" &&
			r.ShowOpt("-t", "work", "-v", "@winch_docked") == ""
	}))
	r.D("toggle", r.CL)
	r.Chk("sidebar re-docked", r.WaitUntil(300, func() bool {
		s := r.Side()
		return s.Left == 0 && s.Width == sideW
	}))
	r.D("toggle", r.CL)
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })

	// J: user splits while docked; undock restore fails cleanly
	r.D("toggle", r.CL)
	r.await(5000, "docked again", func() bool { return r.Side().Pane != "" })
	cur := r.ClientWin()
	np0 := len(strings.Split(r.T("list-panes", "-t", cur, "-F", "x"), "\n")) - r.WinchPanes("-t", cur)
	// Kill by the split's own id later — pane LIST order is an artifact
	// (split-window -b inserts first where join-pane -b appended), so
	// "last listed" is not "the pane this test created".
	userSplit := r.T("split-window", "-v", "-P", "-F", "#{pane_id}", "-t", cur)
	sleep(300)
	r.D("toggle", r.CL)
	r.await(5000, "undock done", func() bool { return r.WinchPanes("-t", cur) == 0 })
	r.Chk("user split survives undock", len(strings.Split(r.T("list-panes", "-t", cur, "-F", "x"), "\n")) == np0+1)
	r.Chk("no sidebar left behind", r.WinchPanes("-t", cur) == 0)
	r.Chk("stale restore logged", r.LogHas("restore layout|undock:"))
	r.TQ("kill-pane", "-t", userSplit)

	// S: daemon restart sweeps leaked spacers
	r.T("split-window", "-d", "-hb", "-f", "-l", "40", "-t", r.W3, "sleep 100000001") // fake a leak
	exec.Command("pkill", "-f", filepath.Base(winchBin)+" -S "+tmuxDir+"/"+r.L+" run").Run()
	r.await(5000, "old daemon dead", func() bool {
		return exec.Command("pgrep", "-f", filepath.Base(winchBin)+" -S "+tmuxDir+"/"+r.L+" run").Run() != nil
	})
	r.D("ls")
	r.Chk("leaked spacer swept", r.WaitUntil(400, func() bool { return r.Spacers() == 0 }))
	r.Chk("gamma layout intact", r.Layout(r.W3) == tail(r.LW3))
	err := exec.Command("pgrep", "-f", filepath.Base(winchBin)+" -S "+tmuxDir+"/"+r.L+" run").Run()
	r.Chk("daemon alive", err == nil)

	// T: daemon killed MID-DOCK leaks session state (@winch_docked routes
	// M-h/M-l into `winch nav` failures, the status pad shifts the bar);
	// the next daemon's startup sweep must clear it. Layout of the docked
	// window is knowingly lost here (the restore lived in the dead daemon).
	r.D("toggle", r.CL)
	r.Chk("docked for the kill", r.WaitUntil(500, func() bool {
		return r.ShowOpt("-t", "work", "-v", "@winch_docked") == "1"
	}))
	exec.Command("pkill", "-f", filepath.Base(winchBin)+" -S "+tmuxDir+"/"+r.L+" run").Run()
	r.await(5000, "old daemon dead", func() bool {
		return exec.Command("pgrep", "-f", filepath.Base(winchBin)+" -S "+tmuxDir+"/"+r.L+" run").Run() != nil
	})
	r.D("ls")
	r.Chk("stale @winch_docked swept", r.WaitUntil(400, func() bool {
		return r.ShowOpt("-t", "work", "-v", "@winch_docked") == ""
	}))
	r.Chk("status pad swept", r.ShowOpt("-t", "work", "status-left") == "")
	// Swept off the @winch_saved_* marks the dead daemon wrote onto the session
	// itself, so the restore is exact rather than a guess at what winch's own
	// writes look like. See rigs/crashrestore_test.go for the window options,
	// which no content-keyed sweep could ever have recognised.
	r.Chk("sweep logged", r.LogHas(`swept leaked session .*@winch_docked`))
	r.Chk("marks cleared", r.ShowOpt("-t", "work", "-v", "@winch_saved_status_format") == "")
}
