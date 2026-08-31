package rigs

import (
	"strings"
	"testing"
)

// TestWinchLeavesThePromptAlone: winch does not touch message-style or
// message-command-style. The command prompt is the user's, entirely.
//
// It used to confine the prompt to the right of the sidebar, so prefix-: would
// stop painting over the strip and the seam. That worked and cost too much.
// `align` does double duty — status_prompt_area reads it to place the AREA,
// while message-format embeds the same style so format_draw reads it again to
// place the TEXT — so forcing align=right to push the area past the sidebar
// also shoved the user's prompt to the far edge. Under that: the paint and the
// area come from two DIFFERENT options; editing either means splitting on
// commas that a conditional style does not use as separators; and set-option
// validates styles, so a mangled one aborts the dock batch and the sidebar
// never opens.
//
// The prompt now paints over the sidebar while it is open, which is what tmux
// does to every other pane's status line. A few seconds of overdraw against a
// whole class of bug.
func TestWinchLeavesThePromptAlone(t *testing.T) {
	r := New(t)
	const ms = "fg=#94e2d5,bg=#181825,fill=#181825"
	const cs = "fg=black,bg=#f9e2af,fill=#f9e2af"
	r.T("set-option", "-g", "message-style", ms)
	r.T("set-option", "-g", "message-command-style", cs)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(800)
	sess := r.ClientSess()

	// Nothing at session scope: `show-options -v` does not inherit, so empty
	// here means winch wrote nothing of its own.
	r.Chk("no session-scope message-style", r.ShowOpt("-t", sess, "-v", "message-style") == "")
	r.Chk("no session-scope message-command-style",
		r.ShowOpt("-t", sess, "-v", "message-command-style") == "")
	r.Chk("the global is untouched", r.ShowOpt("-g", "-v", "message-style") == ms)
	r.Chk("and so is the command style", r.ShowOpt("-g", "-v", "message-command-style") == cs)

	// No claim marks either — a mark with no install is still a mark, and the
	// sweep would act on it.
	r.Chk("no claim mark on message-style",
		r.ShowOpt("-t", sess, "-qv", "@winch_saved_message_style") == "")
	r.Chk("no claim mark on message-command-style",
		r.ShowOpt("-t", sess, "-qv", "@winch_saved_message_command_style") == "")

	// A real prompt opens and takes input over the docked sidebar.
	r.Type("\x02:")
	sleep(600)
	r.Type("display-message winched")
	r.Chk("the prompt works while docked", r.WaitUntil(4000, func() bool {
		s := statusScreenUntil(r, func(*screen) bool { return false })
		return strings.Contains(string(s.grid[r.prof.rows-1]), "display-message winched")
	}))
	r.Type("\x1b")
	sleep(400)

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
	r.Chk("still untouched after undock", r.ShowOpt("-g", "-v", "message-style") == ms)
}

// TestStrandedPromptClaimIsSwept: a daemon from BEFORE the confinement was
// dropped may have left message-style claimed on a live session — width=,
// align=right, fill= and a @winch_saved_ mark. Winch no longer wants the
// option, but it still has to give that one back: the names stay on
// ownedOptions precisely so the startup sweep finds them.
//
// Without this the upgrade strands the user's prompt confined forever, with no
// way back short of setting the option by hand — and nothing to say why.
func TestStrandedPromptClaimIsSwept(t *testing.T) {
	r := New(t)
	sess := r.ClientSess()
	const original = "fg=#94e2d5,bg=default,align=centre"
	r.T("set-option", "-g", "message-style", original)

	// Forge exactly what the old daemon left behind: the confined value on the
	// session, and a mark holding the session's saved value. "[]" is the mark
	// for "unset at this scope", which is what a session inheriting the global
	// would have had.
	r.T("set-option", "-t", sess, "message-style",
		"fg=#94e2d5,bg=default,align=right,width=173,fill=#181825")
	r.T("set-option", "-t", sess, "@winch_saved_message_style", "[]")
	r.Chk("the claim is in place", r.ShowOpt("-t", sess, "-v", "message-style") != "")

	// The sweep runs at attach.
	r.KillDaemon()
	r.D("ls")
	r.await(6000, "swept", func() bool {
		return r.ShowOpt("-t", sess, "-v", "message-style") == ""
	})

	r.Chk("the session-scope value is gone",
		r.ShowOpt("-t", sess, "-v", "message-style") == "")
	r.Chk("the mark went with it",
		r.ShowOpt("-t", sess, "-qv", "@winch_saved_message_style") == "")
	r.Chk("so the user's global is what applies again",
		r.ShowOpt("-g", "-v", "message-style") == original)
}
