package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
)

// User configuration lives in tmux GLOBAL user options rather than in daemon
// memory or a dotfile beside the socket. Three reasons, in order of how much
// they bit us:
//
//   - the dragged sidebar width used to die with the daemon, so every deploy
//     (pkill + restart) silently reset it;
//   - tmux options can be preset in tmux.conf, so the same knob serves as
//     "my default" and "what I just dragged to";
//   - `show-options -g | grep winch` makes the whole surface discoverable,
//     where a 0600 file in /tmp was not.
//
// Naming convention, matching what was already here: @winch-<name> with a
// HYPHEN is user config; @winch_<name> with an UNDERSCORE is daemon runtime
// state (@winch_docked, @winch_sidebar, @winch_agents...). The sweep code
// relies on that distinction too — config must survive a sweep, state must not.
//
// Lifetime is the tmux server's. That is the right scope: it matches the
// theme option that was already read this way, and anything wanting to
// outlive a server restart belongs in tmux.conf, which these options read
// from for free.
const (
	optTheme = "@winch-theme"
	optWidth = "@winch-width"
	optSplit = "@winch-agents-split"
	optSeam  = "@winch-seam-style"
)

// Bounds are enforced on BOTH read and write: a hand-edited tmux.conf is as
// likely a source of nonsense as a drag, and a 4-column sidebar or a 0.99
// split renders as garbage rather than failing loudly.
const (
	minWidth, maxWidth = 18, 80
	minSplit, maxSplit = 0.1, 0.9
)

// loadConfig reads every user option into the daemon and the hub, so a TUI
// spawned later is born with them (they ride the connect snapshot) instead of
// painting a default and correcting.
func (d *daemon) loadConfig(ctl *control) {
	uiTheme = strings.TrimSpace(optStr(ctl, optTheme))
	uiBorderLines = borderLines(ctl)
	loadSeamStyle(ctl)

	if s := optStr(ctl, optWidth); s != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			if n < minWidth || n > maxWidth {
				log.Printf("config: %s=%d out of range %d..%d, ignoring", optWidth, n, minWidth, maxWidth)
			} else {
				d.dockW = n
				d.h.setWidth(n)
			}
		} else {
			log.Printf("config: %s=%q is not a number, ignoring", optWidth, s)
		}
	}

	if s := optStr(ctl, optSplit); s != "" {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			if f < minSplit || f > maxSplit {
				log.Printf("config: %s=%v out of range %v..%v, ignoring", optSplit, f, minSplit, maxSplit)
			} else {
				d.h.setSplit(f)
			}
		} else {
			log.Printf("config: %s=%q is not a number, ignoring", optSplit, s)
		}
	}

	if bench {
		log.Printf("config: theme=%q width=%d split=%v", uiTheme, d.dockW, d.h.split)
	}
}

// A colour stored once and rendered into whichever dialect the consumer needs:
// ANSI escapes for the TUI, which writes to a pty, and #rrggbb for the status
// pad, which writes into a tmux format string.
//
// Two of them have to be exactly right in BOTH dialects at once. The pad's seam
// glyph sits directly above the divider the TUI paints while zoomed, cell to
// cell, so any drift between the two shows up as a broken corner — and it shows
// up silently, because each side looks perfectly reasonable on its own. They
// were hand-written twice, in two encodings, tied together by a comment; this is
// that comment made load-bearing.
type colour struct{ r, g, b int }

func (c colour) fg() string  { return fmt.Sprintf("\033[38;2;%d;%d;%dm", c.r, c.g, c.b) }
func (c colour) bg() string  { return fmt.Sprintf("\033[48;2;%d;%d;%dm", c.r, c.g, c.b) }
func (c colour) hex() string { return fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b) }

var (
	// seamGround is the sidebar's own ground, a step darker than the terminal
	// (catppuccin mantle). tui.go's pal.bg and the pad's invisible columns.
	seamGround = colour{24, 24, 37}
	// seamLine is the chrome colour the TUI draws its divider in (catppuccin
	// overlay0). tui.go's pal.muted and the pad's glyph while scrubbing.
	seamLine = colour{108, 112, 134}
)

// dividerPad is the slack the TUI needs beyond the list column before it will
// draw a divider of its own: below listW+dividerPad the list fills the pane and
// there is no edge for the pad's glyph to continue. Named because the status
// pad has to mirror the same test in a tmux format, and a bare +2 in two
// dialects drifts without anything noticing.
const dividerPad = 2

// loadSeamStyle resolves the colour of the sidebar's edge: @winch-seam-style if
// the user set one, otherwise whatever tmux would paint an active border in.
//
// Called at every DOCK as well as at attach. It used to be attach-only, which
// meant changing pane-active-border-style left the seam disagreeing with the
// border it continues until the daemon was restarted — and nothing said so; the
// corner just looked wrong. Per dock is the right granularity: it costs one
// round trip on a path that already makes several, and "change the theme,
// toggle the sidebar, it is right" is what a person would expect.
func loadSeamStyle(ctl *control) {
	uiSeamStyle = strings.TrimSpace(optStr(ctl, optSeam))
	if uiSeamStyle == "" {
		uiSeamStyle = resolveStyle(ctl, "pane-active-border-style")
	}
}

// borderLines reads tmux's pane-border-lines. A WINDOW option, so -gw; an
// empty answer means "assume the default" rather than "no border".
func borderLines(ctl *control) string {
	lines, err := ctl.run("show-options -gwqv pane-border-lines")
	if err != nil || len(lines) != 1 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}

// borderGlyph is the vertical border character tmux draws for a given
// pane-border-lines. `number` labels segment MIDPOINTS with digits and draws
// │ everywhere else — the pad's cell is an edge, so │ is right there too.
func borderGlyph(lines string) string {
	switch lines {
	case "double":
		return "║"
	case "heavy":
		return "┃"
	case "simple":
		// ASCII on purpose: the setting exists for terminals that cannot
		// render the box-drawing set, and the pad is a literal string in a
		// format, so it gets none of tmux's ACS fallback.
		return "|"
	default:
		return "│"
	}
}

// resolveStyle turns a border style option into a concrete style string. Two
// things stand in the way of just reading it: the value can be a format that
// only means something once expanded (catppuccin's is a #{?pane_in_mode,...}
// chain), and it can be the literal `default`, which means the terminal's
// colours on a border but the BAR's colours in a status line. borderStyle
// handles the second, display-message the first.
func resolveStyle(ctl *control, opt string) string {
	lines, err := ctl.run("display-message -p " + q(borderStyle(opt)))
	if err != nil || len(lines) != 1 {
		return "fg=terminal"
	}
	s := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(lines[0]), "#["), "]")
	if s == "" {
		return "fg=terminal"
	}
	return s
}

// optStr reads one global user option. An UNSET user option exits nonzero
// with stderr noise even under -q, so an error is read as "unset", never as
// a failure worth reporting.
func optStr(ctl *control, name string) string {
	lines, err := ctl.run("show-options -gqv " + name)
	if err != nil || len(lines) != 1 {
		return ""
	}
	return lines[0]
}

// saveOpt persists one user option. Best-effort: losing a preference is not
// worth failing the interaction that produced it.
func saveOpt(ctl *control, name, val string) {
	_, _ = ctl.run(fmt.Sprintf("set-option -g %s %s", name, q(val)))
}
