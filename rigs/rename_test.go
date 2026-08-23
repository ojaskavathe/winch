package rigs

import (
	"strings"
	"testing"
)

// TestRenameSession: `r` on a session row edits the name inline in the
// sidebar — enter commits (rename-session), esc cancels.
func TestRenameSession(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL) // dock on beta (work row selected)
	sleep(800)
	sp := r.Side().Pane
	r.SendKeys(sp, "k") // up to the play row
	sleep(700)

	r.SendKeys(sp, "r")
	sleep(300)
	r.SendKeys(sp, "C-u") // clear the prefilled name
	sleep(150)
	r.SendKeys(sp, "ploy")
	sleep(200)
	r.SendKeys(sp, "Enter")
	r.Chk("enter commits the rename", r.WaitUntil(600, func() bool {
		out := r.T("list-sessions", "-F", "#{session_name}")
		return strings.Contains(out, "ploy") && !strings.Contains(out, "play")
	}))
	r.Chk("sidebar shows the new name", r.WaitUntil(400, func() bool {
		return strings.Contains(r.Capture(sp), "ploy")
	}))

	// esc cancels: the buffer edit never reaches tmux
	r.SendKeys(sp, "r")
	sleep(300)
	r.SendKeys(sp, "xxx")
	sleep(150)
	r.SendKeys(sp, "Escape")
	sleep(400)
	out := r.T("list-sessions", "-F", "#{session_name}")
	r.Chk("esc cancels the rename", strings.Contains(out, "ploy") && !strings.Contains(out, "xxx"))

	r.SendKeys(sp, "q")
	sleep(500)
	r.D("toggle", r.CL)
	sleep(1000)
}
