package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The oracle test: emulatePane's whole claim is that it reproduces tmux's
// reflow cell-for-cell, so it is tested against tmux itself — capture a pane
// joined, emulate the resize, then REALLY resize the same pane and diff the
// visible text and cursor. Runs against a scratch server on its own socket;
// skipped when no tmux is on PATH (the pure-logic tests in emulate_test.go
// still run everywhere).
//
// Rows are compared as rendered cells (see cells below): the wire bytes
// legitimately differ — a real capture restates SGR where the emulation
// carries it — but the cells, runes and attributes both, must not.

type oracleSrv struct {
	t    *testing.T
	sock string
}

func startOracle(t *testing.T, w, h int) *oracleSrv {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux on PATH")
	}
	s := &oracleSrv{t: t, sock: fmt.Sprintf("winch-oracle-%d", os.Getpid())}
	s.run("kill-server")
	if out, err := s.run("-f", "/dev/null",
		"new-session", "-d", "-s", "o", "-x", strconv.Itoa(w), "-y", strconv.Itoa(h)); err != nil {
		t.Fatalf("new-session: %v (%s)", err, out)
	}
	t.Cleanup(func() { s.run("kill-server") })
	s.must("set-option", "-g", "history-limit", "50000")
	return s
}

func (s *oracleSrv) run(args ...string) (string, error) {
	// -u plus a UTF-8 locale: `go test` under nix runs with a C locale, and
	// a tmux started that way stores each emoji as raw bytes — three cells
	// of ⚡ — which is not the server the daemon ever talks to.
	cmd := exec.Command("tmux", append([]string{"-u", "-L", s.sock}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=en_US.UTF-8")
	out, err := cmd.CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}

func (s *oracleSrv) must(args ...string) string {
	s.t.Helper()
	out, err := s.run(args...)
	if err != nil {
		s.t.Fatalf("tmux %v: %v (%s)", args, err, out)
	}
	return out
}

func (s *oracleSrv) lines(args ...string) []string {
	return strings.Split(s.must(args...), "\n")
}

func (s *oracleSrv) paneState() (w, h, hist, cx, cy int, cursor bool) {
	s.t.Helper()
	p := strings.Split(s.must("display-message", "-p", "-t", "o:0",
		"#{pane_width} #{pane_height} #{history_size} #{cursor_x} #{cursor_y} #{cursor_flag}"), " ")
	if len(p) != 6 {
		s.t.Fatalf("pane state %q", p)
	}
	w, _ = strconv.Atoi(p[0])
	h, _ = strconv.Atoi(p[1])
	hist, _ = strconv.Atoi(p[2])
	cx, _ = strconv.Atoi(p[3])
	cy, _ = strconv.Atoi(p[4])
	return w, h, hist, cx, cy, p[5] == "1"
}

// settle waits for the pane's history to stop moving.
func (s *oracleSrv) settle() {
	s.t.Helper()
	last := -1
	for i := 0; i < 100; i++ {
		time.Sleep(100 * time.Millisecond)
		_, _, hist, _, _, _ := s.paneState()
		if hist == last {
			return
		}
		last = hist
	}
	s.t.Fatal("pane never settled")
}

// cells renders rows to canonical per-cell form — each printable rune tagged
// with the SGR state active at it — so escape PLACEMENT differences (a real
// capture restates state at a row edge where the emulation carries it into
// the next row) compare equal, and actual attribute differences do not.
// State folds across rows, like a terminal. Trailing default-state blanks
// and trailing empty rows are trimmed on both sides.
func cells(rows []string) []string {
	var st sgrState
	out := make([]string, len(rows))
	for i, r := range rows {
		var b strings.Builder
		var esc strings.Builder
		mode := 0
		for _, ch := range r {
			switch mode {
			case 1: // after ESC
				esc.WriteRune(ch)
				switch ch {
				case '[':
					mode = 2
				case ']':
					mode = 3
				default:
					mode = 0
					esc.Reset()
				}
				continue
			case 2: // CSI
				esc.WriteRune(ch)
				if ch >= 0x40 && ch <= 0x7e {
					st.fold(esc.String())
					esc.Reset()
					mode = 0
				}
				continue
			case 3: // OSC: swallow
				if ch == 0x07 || ch == '\\' {
					mode = 0
					esc.Reset()
				}
				continue
			}
			if ch == 0x1b {
				esc.WriteRune(ch)
				mode = 1
				continue
			}
			b.WriteString(st.seq())
			b.WriteString("\x00")
			b.WriteRune(ch)
			b.WriteString("\x01")
		}
		row := b.String()
		for strings.HasSuffix(row, "\x00 \x01") {
			row = row[:len(row)-3]
		}
		out[i] = row
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func oracleCompare(t *testing.T, s *oracleSrv, toW int) {
	t.Helper()
	w1, h1, hist, cx, cy, cur := s.paneState()
	pad := min(emulatePad, hist)
	rng := []string{"-S", strconv.Itoa(-pad), "-E", strconv.Itoa(h1 - 1), "-t", "o:0"}
	plain := s.lines(append([]string{"capture-pane", "-e", "-p", "-N"}, rng...)...)
	joined := s.lines(append([]string{"capture-pane", "-e", "-p", "-N", "-J"}, rng...)...)
	spans := logicalSpans(plain, joined)
	if spans == nil {
		t.Errorf("resize %d->%d: logicalSpans misaligned", w1, toW)
		return
	}
	cline, coff, curOK := cursorLocate(plain, spans, pad, cy, cx)
	ep := emulatePane(joined, pad, toW, h1, cline, coff, cur && curOK)

	s.must("resize-window", "-x", strconv.Itoa(toW), "-t", "o:0")
	time.Sleep(200 * time.Millisecond)
	real := s.lines("capture-pane", "-e", "-p", "-N", "-E", strconv.Itoa(h1-1), "-t", "o:0")
	_, _, _, rcx, rcy, rcur := s.paneState()

	got, want := cells(ep.rows), cells(real)
	if len(got) != len(want) {
		t.Fatalf("resize %d->%d: %d emulated rows vs %d real\n emu: %q\nreal: %q",
			w1, toW, len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("resize %d->%d row %d:\n emu: %q\nreal: %q", w1, toW, i, got[i], want[i])
		}
	}
	if rcur && (!ep.cursor || ep.cx != rcx || ep.cy != rcy) {
		t.Errorf("resize %d->%d cursor: emu %v (%d,%d), real (%d,%d)",
			w1, toW, ep.cursor, ep.cx, ep.cy, rcx, rcy)
	}
}

func TestOracleNarrowGrow(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a tmux server")
	}
	s := startOracle(t, 96, 20)
	// Adversarial fill: wrapping color runs, short lines, blanks, wide
	// runes, and enough of it that narrowing pulls real history back in.
	s.must("send-keys", "-t", "o:0", `for i in $(seq 1 120); do `+
		`printf '\033[3%dm%d: wide 世界 emoji ⚡ and a long tail that wraps at ninety-six columns for sure %d\033[0m\n' $((i%7)) $i $i; `+
		`printf 'short %d\n' $i; printf '\n'; done`, "Enter")
	s.settle()
	oracleCompare(t, s, 86) // the iv-pane shrink
	oracleCompare(t, s, 96) // grow back
	oracleCompare(t, s, 51) // aggressive shrink, many rewraps
	oracleCompare(t, s, 96) // grow across multiple wrap levels
}

func TestOracleShellPrompt(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a tmux server")
	}
	s := startOracle(t, 80, 15)
	s.must("send-keys", "-t", "o:0", "seq 1 30", "Enter")
	s.settle()
	oracleCompare(t, s, 70)
	oracleCompare(t, s, 80)
}
