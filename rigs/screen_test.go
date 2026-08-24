package rigs

import (
	"fmt"
	"regexp"
	"strings"
)

// A terminal model for the recorded client stream. Rigs that ask "what did
// the user actually SEE, and for how long" replay the timestamped chunks
// through this and inspect the grid between them — a flicker is a region
// holding the wrong thing across a frame boundary, which no amount of
// capture-pane after the fact can show.

type screen struct {
	grid     [][]rune
	row, col int
	rows     int
	cols     int
	pend     []byte // partial escape carried across chunk boundaries
}

func newScreen(rows, cols int) *screen {
	g := make([][]rune, rows)
	for y := range g {
		g[y] = make([]rune, cols)
		for x := range g[y] {
			g[y][x] = ' '
		}
	}
	return &screen{grid: g, row: 1, col: 1, rows: rows, cols: cols}
}

// colText is the text in the first w columns below the status row — the
// sidebar strip, in other words.
func (s *screen) colText(w int) string {
	var b strings.Builder
	for y := 1; y < s.rows; y++ {
		for x := 0; x < w && x < s.cols; x++ {
			b.WriteRune(s.grid[y][x])
		}
	}
	return b.String()
}

// canvasText is the text to the RIGHT of the sidebar strip — the preview
// region while scrubbing, the real panes once committed.
func (s *screen) canvasText(w int) string {
	var b strings.Builder
	for y := 1; y < s.rows && y < 12; y++ {
		for x := w + 1; x < s.cols; x++ {
			b.WriteRune(s.grid[y][x])
		}
	}
	return b.String()
}

var csiRe = regexp.MustCompile(`^\x1b\[([0-9;:?]*)([A-Za-z@])`)

// incompleteEsc reports whether b starts an escape whose terminator has not
// arrived yet. Without this a sequence split across two reads gets painted
// into the grid as literal text.
func incompleteEsc(b []byte) bool {
	if len(b) < 2 {
		return true
	}
	switch b[1] {
	case '[':
		for i := 2; i < len(b); i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				return false
			}
		}
		return true
	case ']':
		for i := 2; i < len(b); i++ {
			if b[i] == 0x07 || (b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\') {
				return false
			}
		}
		return true
	case '(', ')':
		return len(b) < 3
	}
	return false
}

func (s *screen) write(chunk []byte) {
	rec := append(s.pend, chunk...)
	s.pend = nil
	num := func(p string, def int) int {
		n := def
		fmt.Sscanf(p, "%d", &n)
		return n
	}
	i := 0
	for i < len(rec) {
		b := rec[i]
		if b == 0x1b {
			if incompleteEsc(rec[i:]) {
				s.pend = append([]byte(nil), rec[i:]...)
				return
			}
			switch rec[i+1] {
			case '[':
				m := csiRe.FindSubmatch(rec[i:])
				if m == nil {
					i += 2
					continue
				}
				p, fin := string(m[1]), string(m[2])
				switch fin {
				case "H", "f":
					s.row, s.col = 1, 1
					fmt.Sscanf(p, "%d;%d", &s.row, &s.col)
				case "A":
					s.row -= num(p, 1)
				case "B":
					s.row += num(p, 1)
				case "C":
					s.col += num(p, 1)
				case "D":
					s.col -= num(p, 1)
				case "d":
					s.row = num(p, 1)
				case "G":
					s.col = num(p, 1)
				case "J":
					switch num(p, 0) {
					case 2, 3:
						for y := range s.grid {
							for x := range s.grid[y] {
								s.grid[y][x] = ' '
							}
						}
					case 0:
						for y := s.row - 1; y < s.rows; y++ {
							st := 0
							if y == s.row-1 {
								st = s.col - 1
							}
							for x := st; x < s.cols; x++ {
								s.grid[y][x] = ' '
							}
						}
					}
				case "K":
					if s.row >= 1 && s.row <= s.rows {
						for x := s.col; x <= s.cols; x++ {
							s.grid[s.row-1][x-1] = ' '
						}
					}
				case "X":
					if s.row >= 1 && s.row <= s.rows {
						for x := s.col; x < s.col+num(p, 1) && x <= s.cols; x++ {
							s.grid[s.row-1][x-1] = ' '
						}
					}
				}
				i += len(m[0])
			case ']':
				j := i + 2
				for j < len(rec) && rec[j] != 0x07 && !(rec[j] == 0x1b && j+1 < len(rec) && rec[j+1] == '\\') {
					j++
				}
				if j < len(rec) && rec[j] == 0x1b {
					j++
				}
				i = j + 1
			case '(', ')':
				i += 3
			default:
				i += 2
			}
			continue
		}
		switch {
		case b == '\r':
			s.col = 1
			i++
			continue
		case b == '\n':
			s.row++
			i++
			continue
		case b < 0x20:
			i++
			continue
		}
		str := string(rec[i:min(len(rec), i+4)])
		ru := []rune(str)[0]
		n := len(string(ru))
		if s.row >= 1 && s.row <= s.rows && s.col >= 1 && s.col <= s.cols {
			s.grid[s.row-1][s.col-1] = ru
		}
		s.col++
		i += n
	}
}

// blankStripFrames counts PRESENTED frames whose sidebar strip held no text.
// Frames, not elapsed time, are the unit that matters: tmux wraps each client
// flush in synchronized-output markers (kitty needs the feature declared —
// see liveProfile), and the terminal shows exactly those. One such frame is
// one visible flash, however few milliseconds it lasts.
//
// Sampling the grid between pty reads instead — the obvious approach — misses
// this entirely: several frames routinely arrive inside one read.
func blankStripFrames(chunks []recChunk, rows, cols, w int) int {
	s := newScreen(rows, cols)
	const end = "\x1b[?2026l"
	n := 0
	for _, c := range chunks {
		rest := string(c.Data)
		for {
			i := strings.Index(rest, end)
			if i < 0 {
				s.write([]byte(rest))
				break
			}
			s.write([]byte(rest[:i+len(end)]))
			if strings.TrimSpace(s.colText(w)) == "" {
				n++
			}
			rest = rest[i+len(end):]
		}
	}
	return n
}
