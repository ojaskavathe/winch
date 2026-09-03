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
	optTheme      = "@winch-theme"
	optWidth      = "@winch-width"
	optSplit      = "@winch-agents-split"
	optSeam       = "@winch-seam-style"
	optNav        = "@winch-nav-keys"
	optAgentDelay = "@winch-agent-delay"
)

// navKeys are the four keys the sidebar treats as pane navigation, named the
// way tmux names them. The sidebar is a pane like any other, so the keys that
// move between panes should move within it — and which keys those ARE is the
// user's business, not winch's. C-hjkl was hardcoded, which is only right for
// people who happen to share that binding.
type navKeys struct {
	Left  string `json:"left,omitempty"`
	Down  string `json:"down,omitempty"`
	Up    string `json:"up,omitempty"`
	Right string `json:"right,omitempty"`
}

// navDefault is the vim-tmux-navigator convention, used when nothing is
// configured and nothing could be detected.
var navDefault = navKeys{Left: "C-h", Down: "C-j", Up: "C-k", Right: "C-l"}

func (n navKeys) String() string {
	return fmt.Sprintf("left=%s down=%s up=%s right=%s", n.Left, n.Down, n.Up, n.Right)
}

// navByte resolves a tmux key name to the single byte a terminal sends for it.
//
// Deliberately narrow: C-<letter> and bare printable characters, which is what
// pane-navigation bindings realistically use. Arrow keys need no entry — the
// TUI already reads them as CSI sequences whatever the config says — and M-
// keys are excluded because they arrive as ESC-prefixed pairs that the escape
// state machine would have to disambiguate from a real escape.
func navByte(name string) (byte, bool) {
	switch {
	case name == "":
		return 0, false
	case len(name) == 3 && (name[0] == 'C' || name[0] == 'c') && name[1] == '-':
		c := name[2] | 0x20 // lowercase
		if c < 'a' || c > 'z' {
			return 0, false
		}
		return c - 'a' + 1, true
	case len(name) == 1 && name[0] >= 0x20 && name[0] < 0x7f:
		return name[0], true
	}
	return 0, false
}

// navHit reports whether an input byte is the key named. An unset name never
// matches — notably not byte 0, which is what an unnamed key would otherwise
// resolve to and would swallow C-Space.
func navHit(name string, b byte) bool {
	want, ok := navByte(name)
	return ok && want == b
}

// resolved drops any key winch cannot actually match on, so the TUI never
// waits for a byte that will not come.
func (n navKeys) resolved() navKeys {
	keep := func(s string) string {
		if _, ok := navByte(s); ok {
			return s
		}
		return ""
	}
	return navKeys{Left: keep(n.Left), Down: keep(n.Down), Up: keep(n.Up), Right: keep(n.Right)}
}

// loadNavKeys resolves the sidebar's navigation keys: the explicit option if
// there is one, otherwise whatever the user's own tmux binds move between
// panes with, otherwise C-hjkl.
func loadNavKeys(ctl *control) {
	s := strings.TrimSpace(optStr(ctl, optNav))
	if s != "" && !strings.EqualFold(s, "auto") {
		n, err := parseNavKeys(s)
		if err == nil {
			uiNav = n.resolved()
			log.Printf("config: %s=%q -> %s", optNav, s, uiNav)
			return
		}
		log.Printf("config: %s=%q: %v, detecting instead", optNav, s, err)
	}
	det := detectNavKeys(ctl).resolved()
	uiNav = navKeys{
		Left:  firstNonEmpty(det.Left, navDefault.Left),
		Down:  firstNonEmpty(det.Down, navDefault.Down),
		Up:    firstNonEmpty(det.Up, navDefault.Up),
		Right: firstNonEmpty(det.Right, navDefault.Right),
	}
	log.Printf("config: nav keys %s (detected %s)", uiNav, det)
}

