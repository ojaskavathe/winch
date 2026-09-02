package main

import "strings"

// Billboard emulation: the exact post-carve content of a pane, computed
// without resizing anything. tmux's reflow is deterministic — grid_reflow
// splits each logical line at the pane width (a wide rune that would
// straddle the boundary starts the next row) and joins lines it wrapped
// earlier — so a capture of JOINED logical lines (capture-pane -J) can be
// re-split at the docked width and land cell-for-cell where tmux will. This
// only holds for primary-screen content, but that is the only content that
// needs it: history accumulates ONLY in the primary screen, so a pane too
// heavy to carve is by construction a pane whose billboard can be emulated.
// Alt-screen panes repaint themselves and cannot be predicted — and carry no
// history, so carving them for real is ~free (the gate in preview.go prices
// exactly that).
//
// The capture spans emulatePad history rows above the visible screen: when a
// pane narrows, its logical lines occupy MORE rows, and the extra rows of
// the post-resize screen come from what is currently scrollback.
const emulatePad = 300

// emuPane is one pane's emulated visible region.
type emuPane struct {
	rows   []string
	cx, cy int
	cursor bool
}

// capAux is what emulation needs to know about a pane beyond its framePane:
// whether it sits in the alternate screen, its history depth, and its
// CURRENT geometry (the framePane's gets rewritten to the prediction before
// capture).
type capAux struct {
	alt    bool
	hist   int
	w1, h1 int
}

// splitAt re-splits one logical line (escape sequences inline) into rows of
// at most w columns, the way grid_reflow_split does — including its cell
// accounting: the grid stores a wide rune as a leading cell (width 2) PLUS a
// padding cell that reflow counts as one more unit, so a line splits one
// column early for every wide rune in the row (measured on 3.7b: 10a+世+36b
// resized to 30 splits after 17 b's, display column 29). Escapes take no
// width and never split. Returns the rows and each row's DISPLAY width — the
// padding units steer the split only. An empty line is one empty row. The
// escape state machine mirrors cleanLine's, but emits everything — this
// output is wire content, not paint.
func splitAt(s string, w int) (rows []string, widths []int) {
	if w < 1 {
		w = 1
	}
	var b strings.Builder
	units, disp, esc := 0, 0, 0
	flush := func() {
		rows = append(rows, b.String())
		widths = append(widths, disp)
		b.Reset()
		units, disp = 0, 0
	}
	for _, r := range s {
		switch esc {
		case 1: // after ESC
			b.WriteRune(r)
			switch r {
			case '[':
				esc = 2
			case ']':
				esc = 3
			default:
				esc = 0
			}
			continue
		case 2: // CSI
			b.WriteRune(r)
			if r >= 0x40 && r <= 0x7e {
				esc = 0
			}
			continue
		case 3: // OSC
			b.WriteRune(r)
			if r == 0x07 {
				esc = 0
			} else if r == 0x1b {
				esc = 4
			}
			continue
		case 4: // OSC, after ESC (ST?)
			b.WriteRune(r)
			if r == '\\' || r == 0x07 {
				esc = 0
			} else if r != 0x1b {
				esc = 3
			}
			continue
		}
		if r == 0x1b {
			b.WriteRune(r)
			esc = 1
			continue
		}
		rw := runeWidth(r)
		if units+rw > w {
			flush()
		}
		b.WriteRune(r)
		units += rw
		disp += rw
		if rw == 2 {
			// The wide rune's padding cell: one more unit, no display. If it
			// crosses the boundary it starts the next row (as tmux's does),
			// occupying an invisible unit there.
			if units+1 > w {
				flush()
				units = 1
			} else {
				units++
			}
		}
	}
	flush()
	return rows, widths
}

