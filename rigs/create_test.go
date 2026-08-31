package rigs

import (
	"strings"
	"testing"
)

// TestCreateSession: `n` on a session row names a new session and lands in
// it, inheriting the working directory of the row it was pressed on.
//
// herdr's create flow, which is one idea worth copying: the field opens
// PREFILLED with a suggestion and selected, so enter alone accepts it and the
// first character typed replaces it. Neither choice is the other's penalty.
func TestCreateSession(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	sleep(600)

	// The selected row is a session; `n` opens the field under the heading.
	r.SendKeys(sp, "n")
	r.Chk("the field opens prefilled", r.WaitUntil(1500, func() bool {
		// The suggestion is the session's cwd basename, and the field draws
		// a block cursor after it. Whatever the rig's cwd is, the field is
		// non-empty and carries the cursor.
		return strings.Contains(r.Capture(sp), "█")
	}))

	// Type a name: the first keystroke replaces the suggestion rather than
	// appending to it, so what lands is exactly what was typed.
	for _, k := range []string{"z", "e", "b", "r", "a"} {
		r.SendKeys(sp, k)
	}
	r.Chk("typing replaced the suggestion", r.WaitUntil(1500, func() bool {
		cap := r.Capture(sp)
		return strings.Contains(cap, "zebra█")
	}))

	r.SendKeys(sp, "Enter")
	r.await(5000, "session created", func() bool {
		out, _ := r.TQ("list-sessions", "-F", "#{session_name}")
		return containsLine(out, "zebra")
	})
	r.Chk("and the client landed in it", r.WaitUntil(3000, func() bool {
		return r.ClientSess() == "zebra"
	}))

	// It starts where the row it was created from was, not wherever the
	// daemon happened to be.
	want := strings.TrimSpace(r.T("display-message", "-p", "-t", r.W2, "#{pane_current_path}"))
	got := strings.TrimSpace(r.T("display-message", "-p", "-t", "zebra", "#{pane_current_path}"))
	r.Chk("it inherits the source session's directory", want != "" && got == want)
	if got != want {
		t.Logf("  new session cwd %q, source %q", got, want)
	}

	// Esc abandons a create without making anything.
	r.SendKeys(r.Side().Pane, "n")
	sleep(400)
	r.SendKeys(r.Side().Pane, "q") // a plain character while the field owns keys
	sleep(200)
	r.SendKeys(r.Side().Pane, "Escape")
	r.Chk("esc leaves no session behind", r.WaitUntil(1500, func() bool {
		out, _ := r.TQ("list-sessions", "-F", "#{session_name}")
		return !containsLine(out, "q")
	}))
	// ...and the field is gone, so keys go back to the list.
	r.Chk("the field closed", r.WaitUntil(1000, func() bool {
		return !strings.Contains(r.Capture(r.Side().Pane), "█")
	}))

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// containsLine matches a whole line, so "q" is not found inside "zebra".
func containsLine(out, want string) bool {
	for _, ln := range strings.Split(out, "\n") {
		if strings.TrimSpace(ln) == want {
			return true
		}
	}
	return false
}
