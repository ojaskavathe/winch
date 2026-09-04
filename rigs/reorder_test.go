package rigs

import (
	"strings"
	"testing"
)

// TestReorderSessions: J on a selected session row moves it DOWN in the
// sidebar list, the move persists to @winch-session-order, and a fresh dock is
// born in the saved order. K moves back up.
func TestReorderSessions(t *testing.T) {
	r := New(t)
	r.D("toggle", r.CL)
	sleep(900)
	sp := r.Side().Pane

	// Two sessions in the rig: work and play. Read their relative order off
	// the painted sidebar (top, other) by position.
	posBoth := func(pane string) (top, other string, ok bool) {
		c := r.Capture(pane)
		iw, ip := strings.Index(c, "work"), strings.Index(c, "play")
		if iw < 0 || ip < 0 {
			return "", "", false
		}
		if iw < ip {
			return "work", "play", true
		}
		return "play", "work", true
	}

	// Selection to the topmost session row.
	r.SendKeys(sp, "k")
	sleep(200)
	r.SendKeys(sp, "k")
	sleep(300)
	top, other, ok := posBoth(sp)
	r.Chk("both sessions listed", ok)

	// J moves the selected (top) session down; the two swap.
	r.SendKeys(sp, "J")
	r.Chk("J moves the session below the other", r.WaitUntil(5000, func() bool {
		c := r.Capture(sp)
		return strings.Index(c, top) > strings.Index(c, other)
	}))

	// Persisted, other session now first.
	r.Chk("order persisted to @winch-session-order", r.WaitUntil(3000, func() bool {
		o := r.ShowOpt("-gv", "@winch-session-order")
		io, it := strings.Index(o, other), strings.Index(o, top)
		return io >= 0 && it >= 0 && io < it
	}))

	// A fresh dock is born in the saved order (snapshot carries it).
	r.Undock()
	sleep(700)
	r.D("toggle", r.CL)
	sleep(900)
	sp = r.Side().Pane
	r.Chk("saved order survives a fresh dock", r.WaitUntil(5000, func() bool {
		c := r.Capture(sp)
		return strings.Index(c, other) < strings.Index(c, top)
	}))

	// K on the bottom session moves it back up.
	r.SendKeys(sp, "j")
	sleep(200)
	r.SendKeys(sp, "j")
	sleep(300)
	bt, bo, ok := posBoth(sp) // bt=top, bo=bottom (selection is now on bottom)
	r.Chk("still two rows", ok)
	r.SendKeys(sp, "K")
	r.Chk("K moves the bottom session up", r.WaitUntil(5000, func() bool {
		c := r.Capture(sp)
		return strings.Index(c, bo) < strings.Index(c, bt)
	}))

	r.Undock()
	sleep(500)
}
