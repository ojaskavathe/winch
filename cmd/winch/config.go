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