// plainText strips every escape sequence, leaving only printable cells.
func plainText(s string) string {
	var b strings.Builder
	esc := 0
	for _, r := range s {
		switch esc {
		case 1:
			switch r {
			case '[':
				esc = 2
			case ']':
				esc = 3
			default:
				esc = 0
			}
			continue
		case 2:
			if r >= 0x40 && r <= 0x7e {
				esc = 0
			}
			continue
		case 3:
			if r == 0x07 {
				esc = 0
			} else if r == 0x1b {
				esc = 4
			}
			continue
		case 4:
			if r == '\\' || r == 0x07 {
				esc = 0
			} else if r != 0x1b {
				esc = 3
			}
			continue
		}
		if r == 0x1b {
			esc = 1
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// logicalSpans aligns a pane's two captures of the same range — the plain
// rows (-N) and the joined lines (-J) — and returns how many raw rows each
// logical line spans. The joined capture alone can't tell (its wraps came
// from write-time display accounting, which differs from reflow accounting
// around wide runes), and the plain capture alone can't either (an
// exactly-full unwrapped row is indistinguishable from a wrap). Each joined
// line is matched to the run of plain rows whose concatenated text it is;
// both sides compare with trailing spaces trimmed, because capture pads row
// tails with artifact spaces (probed on 3.7b: a printf'd 85-column line
// captures with a variable run of trailing blanks that survive no reflow).
// Returns nil on any misalignment.
func logicalSpans(plain, joined []string) []int {
	spans := make([]int, 0, len(joined))
	r := 0
	for _, jl := range joined {
		target := strings.TrimRight(plainText(jl), " ")
		accText := ""
		start, matched := r, false
		for r < len(plain) {
			accText += plainText(plain[r])
			r++
			trimmed := strings.TrimRight(accText, " ")
			if trimmed == target {
				matched = true
				break
			}
			if len(trimmed) > len(target) {
				return nil
			}
		}
		if !matched {
			return nil
		}
		spans = append(spans, r-start)
	}
	if r != len(plain) {
		return nil
	}
	return spans
}

// cursorLocate maps the cursor from visible coordinates onto a logical line:
// which line (index into the reconstruction) and how many display columns
// into it. plain has hist history rows above the h1 visible rows, so the
// cursor's raw row is simply hist+cy1; the offset is that row's position
// within its line — earlier rows' display widths, plus cx1.
func cursorLocate(plain []string, spans []int, hist, cy1, cx1 int) (line, off int, ok bool) {
	row := hist + cy1
	if row < 0 || row >= len(plain) {
		return 0, 0, false
	}
	at := 0
	for j, span := range spans {
		if row < at+span {
			off = cx1
			for k := at; k < row; k++ {
				_, wd := splitAt(plain[k], 1<<30)
				off += wd[0]
			}
			return j, off, true
		}
		at += span
	}
	return 0, 0, false
}

// emulatePane rewraps a pane's logical lines into its docked geometry.
//
//	lines      the -J capture's logical lines
//	hist       history rows actually captured (min(emulatePad, history_size))
//	w2 x h2    the predicted docked geometry
//	cline,coff the cursor's logical line and display-column offset
//	           (cursorLocate); cursor=false if hidden or unlocatable
//
// The first line is dropped when it may be a partial tail of a line that
// started above the capture — its wrap phase would be wrong — unless it is
// all there is. Content cut above the returned window is folded into an SGR
// carry and re-seeded onto the first row, the state a real capture of the
// resized pane would have emitted there.
func emulatePane(lines []string, hist, w2, h2, cline, coff int, cursor bool) emuPane {
	var carry sgrState
	if hist >= emulatePad && len(lines) > 1 {
		carry.fold(lines[0])
		lines = lines[1:]
		cline--
		if cline < 0 {
			cursor = false
		}
	}
	// Rewrap from the last line upward until the window is covered (and the
	// cursor's line processed, so it can be placed if it lands inside).
	type lineRows struct {
		rows   []string
		widths []int
	}
	var tail []lineRows
	total := 0
	li := len(lines) - 1
	for ; li >= 0; li-- {
		r2, wd2 := splitAt(lines[li], w2)
		tail = append(tail, lineRows{rows: r2, widths: wd2})
		total += len(r2)
		if total >= h2 && (!cursor || li <= cline) {
			li--
			break
		}
	}
	// tail is newest-first; flatten oldest-first.
	var rows []string
	cRow, cCol, haveCur := -1, 0, false
	for i := len(tail) - 1; i >= 0; i-- {
		lr := tail[i]
		if cursor && len(lines)-1-i == cline {
			// Which rewrapped segment holds display column coff?
			start := 0
			for k, wdt := range lr.widths {
				if coff < start+wdt || k == len(lr.widths)-1 {
					cRow, cCol, haveCur = len(rows)+k, min(coff-start, w2-1), true
					break
				}
				start += wdt
			}
			if cCol < 0 {
				cCol = 0
			}
		}
		rows = append(rows, lr.rows...)
	}
	// The visible window is the last h2 rows; everything cut above it — the
	// unprocessed remainder of lines plus the cut rows — feeds the carry.
	cut := 0
	if len(rows) > h2 {
		cut = len(rows) - h2
	}
	for i := 0; i <= li; i++ {
		carry.fold(lines[i])
	}
	for i := 0; i < cut; i++ {
		carry.fold(rows[i])
	}
	rows = rows[cut:]
	if haveCur {
		cRow -= cut
		if cRow < 0 || cRow >= len(rows) {
			haveCur = false
		}
	}
	if seed := carry.seq(); seed != "" && len(rows) > 0 {
		rows[0] = seed + rows[0]
	}
	return emuPane{rows: rows, cx: cCol, cy: cRow, cursor: haveCur}
}
