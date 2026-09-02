package rigs

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHeavyHistory is the live-@4 scenario: a window whose pane carries
// hundreds of thousands of scrollback lines. tmux reflows that history
// synchronously on ANY width change (~250ms observed live), so such windows
// are never pre-carved during scrubbing (billboard = scaled approximation)
// — only an Enter pays the carve, once — and an M-s dismiss onto one lands
// directly, touching no geometry at all.
//
// NO `clear` after the fill: clear emits \e[3J which tmux honors by WIPING
// scrollback — the old zsh rig did that and unknowingly tested a 49-line
// "heavy" window.
func TestHeavyHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: fills 700k lines of scrollback")
	}
	r := New(t)
	r.T("set-option", "-g", "history-limit", "100000000")
	r.T("new-window", "-t", "work:", "-n", "heavy")
	heavy := r.T("display-message", "-p", "-t", "work:heavy", "#{window_id}")
	r.T("send-keys", "-t", "work:heavy",
		`seq 700000 | awk '{print $0, $0*3, "pad pad pad pad pad"}'`, "Enter")
	r.T("split-window", "-h", "-t", "work:heavy")
	hist := func() int {
		h := 0
		for _, ln := range strings.Split(r.T("list-panes", "-t", "work:heavy", "-F", "#{history_size}"), "\n") {
			if n, _ := strconv.Atoi(ln); n > h {
				h = n
			}
		}
		return h
	}
	r.await(30000, "700k lines of scrollback", func() bool { return hist() >= 690_000 })
	r.T("select-window", "-t", r.W2)
	r.Settle()

	// enter heavy via scrub (from beta: gamma, heavy): billboards must not
	// carve it; the Enter pays the one carve
	r.D("toggle", r.CL)
	r.Settle()
	sp := r.Side().Pane
	r.SendKeys(sp, "l")
	r.Settle()
	r.SendKeys(sp, "l")
	r.Settle()
	r.SendKeys(sp, "Enter")
	r.WaitUntil(2000, func() bool { return r.ClientWin() == heavy })
	r.Settle()
	r.Chk("entered heavy", r.ClientWin() == heavy)
	// leave (gamma is carved from the scrub past it) and re-enter: pure swaps
	r.SendKeys(sp, "h")
	r.Settle()
	r.SendKeys(sp, "Enter")
	r.Settle()
	r.SendKeys(sp, "l")
	r.Settle()
	r.SendKeys(sp, "Enter")
	// Await like the first entry does: under full-suite load a bare Settle
	// races the commit and this flaked.
	r.WaitUntil(3000, func() bool { return r.ClientWin() == heavy })
	r.Settle()
	r.Chk("re-entered heavy", r.ClientWin() == heavy)
	// Enter committed and put the keyboard in heavy's own pane, so M-s there
	// focuses before it closes — Undock does both.
	r.Undock() // undock on heavy; releases drain deferred
	r.await(6000, "deferred releases drained", func() bool {
		return r.Spacers() == 0 && r.WinchPanes("-a") == 0
	})

	// M-s dismiss INTO the (again uncarved) heavy window lands directly —
	// the target is already full width; no carve, no reflow
	r.T("switch-client", "-c", r.CL, "-t", "work", ";", "select-window", "-t", r.W2)
	r.Settle()
	r.D("toggle", r.CL)
	r.Settle()
	sp = r.Side().Pane
	r.SendKeys(sp, "l")
	r.Settle()
	r.SendKeys(sp, "l")
	r.Settle()
	r.D("toggle", r.CL)
	r.Chk("dismiss landed on heavy", r.WaitUntil(2000, func() bool { return r.ClientWin() == heavy }))
	r.await(6000, "post-dismiss releases drained", func() bool {
		return r.Spacers() == 0 && r.WinchPanes("-a") == 0
	})

	b, err := os.ReadFile(r.Sock + ".log")
	if err != nil {
		t.Fatalf("daemon log: %v", err)
	}
	log := string(b)
	// billboarding/prefetch must never carve the heavy window
	if regexp.MustCompile(`bench carve win=` + regexp.QuoteMeta(heavy)).MatchString(log) {
		t.Errorf("scrub pre-carved the heavy window")
	}
	// Interactions must never stall at history-reflow scale (~200ms live).
	// The daemon logs anything over 25ms, but undocking FROM the heavy
	// window legitimately hovers around 25-40ms (break-pane hands the
	// sidebar's 40 cols to the heavy neighbor — one unavoidable reflow), so
	// only durations over 100ms count. At most one is allowed: the entry
	// carve. Re-enters are swaps, the dismiss is a direct land, and undocks
	// must not stall on releases.
	slow := []string{}
	for _, m := range regexp.MustCompile(`(?m)^.*(?:commit|toggle|nav) took (\S+) .*$`).FindAllStringSubmatch(log, -1) {
		if d, err := time.ParseDuration(m[1]); err == nil && d > 100*time.Millisecond {
			slow = append(slow, m[0])
		}
	}
	if len(slow) > 1 {
		for _, m := range slow[1:] {
			t.Errorf("reflow-scale stall beyond the entry carve: %s", m)
		}
	}
	r.Chk("deferred release drained", r.Spacers() == 0)
}