// navPtr is uiNav for the wire. A pointer so an older daemon's snapshot — or
// any snapshot built before the option was read — leaves the field absent and
// the TUI keeps its own default, rather than receiving four empty strings and
// concluding the user unbound everything.
func navPtr() *navKeys {
	n := uiNav
	return &n
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// parseNavKeys reads "left,down,up,right" — the order tmux itself lists the
// directions in for select-pane. A field may be empty or "-" to leave that
// direction unbound.
func parseNavKeys(s string) (navKeys, error) {
	f := strings.Split(s, ",")
	if len(f) != 4 {
		return navKeys{}, fmt.Errorf("want 4 comma-separated keys (left,down,up,right), got %d", len(f))
	}
	for i := range f {
		f[i] = strings.TrimSpace(f[i])
		if f[i] == "-" {
			f[i] = ""
		}
		if f[i] != "" {
			if _, ok := navByte(f[i]); !ok {
				return navKeys{}, fmt.Errorf("key %q is not a C-<letter> or a single character", f[i])
			}
		}
	}
	return navKeys{Left: f[0], Down: f[1], Up: f[2], Right: f[3]}, nil
}

// detectNavKeys reads the user's own pane-navigation keys out of tmux's root
// key table, by looking for what each key ultimately DOES rather than for any
// particular plugin's spelling. vim-tmux-navigator wraps its select-pane in an
// if-shell; a plain config binds it directly; both say "select-pane -D"
// somewhere in the command, and that is the whole test.
//
// Root table only. A prefixed binding cannot reach the sidebar at all — tmux
// swallows the prefix — so adopting one would bind the sidebar to a key that
// never arrives.
func detectNavKeys(ctl *control) navKeys {
	lines, err := ctl.run("list-keys -T root")
	if err != nil {
		return navKeys{}
	}
	return navFromLines(lines)
}

// navFromLines is detectNavKeys' parsing half, split out so it can be tested
// against real `list-keys` output without a tmux server.
//
// Keys winch cannot match on are still RECORDED here, and dropped later by
// resolved(). That way `winch doctor` can report "your up key is M-Up and I
// cannot use it" instead of silently showing the default and leaving the user
// to wonder why their binding did nothing.
func navFromLines(lines []string) navKeys {
	var got navKeys
	for _, ln := range lines {
		key, cmd, ok := splitRootBind(ln)
		if !ok {
			continue
		}
		// First binding wins: tmux itself resolves a duplicated key to the
		// last one loaded, but list-keys prints in table order and the
		// realistic duplicate is a leftover, not the live one.
		switch {
		case strings.Contains(cmd, "select-pane -U") && got.Up == "":
			got.Up = key
		case strings.Contains(cmd, "select-pane -D") && got.Down == "":
			got.Down = key
		case strings.Contains(cmd, "select-pane -L") && got.Left == "":
			got.Left = key
		case strings.Contains(cmd, "select-pane -R") && got.Right == "":
			got.Right = key
		}
	}
	return got
}

// splitRootBind pulls the key and the command out of one `list-keys -T root`
// line: `bind-key  -T root C-j  if-shell ...`.
func splitRootBind(ln string) (key, cmd string, ok bool) {
	f := strings.Fields(ln)
	for i := 1; i+1 < len(f); i++ {
		if f[i-1] == "-T" && f[i] == "root" {
			return strings.Trim(f[i+1], `"`), strings.Join(f[i+2:], " "), true
		}
	}
	return "", "", false
}

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
	uiAgentRowsRaw = strings.TrimSpace(optStr(ctl, optAgentRows))
	if _, why := parseAgentRows(uiAgentRowsRaw); why != "" {
		log.Printf("config: %s: %s, using the default layout", optAgentRows, why)
	}
	uiBorderLines = borderLines(ctl)
	loadSeamStyle(ctl)
	loadNavKeys(ctl)

	// @winch-agent-delay off: drop the focus/resize separation delays on
	// dock transitions around agent panes. Only sensible on a tmux whose
	// presentation layer holds the client sync open across a pane's own
	// ?2026 region (patched); on stock tmux the delays are what keep a
	// Claude Code pane from presenting its repaint one glyph at a time.
	if s := strings.ToLower(strings.TrimSpace(optStr(ctl, optAgentDelay))); s != "" {
		switch s {
		case "off", "0", "false", "no":
			d.agentDelayOff = true
		case "on", "1", "true", "yes":
			d.agentDelayOff = false
		default:
			log.Printf("config: %s=%q is not on/off, ignoring", optAgentDelay, s)
		}
	}

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
	uiSeamStyle = expandStyle(ctl, strings.TrimSpace(optStr(ctl, optSeam)))
	if uiSeamStyle == "" {
		uiSeamStyle = resolveStyle(ctl, "pane-active-border-style")
	}
}

// expandStyle resolves a style written as a FORMAT down to the literal
// directives it currently evaluates to.
//
// padCell renders the seam style into a tmux conditional by turning its commas
// into separate #[] directives — tmux splits a conditional at the first comma
// not inside #{}, and does not count #[]. That rewrite is only correct for a
// plain directive list. A conditional style's commas are not separators, so
// `#{?client_prefix,fg=red,fg=blue}` came out as
// `#[#{?client_prefix]#[fg=red]#[fg=blue}]` — the same class of bug that took
// the command prompt confinement down, one option over.
//
// The fallback path never had it: resolveStyle already expands, because
// catppuccin writes pane-active-border-style as a #{?pane_in_mode,...} chain.
// Only the user's own @winch-seam-style went in raw. Expanding freezes it at
// dock time, exactly as the fallback is frozen, which is the same trade already
// made there.
func expandStyle(ctl *control, s string) string {
	if s == "" || !strings.Contains(s, "#{") {
		return s
	}
	lines, err := ctl.run("display-message -p " + q(s))
	if err != nil || len(lines) != 1 {
		return "" // unusable; the caller falls back to the border style
	}
	return strings.TrimSpace(lines[0])
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
