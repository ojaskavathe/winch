package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

// The sidebar TUI: one process rendering the session/window list (left, 40
// cols) and, when its pane is zoomed for scrubbing, the live preview canvas
// (right) in the same pane. Frames are
// cached per window, so scrubbing paints the cached frame in ~0ms — the
// herdr/choose-tree trick: state lives locally, painting is local. The fresh
// frame (and the 10fps live stream) replaces it moments later. The daemon
// owns every tmux interaction; this process spawns nothing.

// benchLog, when WINCH_BENCH is set, records per-event timings so latency can
// be attributed (key handling vs paint cost vs frame arrival).
var benchLog *os.File

func benchf(format string, args ...any) {
	if benchLog == nil {
		return
	}
	fmt.Fprintf(benchLog, "%s tui ", time.Now().Format("15:04:05.000000"))
	fmt.Fprintf(benchLog, format+"\n", args...)
}

// tuiLog is ALWAYS on (<winch sock>.tui.log): low-volume lifecycle events —
// which build started, what select arrived and where it resolved — so field
// reports can be diagnosed from logs instead of guesses.
var tuiLog *os.File

func tlogf(format string, args ...any) {
	if tuiLog == nil {
		return
	}
	fmt.Fprintf(tuiLog, "%s ", time.Now().Format("01-02 15:04:05.000"))
	fmt.Fprintf(tuiLog, format+"\n", args...)
}

type store struct {
	sessions map[string]session
	windows  map[string]window
	panes    map[string]pane
}

func (st *store) apply(m wireMsg) {
	switch m.Type {
	case "snapshot":
		st.sessions = map[string]session{}
		st.windows = map[string]window{}
		st.panes = map[string]pane{}
		for _, s := range m.Sessions {
			st.sessions[s.ID] = s
		}
		for _, w := range m.Windows {
			st.windows[w.ID] = w
		}
		for _, p := range m.Panes {
			st.panes[p.ID] = p
		}
	case "diff":
		for _, o := range m.Ops {
			switch o.Kind {
			case "session":
				if o.Op == "del" {
					delete(st.sessions, o.ID)
				} else {
					var v session
					if json.Unmarshal(o.Value, &v) == nil {
						st.sessions[o.ID] = v
					}
				}
			case "window":
				if o.Op == "del" {
					delete(st.windows, o.ID)
				} else {
					var v window
					if json.Unmarshal(o.Value, &v) == nil {
						st.windows[o.ID] = v
					}
				}
			case "pane":
				if o.Op == "del" {
					delete(st.panes, o.ID)
				} else {
					var v pane
					if json.Unmarshal(o.Value, &v) == nil {
						st.panes[o.ID] = v
					}
				}
			}
		}
	}
}

type row struct {
	label   string
	window  string // preview target
	pane    string // agent rows: the agent's pane — commit focuses it
	sess    string // session rows: the session id (identity across rebuilds)
	att     bool   // session rows: some client is attached
	session bool
	arow    bool   // agents-section row (rendered in the pinned bottom region)
	gap     bool   // blank spacer between session groups; never selectable
	head    bool   // section heading (" sessions"); never selectable
	cont    bool   // continuation line of a multi-row entry (herdr's model)
	create  bool   // the `n` field: a session that does not exist yet
	agent   string // worst agent state (window rows) / the state (agent rows)
	styled  string // optional pre-styled label (only fg/dim codes, self-closing);
	// used when it fits — truncation falls back to the plain label
}

// inert rows are chrome or continuations: selection passes over them
// (a continuation highlights with its owner instead). The create field is
// inert too — it owns the keyboard while it exists, so a selection on it
// would mean nothing and a selection moving THROUGH it would be worse.
func (r row) inert() bool { return r.gap || r.head || r.cont || r.create }

// palette: the sidebar's theme as raw SGR fragments. The look lives on a
// brightness ladder (text > subtext > muted, plus bold/dim) — that ladder
// is what reads as "font sizes" in a terminal. Default is catppuccin mocha
// (herdr's default); `@winch-theme terminal` keeps an ANSI-16 mapping that
// inherits the host scheme instead.
type palette struct {
	text    string // fg: selected/active names
	subtext string // fg: inactive names
	muted   string // fg: chrome — border, rules, headers, tails
	accent  string // fg: attached dot, active cues
	mauve   string // fg: git branch
	bg      string // bg: the sidebar's own ground, a step darker than the terminal
	fill    string // bg: selection row fill
	actFill string // bg: the client's current session card
	red     string // blocked
	yellow  string // working
	teal    string // done (finished unseen)
	green   string // idle
	peach   string // background (turn done, side work live)
}

func rgb(r, g, b int) string   { return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b) }
func rgbBG(r, g, b int) string { return fmt.Sprintf("\033[48;2;%d;%d;%dm", r, g, b) }

var themes = map[string]palette{
	"catppuccin": {
		text: rgb(205, 214, 244), subtext: rgb(166, 173, 200), muted: seamLine.fg(),
		accent: rgb(137, 180, 250), mauve: rgb(203, 166, 247),
		bg: seamGround.bg(), fill: rgbBG(49, 50, 68), actFill: rgbBG(30, 30, 46),
		red: rgb(243, 139, 168), yellow: rgb(249, 226, 175),
		teal: rgb(148, 226, 213), green: rgb(166, 227, 161),
		peach: rgb(250, 179, 135),
	},
	"terminal": {
		text: "", subtext: "\033[37m", muted: "\033[90m",
		accent: "\033[34m", mauve: "\033[35m",
		bg: "", fill: "\033[100m", actFill: "\033[100m",
		red: "\033[91m", yellow: "\033[33m", teal: "\033[36m", green: "\033[32m",
		peach: "\033[95m",
	},
}

// pal is set from the snapshot's theme before the first paint.
var pal = themes["catppuccin"]

// listW is the list column's width. The daemon owns the real value (width
// msgs update it); dragging the │ border in browse mode changes it here
// first and reports back on release.
var listW = listWidth

// uiSessionOrder is the user's session order, by name (@winch-session-order).
// Sessions named here list first, in this order; the rest follow in creation
// order. Set from the snapshot and edited in place by J/K, which persists it.
var uiSessionOrder []string

// curSess is the session the client is REALLY on (tracked from daemon
// selects: dock, commit, nav all send one). Its card carries the active
// fill — herdr's active_row_bg, distinct from the selection cursor.
var curSess string

// curAgent is the agent pane the client is sitting in — herdr's
// is_active_pane, which its whole dim ladder hangs off (sidebar.rs:1503).
// Derived from the world rather than plumbed: tmux already ships both flags
// we need (window.Active = the session's current window, pane.Active = that
// window's active pane).
//
// STICKY, and that is the entire point. Docking moves tmux's focus ONTO the
// sidebar, so a live reading would blank the highlight at exactly the moment
// you are looking at the list. Only another agent replaces it. A session
// change clears it instead, because a highlight pointing into a session you
// have left is worse than no highlight at all.
var curAgent string

// setCurSess moves the client's session and drops the sticky agent highlight
// when the session actually changed. The two are one invariant, so they get
// one function rather than two lines that have to be remembered together.
func setCurSess(id string) {
	if id != curSess {
		curAgent = ""
	}
	curSess = id
}

// pickCurAgent chooses the client's own agent: the one in the window it is
// actually on. Active answers whenever the user is IN the agent; Last answers
// the moment they are not, and docking makes that the common case — winch
// takes the focus for its own pane, so at the instant the sidebar first paints
// the agent you were working in is active nowhere. Preferring Active keeps two
// agents in one window deterministic (map order would otherwise decide).
//
// Only ever ASSIGNS. curAgent is sticky and the one thing that clears it is a
// session change, in setCurSess.
//
// Called from BOTH the row build and applySelect, because either can be the
// one that completes the picture: the build has the world but may run before
// any select has said which session the client is on, and a select can arrive
// after the last build a static world will ever provoke. Recomputing in only
// one of them left the highlight missing about one dock in five.
func (st *store) pickCurAgent() {
	var active, last string
	for _, p := range st.panes {
		if p.Agent == "" || p.SessionID != curSess || !st.windows[p.WindowID].Active {
			continue
		}
		if p.Active {
			active = p.ID
		}
		if p.Last {
			last = p.ID
		}
	}
	switch {
	case active != "":
		curAgent = active
	case last != "":
		curAgent = last
	}
}

// The inline text field. `r` renames the selected session, `n` names a new
// one; while active it owns the keyboard until enter or esc, and paintList
// renders its line as buffer + accent █ cursor.
//
// renSess is the rename TARGET, or for a create the session whose working
// directory the new one inherits — you pressed `n` on a row, and "like that
// one, but new" is what that means.
type editKind int

const (
	editNone editKind = iota
	editRename
	editCreate
)

var (
	editWhat editKind
	renSess  string
	renBuf   string
	// editReplace is herdr's name_input_replace_on_type: the field opens
	// prefilled with a suggestion and SELECTED, so the first character
	// typed replaces it. Accepting a suggestion and clearing it are both
	// one keystroke; neither is the other's punishment.
	editReplace bool
)

// The x confirmation. Closing a session or a window destroys work that is not
// coming back, so x arms and y fires — and the prompt takes over the row it
// was pressed on rather than opening anywhere else, so what is about to die is
// the thing under the cursor, named, while you answer.
//
// confirmSess and confirmPane are the two kill shapes: a session row closes
// the whole session, an agent row closes only that agent's pane.
// The target is held as an id rather than a row index, and the prompt finds
// its row again on every paint — rows are rebuilt from scratch on every diff,
// and an index would quietly come to mean a different row than the one you
// armed. It is the rule the selection restore already follows.
var (
	confirmSess string // session id, when a session row is armed
	confirmPane string // agent pane id, when an agent row is armed
	confirmName string // what to call the target in the prompt
)

// Where the selection last sat in each of the sidebar's two regions, so the
// pane-navigation keys land where you left rather than at the top. Held as row
// IDENTITY (session id, agent pane id) because indexes are rebuilt on every
// diff and would come to mean a different row.
var (
	lastSessKey  string
	lastAgentKey string
)

func confirming() bool { return confirmSess != "" || confirmPane != "" }

func confirmClear() { confirmSess, confirmPane, confirmName = "", "", "" }

// confirmTargets reports whether the prompt belongs on this row. Agent rows
// key on the PANE: agents share a window often enough that a window would arm
// a neighbour's row too, and kill it.
func confirmTargets(r row) bool {
	switch {
	case confirmSess != "":
		return r.session && r.sess == confirmSess
	case confirmPane != "":
		return r.arow && !r.cont && r.pane == confirmPane
	}
	return false
}

func setTheme(name string) {
	if p, ok := themes[name]; ok {
		pal = p
	}
}

// winsOf returns a session's windows sorted by index.
func (st *store) winsOf(sid string) []window {
	wins := make([]window, 0, 8)
	for _, w := range st.windows {
		if w.SessionID == sid {
			wins = append(wins, w)
		}
	}
	sort.Slice(wins, func(i, j int) bool { return wins[i].Index < wins[j].Index })
	return wins
}

// sortSessions orders sessions the way the sidebar lists them: names present
// in uiSessionOrder first, in that order; the rest after, by creation time —
// stable and non-alphabetical, so new sessions land at the bottom and nothing
// reshuffles on its own.
func sortSessions(ss []session) {
	idx := make(map[string]int, len(uiSessionOrder))
	for i, name := range uiSessionOrder {
		idx[name] = i
	}
	sort.Slice(ss, func(i, j int) bool {
		oi, iok := idx[ss[i].Name]
		oj, jok := idx[ss[j].Name]
		if iok && jok {
			return oi < oj
		}
		if iok != jok {
			return iok // an ordered session sits ahead of an unordered one
		}
		if ss[i].Created != ss[j].Created {
			return ss[i].Created < ss[j].Created
		}
		return ss[i].ID < ss[j].ID
	})
}

// sessionOrderNames materializes the sidebar's current session order as a name
// list — what J/K edit, so the first move pins today's order and the swap
// lands relative to it.
func sessionOrderNames(st *store) []string {
	ss := make([]session, 0, len(st.sessions))
	for _, s := range st.sessions {
		ss = append(ss, s)
	}
	sortSessions(ss)
	names := make([]string, len(ss))
	for i, s := range ss {
		names[i] = s.Name
	}
	return names
}

// rows builds the list: sessions as herdr-style space cards (windows are
// NOT listed — they're auto-named command noise; h/l pages the selected
// session's windows through the billboard instead, winPick remembering the
// choice), then the pinned agents section. winPick may be nil.
func (st *store) rows(winPick map[string]string) []row {
	out := []row{{label: " sessions", head: true}}
	// A session being named does not exist yet, so it gets a line of its
	// own directly under the heading. Anywhere else would move as you type
	// — the list is sorted by name — and a field that jumps mid-word is
	// unusable.
	if editWhat == editCreate {
		out = append(out, row{create: true})
	}
	sessions := make([]session, 0, len(st.sessions))
	for _, s := range st.sessions {
		sessions = append(sessions, s)
	}
	sortSessions(sessions)

	// Worst agent state per session: blocked > done > background > working
	// > idle. background outranks working because it wants you (the turn is
	// done) and sits under done because its side work may yet change what
	// the answer is.
	rank := map[string]int{"blocked": 5, "done": 4, "background": 3, "working": 2, "idle": 1}
	agg := map[string]string{}
	var agents []pane
	for _, p := range st.panes {
		if p.Agent != "" {
			agents = append(agents, p)
		}
		if p.AgentState != "" && rank[p.AgentState] > rank[agg[p.SessionID]] {
			agg[p.SessionID] = p.AgentState
		}
	}
	st.pickCurAgent()

	for _, s := range sessions {
		target := ""
		for _, w := range st.winsOf(s.ID) {
			if w.Active {
				target = w.ID
			}
		}
		if pick, ok := winPick[s.ID]; ok {
			if w, live := st.windows[pick]; live && w.SessionID == s.ID {
				target = pick
			}
		}
		// A blank row between cards — herdr's row_gap = 1, which it
		// documents as "the previous spacing". Its own default is 0, and
		// that is the wrong trade here: herdr's sidebar is wider and its
		// cards are single-purpose, while these stack a name over a branch
		// in 26 columns, where cards with no rule between them read as one
		// block of text.
		out = append(out, row{gap: true})
		out = append(out, row{
			label: "   " + s.Name, window: target, sess: s.ID,
			session: true, att: s.Attached, agent: agg[s.ID],
		})
		// Row two of the card: git identity (branch, ↑ahead ↓behind) —
		// herdr's spaces layout. No repo, no row.
		if s.Branch != "" {
			git, styled := s.Branch, pal.mauve+s.Branch
			if s.Ahead > 0 {
				git += fmt.Sprintf(" ↑%d", s.Ahead)
				styled += fmt.Sprintf("%s ↑%d", pal.green, s.Ahead)
			}
			if s.Behind > 0 {
				git += fmt.Sprintf(" ↓%d", s.Behind)
				styled += fmt.Sprintf("%s ↓%d", pal.red, s.Behind)
			}
			out = append(out, row{
				label: "   " + git, styled: "   " + styled + "\033[39m",
				window: target, sess: s.ID, cont: true,
			})
		}
	}
	// The agents section: one CARD per agent pane, attention-sorted
	// (blocked > done > background > working > idle), enter jumps to the pane. These
	// rows render in a PINNED bottom region under a labeled rule
	// (paintList), so a long session tree never scrolls the agents out of
	// sight.
	//
	// Three rows, herdr's documented layout for claude:
	//
	//	[ui.sidebar.agents.rows_by_agent]
	//	claude = [["state_icon", "workspace", "tab"],
	//	          ["terminal_title_stripped"],
	//	          ["agent"]]
	//
	// The middle row is the point. Agents publish their conversation name
	// in the OSC title, so it is a name winch is HANDED rather than one it
	// has to invent — and it is the only field that separates two agents in
	// the same window. It used to ride as a dim tail on the state line and
	// was dropped whenever the whole line overflowed, which at any real
	// title length was always: five claude panes rendered as five copies of
	// their session name, and the switcher looked like it picked at random.
	//
	// No state_text, matching herdr: the dot carries the state.
	if len(agents) > 0 {
		sort.Slice(agents, func(i, j int) bool {
			ri, rj := rank[agents[i].AgentState], rank[agents[j].AgentState]
			if ri != rj {
				return ri > rj
			}
			// Equal attention: most recently changed first — herdr's
			// priority tie-break. Among several idle agents the one that
			// just finished is the one being looked for; pane number
			// answers a question nobody asked. Pane number still breaks a
			// tie between agents that have never transitioned, so the
			// order is always total.
			if agents[i].AgentSeq != agents[j].AgentSeq {
				return agents[i].AgentSeq > agents[j].AgentSeq
			}
			return paneNum(agents[i].ID) < paneNum(agents[j].ID)
		})
		avail := listW - 3
		for _, p := range agents {
			out = append(out, row{gap: true, arow: true})
			// Row one: state_icon (painted at col 2 by paintList) then
			// workspace, tab, agent — herdr's tokens, joined by its
			// separator (" · " between tokens; the plain space after the
			// icon is already there because winch paints it separately).
			//
			// The card's rows are the user's (agentrows.go). The default
			// keeps the agent kind riding the head row rather than owning
			// one, as herdr's default spends a whole line on it — a
			// reasonable default for someone running four different agents
			// and a wasted line for anyone running one.
			for ri, spec := range uiAgentRows.rows {
				vals := uiAgentRows.values(spec, st, p)
				if len(vals) == 0 {
					continue
				}
				// The first rendered row carries the glyph and the state;
				// later ones are continuations. The dot itself is
				// paintList's business — a working row spins there, off the
				// sidebar's own clock. Baking a frame into the row would
				// mean rebuilding every row eight times a second to animate
				// one cell.
				head := ri == 0
				// The ambient colour paintList will be using for this row,
				// so a token that has to restore it can.
				ambient := pal.subtext
				if p.ID == curAgent {
					ambient = pal.text
				}
				label, styled := fitAgentRow(vals, avail, head, ambient)
				r := row{
					label: label, styled: styled,
					window: p.WindowID, pane: p.ID, arow: true, cont: !head,
				}
				if head {
					r.agent = p.AgentState
				}
				out = append(out, r)
			}

		}
	}
	return out
}

// sessPath is a session's working directory: the path of the active pane in
// its active window, which is where a shell opened there would land.
func (st *store) sessPath(sid string) string {
	active := ""
	for _, w := range st.windows {
		if w.SessionID == sid && w.Active {
			active = w.ID
		}
	}
	for _, p := range st.panes {
		if p.WindowID == active && p.Active && p.Path != "" {
			return p.Path
		}
	}
	for _, p := range st.panes {
		if p.SessionID == sid && p.Path != "" {
			return p.Path
		}
	}
	return ""
}

// baseName is herdr's derive_label_from_cwd, minus the git part: the last
// path element, with ~ for home. winch has no repo-root lookup in the TUI,
// and a directory basename is what you would have typed anyway.
func baseName(path string) string {
	path = strings.TrimRight(path, "/")
	if path == "" {
		return ""
	}
	if home := os.Getenv("HOME"); home != "" && path == strings.TrimRight(home, "/") {
		return "~"
	}
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// tabLabel is herdr's `tab` token, ported to tmux honestly — and empty when
// the token would say nothing, which is herdr's rule too:
//
//	show_tab = multi_tab || !tab.is_auto_named()
//
// A herdr tab is either a name someone chose or a NUMBER. tmux windows
// auto-rename to the running command, which is the same "I made this up"
// state with a noisier value: every agent window here is `.claude-wrapped`,
// which says nothing and repeats row three. So a deliberately named window
// shows its name, an auto-named one shows its index the way herdr shows a
// number — and a session with only ONE window shows neither, because an
// index that is 1 on every card is a column spent on nothing.
func (st *store) tabLabel(sid, wid string) string {
	w, ok := st.windows[wid]
	if !ok {
		return ""
	}
	if w.Name != "" && !st.autoNamed(wid) {
		return w.Name
	}
	if len(st.winsOf(sid)) > 1 {
		return strconv.Itoa(w.Index)
	}
	return ""
}

// killLabel names an agent's pane for the x confirm — its kind ("kill
// claude?"), which is short enough to survive the sidebar's width.
//
// The kind alone would be ambiguous between two claudes, except that the
// prompt replaces the FIRST line of the card it was armed on and the second
// line is the conversation name, which stays put. So the two lines read
// together as the question and its subject, and the selection highlight says
// which card is answering.
func (st *store) killLabel(pid string) string {
	p, ok := st.panes[pid]
	if !ok {
		return "agent"
	}
	if p.Agent != "" {
		return p.Agent
	}
	return "agent"
}

// autoNamed reports whether tmux picked this window's name rather than a
// person. Compared against every pane in the window, not just the agent's:
// tmux names a window after its ACTIVE pane, which may be the editor sitting
// beside the agent.
func (st *store) autoNamed(wid string) bool {
	w, ok := st.windows[wid]
	if !ok || w.Name == "" {
		return true
	}
	for _, p := range st.panes {
		if p.WindowID == wid && baseCmd(w.Name) == baseCmd(p.Command) {
			return true
		}
	}
	return false
}

// baseCmd strips what nix and tmux add around a command name, so the
// `.claude-wrapped` a window is named matches the `.claude-wrapped` its pane
// is running. Wrapper scripts are the norm in this setup, not an edge case.
func baseCmd(s string) string {
	return strings.TrimSuffix(strings.TrimPrefix(s, "."), "-wrapped")
}

// fitTokens shortens s to at most max columns by dropping whole trailing
// words, marking the elision. Mid-word truncation reads as breakage rather
// than as brevity; a dropped word reads as a summary. Falls back to a rune
// cut only when the first word alone will not fit.
func fitTokens(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len([]rune(s)) <= max {
		return s
	}
	words := strings.Fields(s)
	for n := len(words) - 1; n > 0; n-- {
		cut := strings.Join(words[:n], " ") + "…"
		if len([]rune(cut)) <= max {
			return cut
		}
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// agentTaskTitle strips the state ornament (spinner char, ✳) off an agent's
// pane title, leaving the task summary.
func agentTaskTitle(t string) string {
	_, name := splitOrnament(t)
	return name
}

// splitOrnament separates an agent title's leading state ornament from its
// text. The ornament is the agent's OWN spinner frame, which is why winch
// shows it rather than animating a glyph of its own on a timer: it advances
// when the agent advances, and it stops when the agent stops. A spinner that
// keeps turning for a wedged process is a lie told at 3fps.
//
// Kept apart from the title on the wire (pane.Spin) so a frame change diffs
// only the ornament. Folding it back into Title would make every frame a
// change to the NAME as well, and the name is what the row is keyed on.
func splitOrnament(t string) (orn, name string) {
	t = strings.TrimSpace(t)
	if r := []rune(t); len(r) > 1 {
		c := r[0]
		if c == '✳' || (c >= 0x2800 && c <= 0x28FF) || (c >= 0x25D0 && c <= 0x25D3) {
			return string(c), strings.TrimSpace(string(r[1:]))
		}
	}
	return "", t
}

func cmdTui(tmuxSock, winchSock string) {
	conn, err := dialEnsure(tmuxSock, winchSock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "winch tui: %v\r\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if os.Getenv("WINCH_BENCH") != "" {
		benchLog, _ = os.OpenFile(winchSock+".tui-bench.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}
	tuiLog, _ = os.OpenFile(winchSock+".tui.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	exe, _ := os.Executable()
	cols, height := surfaceSize()
	tlogf("start build=%s pane=%s size=%dx%d", exe, os.Getenv("TMUX_PANE"), cols, height)

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err == nil {
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}
	// Hide cursor; disable autowrap so frame lines wider than the preview
	// region clip at the right edge instead of wrapping over the layout.
	// Mouse: button presses + SGR encoding — tmux (mouse on) forwards
	// events to the pane with pane-relative coordinates.
	// 1002 (button-motion tracking), not 1000: dragging the tree/agents
	// divider needs motion events while the button is held.
	// 1049 (alternate screen) is load-bearing, not cosmetic: tmux reflows a
	// pane's grid on every width change EXCEPT while the alternate screen is
	// active (window_pane_resize passes reflow = saved_grid == NULL), and
	// leaving a zoomed scrub shrinks this pane 480 -> 26. On the normal
	// screen that rewraps the wide canvas into a wall of text in the strip
	// (the "blob"), which is why unzooming used to respawn the whole process.
	// In the alternate screen the grid is CLIPPED instead — and since the
	// zoomed layout already paints the list in columns 1..listW, the clip
	// leaves exactly the list. Probe-verified against tmux 3.7b.
	fmt.Print("\033[?1049h\033[?25l\033[?7l\033[?1002h\033[?1006h")
	defer fmt.Print("\033[?1006l\033[?1002l\033[?25h\033[?7h\033[?1049l")

	msgs := make(chan wireMsg, 64)
	go func() {
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
		for sc.Scan() {
			var m wireMsg
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				msgs <- m
			}
		}
		close(msgs)
	}()

	keys := make(chan byte, 16)
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				close(keys)
				return
			}
			for _, b := range buf[:n] {
				keys <- b
			}
		}
	}()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	var winchT *time.Timer
	var winchC <-chan time.Time

	send := func(m cmdMsg) {
		m.Type = "cmd"
		b, _ := json.Marshal(m)
		conn.Write(append(b, '\n'))
	}
	// hello is sent AFTER the first paint, not on connect — it is the
	// signal that this pane is ready to be looked at, so it has to mean
	// "I am on screen" rather than "I exist".
	helloSent := false
	sayHello := func() {
		if !helloSent {
			helloSent = true
			conn.Write([]byte(`{"type":"hello","role":"list"}` + "\n"))
		}
	}

	st := &store{}
	sel := 0
	esc := 0           // escape-sequence state: arrows + SGR mouse
	var mbuf []byte    // SGR mouse params after \x1b[<
	dragging := false  // left button held on the agents divider
	widthDrag := false // left button held on the │ width border (wide mode)
	var rows []row
	// winPick: per-session window choice (h/l pages the billboard through a
	// session's windows; the pick survives rebuilds until the window dies).
	winPick := map[string]string{}

	// Per-window frame cache with generations, so "did I already paint
	// exactly this?" is an integer compare, never a deep one. dgen is the
	// DAEMON's generation for this frame — delta frames apply only when
	// their base matches it.
	type cached struct {
		gen   int
		dgen  int
		panes []framePane
		at    time.Time
	}
	// A cache older than this never paints: it exists to make ACTIVE
	// scrubbing instant (frames milliseconds old), but a window last
	// billboarded minutes ago is ancient content — painting it flashes the
	// old screen for the ~50ms until the fresh capture lands, which reads
	// as random flicker. Better to leave the canvas and wait.
	frameTTL := 3 * time.Second
	if testFast {
		frameTTL = 1 * time.Second
	}
	frames := map[string]cached{}
	gen := 0
	paintedWin, paintedGen := "", -1
	paintedCurPane := ""
	var paintedPanes []framePane

	target := func() string {
		if sel >= 0 && sel < len(rows) {
			return rows[sel].window
		}
		return ""
	}
	// targetPane: agent rows carry the agent's pane, so committing one
	// lands focus on the agent itself, not the window's last-active pane.
	targetPane := func() string {
		if sel >= 0 && sel < len(rows) {
			return rows[sel].pane
		}
		return ""
	}
	// Painting is split: a selection move repaints the list column only, a
	// frame repaints the preview region only. Neither clears the screen —
	// everything is positioned overwrites, so tmux's own cell diff decides
	// what actually reaches the terminal (a selection move is ~2 changed
	// rows, not 45k cells). Streaming updates of the same window diff at
	// line level: a claude spinner tick repaints 1-3 lines, not the region.
	paintFrameFor := func(win string) {
		// The billboarded cursor follows the SELECTED row's pane (agent
		// rows carry one) — switching rows within the same window changes
		// nothing but the cursor, so the skip-guard must see it too.
		cp := targetPane()
		c, ok := frames[win]
		if !ok || (win == paintedWin && c.gen == paintedGen && cp == paintedCurPane) {
			return
		}
		if time.Since(c.at) > frameTTL {
			benchf("stale skip win=%s age_ms=%d", win, time.Since(c.at).Milliseconds())
			return // stale: the fresh frame is already on its way
		}
		cols, _ := surfaceSize()
		avail := cols - (listW + 1)
		if avail <= 0 {
			return // narrow (docked): no canvas; keep the cache unmarked
		}
		// Frames are cached RAW and scaled at paint time, so a resize (zoom
		// in/out) just re-scales on the next paint — no cache invalidation.
		scaled := scaleFrame(c.panes, avail)
		var prev []framePane
		// Diff against what is ON SCREEN, not against "the same window's
		// last frame". The screen does not care where content came from, so
		// a scrub step to a same-shaped window is a line diff like any
		// other; keying this on window identity made the hot path — moving
		// the selection — take the full-repaint branch by definition.
		if len(paintedPanes) > 0 && sameRects(paintedPanes, scaled) {
			prev = paintedPanes
		}
		// Borders carry the active pane's color, and they live in the gaps
		// no pane writes — so a diffed paint still has to redraw them when
		// the active pane changed.
		paintFrame(scaled, prev, cp, paintedCurPane, prev == nil || !sameActive(prev, scaled))
		paintedWin, paintedGen, paintedPanes, paintedCurPane = win, c.gen, scaled, cp
	}
	// clampSel keeps the selection inside the list and off chrome — a
	// rebuild can land the raw index on a heading, gap or rule.
	clampSel := func() {
		if sel >= len(rows) {
			sel = len(rows) - 1
		}
		if sel < 0 {
			sel = 0
		}
		for sel < len(rows)-1 && rows[sel].inert() {
			sel++
		}
		for sel > 0 && rows[sel].inert() {
			sel-- // bottom edge was chrome: back up into the list
		}
	}
	// applySelect moves the selection onto the row the daemon named — an
	// agent's own row when a pane is given, otherwise the window's SESSION
	// row (windows aren't listed; the pick remembers which window, so daemon
	// nav keeps the billboard on the real one).
	applySelect := func(win, pane string) bool {
		found := false
		for i, r := range rows {
			if pane != "" {
				if r.arow && !r.cont && r.pane == pane {
					sel, found = i, true
					break
				}
				continue
			}
			if r.session && r.sess == st.windows[win].SessionID {
				winPick[r.sess] = win
				rows[i].window = win
				sel, found = i, true
				break
			}
		}
		if w, ok := st.windows[win]; ok {
			setCurSess(w.SessionID)
			st.pickCurAgent()
		}
		return found
	}
	// The spinner's clock. Armed only while some agent is working, so an
	// idle server never wakes for it — that is the whole reason herdr's
	// version was expensive enough to delete: it drove a 60fps repaint of
	// the entire UI, always on. This repaints one narrow strip, 8 times a
	// second, only when there is something to animate.
	var spinT *time.Ticker
	var spinC <-chan time.Time
	armSpin := func(rows []row) {
		want := false
		for _, r := range rows {
			if r.arow && r.agent == "working" {
				want = true
				break
			}
		}
		switch {
		case want && spinT == nil:
			spinT = time.NewTicker(spinPeriod)
			spinC = spinT.C
		case !want && spinT != nil:
			spinT.Stop()
			spinT, spinC = nil, nil
			spinTick = 0 // next turn starts at frame one, not mid-rotation
		}
	}
	defer func() {
		if spinT != nil {
			spinT.Stop()
		}
	}()

	paintAll := func() {
		rows = st.rows(winPick)
		armSpin(rows)
		clampSel()
		lastListOut = "" // geometry moved: never suppress this repaint
		paintList(rows, sel)
		// Size/world may have shifted the regions: the screen no longer
		// holds what we last painted, so the diff baseline goes too (the
		// canvas diff keys on geometry alone now, not on window identity).
		paintedWin, paintedGen, paintedPanes = "", -1, nil
		paintFrameFor(target())
	}
	// rebuild recomputes the row list and re-finds the selection by identity.
	// Needed when the list changes SHAPE rather than content — the create
	// field appearing or going — because a row inserted near the top shifts
	// every index after it, and `sel` is an index. relayout alone repaints
	// the rows it already has, which is why a rename works without this and
	// a create does not.
	rebuild := func() {
		prev := row{}
		if sel >= 0 && sel < len(rows) {
			prev = rows[sel]
		}
		rows = st.rows(winPick)
		armSpin(rows)
		for i, r := range rows {
			switch {
			case prev.session && r.session && r.sess == prev.sess:
			case prev.arow && r.arow && !r.cont && prev.pane != "" && r.pane == prev.pane:
			default:
				continue
			}
			sel = i
			break
		}
		clampSel()
	}

	// shrinkExpected: a commit/close was sent from wide mode, so the pane is
	// about to shrink to 40 cols. tmux REWRAPS the grid on width change —
	// the canvas mashes into a wall of wrapped text in the 40-col strip (the
	// "blob" flicker) until the winch repaint covers it. Painting anything
	// more into the canvas meanwhile only widens that window (a 100KB frame
	// write can delay the covering repaint ~100ms), so canvas paints pause
	// until the winch lands. Pre-CLEARING instead is worse: tmux flushes the
	// clear as its own sync-wrapped frame (probe-verified), which blanks the
	// billboard visibly before the swap.
	shrinkExpected := false
	// The daemon always replays a select to a freshly spawned sidebar
	// (dockOpen does, and so does the respawn fallback). Painting the
	// snapshot before it arrives shows the selection on row one for a few
	// ms and then jumps it — one visible flick per respawn, i.e. on every
	// Enter that lands back on the docked window. So the first list paint
	// waits for that select, with a deadline in case one never comes.
	selPending := true
	selDeadline := time.After(150 * time.Millisecond)
	// Browse mode: cached frame paints locally NOW; the daemon refreshes it
	// (and streams it live) right behind, neighbors prefetched warm. Docked
	// (narrow) mode: the same preview cmd IS the scrub — the daemon moves
	// the real main area to the target window; prefetch means nothing.
	requestFrames := func() {
		cur := target()
		if cur != "" {
			send(cmdMsg{Cmd: "preview", Window: cur})
		}
		if narrowMode() {
			return
		}
		seen := map[string]bool{cur: true, "": true}
		for _, dir := range []int{-1, 1} {
			i := sel + dir
			for i >= 0 && i < len(rows) && rows[i].inert() {
				i += dir // neighbors sit past chrome rows (gaps, headings)
			}
			if i >= 0 && i < len(rows) && !seen[rows[i].window] {
				seen[rows[i].window] = true
				send(cmdMsg{Cmd: "preview", Window: rows[i].window, Prefetch: true})
			}
		}
	}
	// moveSel clamps and applies a selection delta; painting is the caller's
	// job — the key loop coalesces a drained batch into one paint.
	moveSel := func(delta int) bool {
		if len(rows) == 0 {
			return false
		}
		step := 1
		if delta < 0 {
			step = -1
		}
		next := sel + delta
		for next >= 0 && next < len(rows) && rows[next].inert() {
			next += step // roll over chrome rows in the travel direction
		}
		if next < 0 {
			next = 0
		}
		if next >= len(rows) {
			next = len(rows) - 1
		}
		for next > 0 && rows[next].inert() {
			next-- // clamped onto chrome at an edge: back into the list
		}
		if next == sel || rows[next].inert() {
			return false
		}
		sel = next
		return true
	}
	// jumpRegion is what the pane-navigation keys do: move to the OTHER
	// region, not one row.
	//
	// The sidebar has two, the sessions list and the pinned agents section,
	// and they are the closest thing it has to panes. So the keys that move
	// between panes move between them — C-j from the sessions goes to the
	// agents the way it would go to the split below, in one press. Stepping a
	// row at a time is what j/k are for, and with five sessions on screen the
	// difference is one press against a dozen.
	//
	// Where you were in each region is remembered, so coming back lands where
	// you left rather than at the top. Identity, not index: the rows are
	// rebuilt on every diff.
	jumpRegion := func(toAgents bool) bool {
		if sel >= 0 && sel < len(rows) && rows[sel].arow == toAgents {
			return false // already there; tmux does not wrap either
		}
		if sel >= 0 && sel < len(rows) {
			if rows[sel].arow {
				lastAgentKey = rows[sel].pane
			} else {
				lastSessKey = rows[sel].sess
			}
		}
		want := lastSessKey
		if toAgents {
			want = lastAgentKey
		}
		target, first := -1, -1
		for i, r := range rows {
			if r.inert() || r.arow != toAgents {
				continue
			}
			if first < 0 {
				first = i
			}
			key := r.sess
			if r.arow {
				key = r.pane
			}
			if want != "" && key == want {
				target = i
				break
			}
		}
		if target < 0 {
			target = first
		}
		if target < 0 || target == sel {
			return false // that region is empty — no agents, say
		}
		sel = target
		return true
	}
	// cycleWin (h/l, arrows): pages the selected session's windows through
	// the billboard — the tab-bar gesture, without listing windows.
	cycleWin := func(delta int) bool {
		if sel < 0 || sel >= len(rows) || !rows[sel].session {
			return false
		}
		wins := st.winsOf(rows[sel].sess)
		if len(wins) < 2 {
			return false
		}
		cur := 0
		for i, w := range wins {
			if w.ID == rows[sel].window {
				cur = i
				break
			}
		}
		pick := wins[(cur+delta+len(wins))%len(wins)].ID
		winPick[rows[sel].sess] = pick
		rows[sel].window = pick
		return true
	}
	// click handles a left press at 1-based pane coordinates. List column:
	// select the row; a second click on the selected row enters it. Canvas:
	// hit-test the painted billboard's pane rects and enter the target with
	// the clicked split focused. Returns whether the selection moved (the
	// caller paints).
	click := func(x, y int, setShrink func()) bool {
		cols, height := surfaceSize()
		lw := listW
		if cols <= listW+dividerPad {
			lw = cols
		}
		if x <= lw {
			i := layoutList(rows, sel, height).rowAt(y-1, len(rows))
			for i > 0 && i < len(rows) && rows[i].cont {
				i-- // a continuation line clicks as its owner
			}
			if i < 0 || i >= len(rows) || rows[i].inert() {
				return false
			}
			if i == sel {
				setShrink()
				send(cmdMsg{Cmd: "commit", Window: target(), Pane: targetPane()})
				return false
			}
			sel = i
			return true
		}
		if x < listW+dividerPad || paintedWin == "" || paintedWin != target() {
			return false
		}
		cx, cy := x-(listW+1)-1, y-1
		for _, p := range paintedPanes {
			if p.ID != "" && cx >= p.Left && cx < p.Left+p.Width &&
				cy >= p.Top && cy < p.Top+p.Height {
				setShrink()
				send(cmdMsg{Cmd: "commit", Window: paintedWin, Pane: p.ID})
				return false
			}
		}
		return false
	}

	// The process is per-dock: spawned by dockOpen, killed with its pane at
	// undock. It also exits itself when the daemon connection closes, so a
	// dead daemon never leaves a zombie sidebar pane behind.
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				tlogf("exit: daemon connection closed")
				return
			}
			switch m.Type {
			case "reply":
				// A failed commit/close means the shrink is never coming —
				// unfreeze canvas painting (the stream refills it in a tick).
				if shrinkExpected && m.OK != nil && !*m.OK {
					shrinkExpected = false
					paintFrameFor(target())
				}
			case "snapshot", "diff":
				if m.Type == "snapshot" {
					if m.Theme != "" {
						setTheme(m.Theme)
					}
					// The daemon is the only side that can read tmux's key
					// table, so which keys move between panes arrives here.
					if m.Nav != nil {
						uiNav = m.Nav.resolved()
					}
					// Card layout before the first paint, for the same
					// reason as the theme: repainting into the user's
					// layout after showing the default is a visible jump.
					// The daemon already logged any parse error; here the
					// fallback just has to be silent and correct.
					uiAgentRows, _ = parseAgentRows(m.Rows)
					// Width before the first paint: laying out at the default
					// and correcting to the user's dragged width is a jump.
					if m.Width >= 18 {
						listW = m.Width
					}
					// Same for the agents divider. The TUI is per-dock, so
					// without this the split reset on every single M-s.
					if m.Split > 0 {
						listSplit = m.Split
					}
					// Session order, first-paint reasoning like the split. nil
					// means no custom order — the list falls to creation order.
					uiSessionOrder = m.Order
				}
				// Selection is sticky to the ROW IDENTITY, not the index:
				// world churn (a window or session appearing/dying — agents
				// do this constantly) rebuilds rows, and an index-anchored
				// highlight visibly jumps to whatever slid into its slot.
				var prev row
				if sel >= 0 && sel < len(rows) {
					prev = rows[sel]
				}
				st.apply(m)
				rows = st.rows(winPick)
				armSpin(rows) // a world change is how an agent starts working
				for i, r := range rows {
					switch {
					case prev.session && r.session && r.sess == prev.sess:
					case prev.arow && r.arow && !r.cont && prev.pane != "" && r.pane == prev.pane:
					default:
						continue
					}
					sel = i
					break
				}
				// An armed prompt whose target has gone — killed from
				// elsewhere, or by this very confirm — has to disarm, or it
				// holds the keyboard while drawing nothing.
				if confirming() {
					live := false
					for _, r := range rows {
						if confirmTargets(r) {
							live = true
							break
						}
					}
					if !live {
						confirmClear()
					}
				}
				if selPending && m.Type == "snapshot" && m.Select != "" {
					// The daemon stamped where the selection belongs into this
					// TUI's very first world, so the first paint is already
					// right — no wait, no correcting repaint.
					applySelect(m.Select, m.SelectPane)
					selPending = false
				}
				clampSel()
				if selPending {
					// Selection still unknown: hold the paint rather than
					// show a default row that is about to jump.
					break
				}
				paintList(rows, sel)
				sayHello()
			case "width":
				if m.Width >= 18 && m.Width != listW {
					listW = m.Width
					paintAll()
				}
			case "surface":
				// Authoritative pane width from the daemon around a scrub
				// zoom (Cols>0) or its end (Cols==0). Repaint at the new
				// surface so the billboard widens even when tmux never
				// resized our pty (and so a stale-pty unzoom narrows back).
				if surfaceCols != m.Cols {
					surfaceCols = m.Cols
					paintAll()
				}
			case "select":
				found := applySelect(m.Window, m.Pane)
				selPending = false
				clampSel()
				tlogf("select win=%s found=%v quiet=%v sel=%d rows=%d", m.Window, found, m.Quiet, sel, len(rows))
				paintList(rows, sel)
				sayHello()
				// A quiet select moves the highlight and stops there. The
				// frame request is what makes an off-window selection scrub,
				// and a selection the DAEMON placed has not been navigated
				// to — it has been offered.
				if !shrinkExpected && !m.Quiet {
					paintFrameFor(target())
					requestFrames()
				}
			case "frame":
				benchf("frame win=%s current=%v delta=%v", m.Window, m.Window == target(), m.Delta)
				// Input outranks painting: with keys already queued, a full
				// frame paint (20-120ms in a saturated tmux) would land
				// behind the selection the user has already left. The frame
				// is cached either way — the key batch paints it if the
				// target still matches, the stream re-covers it otherwise.
				quiet := len(keys) == 0 && !shrinkExpected
				if m.Fresh {
					// The daemon skipped a prefetch because this window has
					// not changed since we were last given it. Restamp so the
					// cache doesn't age out of frameTTL and lose the instant
					// paint the prefetch was for. Unknown window: ignore —
					// the real preview will bring content when we land there.
					if prev, ok := frames[m.Window]; ok {
						prev.at = time.Now()
						frames[m.Window] = prev
					}
					break
				}
				if m.Delta {
					// Row delta against the cache at Base. Patch copies every
					// touched Lines slice — the painted state may share lines
					// with the cache (scaleFrame's unscaled path returns the
					// frame as-is), and an in-place patch would blind the
					// prev-diff painter to its own change.
					c, ok := frames[m.Window]
					if !ok || c.dgen != m.Base || !applyDelta(&c.panes, m.Frame) {
						// Lost lineage (fresh TUI, evicted cache): drop it and
						// re-request — a command-driven preview always ships full.
						benchf("delta resync win=%s", m.Window)
						if m.Window == target() {
							requestFrames()
						}
						break
					}
					gen++
					c.gen, c.dgen, c.at = gen, m.Gen, time.Now()
					frames[m.Window] = c
					if m.Window == target() && quiet {
						paintFrameFor(m.Window)
					}
					break
				}
				if prev, ok := frames[m.Window]; ok && framesEqual(prev.panes, m.Frame) {
					// Confirming frame, content unchanged: no repaint, but
					// the cache is proven current — restamp it (and adopt the
					// daemon's gen, or the next delta's base won't match), or
					// a static window's cache would age out while streaming.
					// Target-only paint: a stale-skip may have left the
					// canvas waiting for exactly this confirmation.
					prev.at, prev.dgen = time.Now(), m.Gen
					frames[m.Window] = prev
					if m.Window == target() && quiet {
						paintFrameFor(m.Window)
					}
					break
				}
				gen++
				frames[m.Window] = cached{gen: gen, dgen: m.Gen, panes: m.Frame, at: time.Now()}
				if m.Window == target() && quiet {
					paintFrameFor(m.Window)
				}
			}
		case b, ok := <-keys:
			if !ok {
				return
			}
			// Coalesce: drain every key already queued and paint ONCE for
			// the final selection. A held j autorepeats faster than tmux
			// drains full-canvas paints (20-120ms each, log-measured on a
			// 440x95 client); painting every intermediate row shoves ~100KB
			// a step into the saturated pty and the input queue backs up —
			// the held-scrub mush. NOT a debounce: nothing waits, a lone key
			// finds the queue empty and paints immediately.
			moved := false
			relayout := false // divider drag: repaint the list, nothing else
			resized := false  // width drag: repaint everything, canvas included
			wheeled := false  // one gesture-commit per batch, drop the rest
			setShrink := func() { shrinkExpected = !narrowMode() }
			// commitAt: enter the billboard split under 1-based pane coords.
			// Wheel, middle and right button all route here — any mouse
			// gesture on a split means "interact with that pane for real",
			// and the real pane's own handling (scrollback, paste, menu)
			// then has full parity. Keyboard stays the picker's.
			commitAt := func(mx, my int) bool {
				if paintedWin == "" || paintedWin != target() {
					return false
				}
				cx, cy := mx-(listW+1)-1, my-1
				for _, p := range paintedPanes {
					if p.ID != "" && cx >= p.Left && cx < p.Left+p.Width &&
						cy >= p.Top && cy < p.Top+p.Height {
						setShrink()
						send(cmdMsg{Cmd: "commit", Window: paintedWin, Pane: p.ID})
						return true
					}
				}
				return false
			}
			for {
				switch {
				case esc == 1 && b == '[':
					esc = 2
				case esc == 2 && b == '<': // SGR mouse
					esc, mbuf = 3, mbuf[:0]
				case esc == 3:
					if b == 'M' || b == 'm' {
						esc = 0
						if btn, mx, my, ok := parseMouse(mbuf); ok {
							if b == 'm' { // release
								if dragging {
									// Report on release, not per motion event:
									// the daemon writes a tmux option for each
									// one, and a drag is dozens of events.
									dragging = false
									send(cmdMsg{Cmd: "split", Split: listSplit})
								}
								if widthDrag {
									// the daemon owns the width from here
									widthDrag = false
									send(cmdMsg{Cmd: "width", Width: listW})
								}
								break
							}
							switch {
							case btn&64 != 0: // wheel
								cols, _ := surfaceSize()
								lw := listW
								if cols <= listW+dividerPad {
									lw = cols
								}
								if mx <= lw {
									// over the list: wheel walks the selection
									if btn&3 == 0 {
										moved = moveSel(-1) || moved
									} else if btn&3 == 1 {
										moved = moveSel(1) || moved
									}
								} else if !wheeled && commitAt(mx, my) {
									// Over the canvas: a scroll gesture means
									// "read that pane" — enter it for real.
									// The billboard can't scroll faithfully
									// (alt-screen apps like vim have no
									// history to show).
									wheeled = true
								}
							case (btn&3 == 1 || btn&3 == 2) && btn&32 == 0:
								// middle/right press over a split: paste and
								// menus belong to the real pane — enter it
								if !wheeled && commitAt(mx, my) {
									wheeled = true
								}
							case btn&3 == 0 && btn&32 == 0: // left press
								cols, height := surfaceSize()
								lw := listW
								if cols <= listW+dividerPad {
									lw = cols
								}
								if mx <= lw && my-1 == layoutList(rows, sel, height).sepY {
									dragging = true // grab the agents divider
								} else if cols > listW+dividerPad && mx == listW+1 {
									widthDrag = true // grab the │ width border
								} else {
									moved = click(mx, my, setShrink) || moved
								}
							case btn&3 == 0 && btn&32 != 0 && widthDrag: // width drag
								cols, _ := surfaceSize()
								if nw := mx - 1; nw >= 18 && nw <= 80 && nw <= cols-40 && nw != listW {
									listW = nw
									resized = true
								}
							case btn&3 == 0 && btn&32 != 0 && dragging: // divider drag
								_, height := surfaceSize()
								if avail := height - 1; avail >= 6 {
									r := float64(my-1) / float64(avail)
									if r < 0.1 {
										r = 0.1
									}
									if r > 0.9 {
										r = 0.9
									}
									if r != listSplit {
										listSplit = r
										relayout = true
									}
								}
							}
						}
					} else if len(mbuf) < 24 {
						mbuf = append(mbuf, b)
					} else {
						esc = 0 // runaway
					}
				case esc == 2:
					esc = 0
					switch b {
					case 'A':
						moved = moveSel(-1) || moved
					case 'B':
						moved = moveSel(1) || moved
					case 'C':
						moved = cycleWin(1) || moved
					case 'D':
						moved = cycleWin(-1) || moved
					}
				case b == 0x1b:
					esc = 1
					if editWhat != editNone {
						creating := editWhat == editCreate
						editWhat, renSess, renBuf = editNone, "", "" // esc cancels
						if creating {
							rebuild()
						}
						relayout = true
					}
					if confirming() {
						confirmClear()
						relayout = true
					}
				case confirming():
					// Armed: the prompt owns the keyboard. Anything that is
					// not an explicit yes cancels — including j/k, so a
					// reflex keypress moves the selection next time rather
					// than killing whatever this time landed on.
					if b == 'y' || b == 'Y' {
						send(cmdMsg{Cmd: "kill", Sess: confirmSess, Pane: confirmPane})
					}
					confirmClear()
					relayout = true
				case editWhat != editNone:
					// The inline field owns the keyboard until enter/esc.
					switch {
					case b == '\r':
						name := strings.TrimSpace(renBuf)
						switch {
						case editWhat == editCreate && name != "":
							send(cmdMsg{Cmd: "create", Sess: renSess, Name: name})
						case editWhat == editRename && name != "" && name != st.sessions[renSess].Name:
							send(cmdMsg{Cmd: "rename", Sess: renSess, Name: name})
						}
						creating := editWhat == editCreate
						editWhat, renSess, renBuf = editNone, "", ""
						if creating {
							rebuild() // the field row goes away
						}
					case b == 0x7f, b == 0x08: // backspace
						// Backspace EDITS the suggestion rather than
						// replacing it: you reached for the tail of the
						// prefill, so you want to keep the head.
						editReplace = false
						if r := []rune(renBuf); len(r) > 0 {
							renBuf = string(r[:len(r)-1])
						}
					case b == 0x15: // ctrl-u
						editReplace = false
						renBuf = ""
					case b >= 0x20 && b != 0x7f:
						if editReplace {
							renBuf, editReplace = "", false
						}
						renBuf += string([]byte{b}) // raw byte: utf-8 rides through
					}
					relayout = true
				// vim-tmux-navigator hands its keys to this pane (the
				// @vim_navigator_pattern includes winch), so the sidebar
				// behaves like a vim split: C-l goes INTO what you're looking
				// at — the billboarded window mid-scrub, the main pane when
				// docked idle — never "escapes" back to the docked window's
				// hidden panes via a raw unzoom. C-j/C-k mirror j/k. C-h has
				// nowhere left to go and is ignored.
				case b == 'j':
					moved = moveSel(1) || moved
				case b == 'k':
					moved = moveSel(-1) || moved
				case b == 'h':
					moved = cycleWin(-1) || moved
				case b == 'l':
					moved = cycleWin(1) || moved
				// The user's own pane-navigation keys, whatever they are
				// (config.go). AFTER the letters above so a config that maps a
				// bare `l` cannot take `l` away from window paging — the
				// literal keys are the sidebar's own and win ties.
				//
				// These JUMP between the list and the agents section rather
				// than stepping a row: they are the between-panes keys, and
				// the two sections are the sidebar's panes.
				case navHit(uiNav.Down, b):
					moved = jumpRegion(true) || moved
				case navHit(uiNav.Up, b):
					moved = jumpRegion(false) || moved
				case b == 'r':
					// Rename the selected session inline, prefilled.
					if sel >= 0 && sel < len(rows) && rows[sel].session {
						editWhat, editReplace = editRename, false
						renSess = rows[sel].sess
						renBuf = st.sessions[renSess].Name
						relayout = true
					}
				case b == 'n':
					// New session, named. Prefilled from the selected
					// session's working directory and selected, so enter
					// alone takes the suggestion — herdr's create flow.
					if sel >= 0 && sel < len(rows) && rows[sel].session {
						editWhat, editReplace = editCreate, true
						renSess = rows[sel].sess
						renBuf = baseName(st.sessPath(renSess))
						rebuild() // the field is a row that did not exist
						relayout = true
					}
				case b == 'J', b == 'K':
					// Reorder the selected SESSION down (J) / up (K), and
					// persist. Sessions only — agents keep their attention-sort.
					// The first nudge pins the whole current order (creation
					// order becomes explicit) and the swap lands within it.
					if sel >= 0 && sel < len(rows) && rows[sel].session && rows[sel].sess != "" {
						name := st.sessions[rows[sel].sess].Name
						order := sessionOrderNames(st)
						from := -1
						for k, n := range order {
							if n == name {
								from = k
								break
							}
						}
						to := from + 1
						if b == 'K' {
							to = from - 1
						}
						if from >= 0 && to >= 0 && to < len(order) {
							order[from], order[to] = order[to], order[from]
							uiSessionOrder = order
							send(cmdMsg{Cmd: "order", Order: order})
							rebuild()
							relayout = true
						}
					}
				case b == 'x':
					// Arm the kill confirm on the selected row. Nothing is
					// sent yet — y fires, everything else cancels.
					if sel >= 0 && sel < len(rows) && editWhat == editNone {
						switch r := rows[sel]; {
						case r.session && r.sess != "":
							confirmSess, confirmPane = r.sess, ""
							confirmName = st.sessions[r.sess].Name
							relayout = true
						case r.arow && r.pane != "":
							confirmSess, confirmPane = "", r.pane
							confirmName = st.killLabel(r.pane)
							relayout = true
						}
					}
				case b == '\r': // enter
					shrinkExpected = !narrowMode()
					send(cmdMsg{Cmd: "commit", Window: target(), Pane: targetPane()})
				case navHit(uiNav.Right, b):
					if narrowMode() {
						// Docked idle: "pane to the right" is the pane NEXT to
						// the sidebar, not the window's last-active one (commit
						// would skip splits).
						send(cmdMsg{Cmd: "focus"})
					} else {
						// Zoomed billboard / full-screen browse: right goes INTO
						// what you're looking at, like Enter.
						shrinkExpected = true
						send(cmdMsg{Cmd: "commit", Window: target(), Pane: targetPane()})
					}
				// Left has nowhere to go — the sidebar is the leftmost thing in
				// its window — but it is swallowed rather than ignored, so it
				// cannot fall through to `default` and reset the escape state.
				case navHit(uiNav.Left, b):
				case b == 'q', b == 0x03: // q, ctrl-c
					shrinkExpected = !narrowMode()
					send(cmdMsg{Cmd: "close"})
				default:
					esc = 0
				}
				more := false
				select {
				case b, ok = <-keys:
					if !ok {
						return
					}
					more = true
				default:
				}
				if !more {
					break
				}
			}
			if resized {
				// Width changed: everything relayouts — rows refit their
				// tokens, the canvas rescales to the new offset.
				paintAll()
			} else if moved || relayout {
				benchf("key sel=%d target=%s cached=%v", sel, target(), frames[target()].panes != nil)
				paintList(rows, sel)
				if moved && !shrinkExpected {
					paintFrameFor(target())
					requestFrames()
				}
			}
		case <-spinC:
			// One frame on. Repaints the strip without rebuilding rows —
			// the frame is chosen inside paintList, so animating one cell
			// costs a paint, not a re-model.
			spinTick++
			paintList(rows, sel)
		case <-selDeadline:
			// No select arrived (a TUI spawned outside the docked flow):
			// paint what we have rather than sitting blank.
			if selPending {
				selPending = false
				tlogf("select deadline: painting without one")
				paintAll()
				sayHello()
			}
		case <-winch:
			shrinkExpected = false // the resize this was armed for has landed
			paintAll()
			// A client resize (monitor switch) rescales the docked sidebar
			// off its fixed width, and no tmux notification crosses sessions
			// to tell the daemon — this SIGWINCH is the only signal. Report
			// it DELAYED: zoom transitions fire SIGWINCH too, and an instant
			// winch cmd races into the daemon's queue right as scrub-start
			// prefetches are dispatched, making them look superseded (they
			// get abandoned, killing the billboard cache warming). A monitor
			// switch doesn't care about a 250ms wait.
			if winchT != nil {
				winchT.Stop()
			}
			winchT = time.NewTimer(250 * time.Millisecond)
			winchC = winchT.C
		case <-winchC:
			winchC = nil
			if cols, _ := surfaceSize(); cols != listW {
				send(cmdMsg{Cmd: "winch", Width: cols})
			}
		}
	}
}

// scaleFrame maps a frame captured at its window's real width onto the
// canvas: pane rects scale proportionally — splits land where they will
// actually be once the window is entered and docked — and every line is
// truncated to its scaled cell so panes never bleed into a neighbor. A
// plain crop (the old behavior) put a 100/99 split's border at col 100 of
// a 159-col canvas and clipped the right pane to a sliver. Identity when
// the frame already fits (the docked window's own billboard).
func scaleFrame(frame []framePane, avail int) []framePane {
	fw := 0
	for _, p := range frame {
		if p.Left+p.Width > fw {
			fw = p.Left + p.Width
		}
	}
	if fw <= avail || fw <= 0 {
		return frame
	}
	s := float64(avail) / float64(fw)
	out := make([]framePane, len(frame))
	for i, p := range frame {
		// A pane with a left neighbor owns the column AFTER its scaled
		// border; its neighbor ends AT the border. Scaling the border
		// position (Left-1) keeps content and border from ever sharing a
		// column — plain edge rounding collapses the gap and the border
		// glyph eats the pane's first character.
		x0 := 0
		if p.Left > 0 {
			x0 = int(float64(p.Left-1)*s+0.5) + 1
		}
		x1 := int(float64(p.Left+p.Width)*s + 0.5)
		w := x1 - x0
		if w < 1 {
			w = 1
		}
		lines := make([]string, len(p.Lines))
		for j, ln := range p.Lines {
			lines[j], _ = cleanLine(ln, w)
		}
		np := framePane{ID: p.ID, Left: x0, Top: p.Top, Width: w, Height: p.Height, Active: p.Active, Lines: lines}
		if p.Cursor {
			// Cursor column scales with the content; off the truncated edge
			// it simply hides (approximated billboards stay approximate).
			if cx := int(float64(p.CursorX)*s + 0.5); cx < w {
				np.Cursor, np.CursorX, np.CursorY = true, cx, p.CursorY
			}
		}
		out[i] = np
	}
	return out
}

// cleanLine renders a captured line paint-safe: truncated at max display
// columns (max < 0 means no limit), CSI sequences passed through unconsumed
// (they take no columns), OSC sequences STRIPPED — capture emits them for
// hyperlinks (grok's TUI), their payload is zero-width so counting it skews
// the pad math, and a truncation cutting one mid-way leaves a dangling OSC
// that swallows everything painted after it. Returns the line and its
// display width (runeWidth in width.go — tmux-parity, emulation depends on
// it). Trailing SGR state is deliberate: paintFrame pads in it (BCE bars),
// then resets.
func cleanLine(s string, max int) (string, int) {
	w := 0
	esc := 0 // 0 plain, 1 ESC, 2 CSI, 3 OSC, 4 ESC-in-OSC (ST?)
	var b strings.Builder
	for _, r := range s {
		switch esc {
		case 1:
			switch r {
			case '[':
				b.WriteRune(0x1b)
				b.WriteRune(r)
				esc = 2
			case ']':
				esc = 3
			default: // two-byte ESC sequence
				b.WriteRune(0x1b)
				b.WriteRune(r)
				esc = 0
			}
			continue
		case 2:
			b.WriteRune(r)
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
		rw := runeWidth(r)
		if max >= 0 && w+rw > max {
			break
		}
		w += rw
		b.WriteRune(r)
	}
	return b.String(), w
}

// charAtCol walks a captured line to display column col — the same
// CSI/OSC-skipping walk as cleanLine — and returns the rune there (space
// when the line is shorter, or col lands inside a wide rune's second cell).
func charAtCol(s string, col int) rune {
	w, esc := 0, 0
	for _, r := range s {
		switch esc {
		case 1:
			if r == '[' {
				esc = 2
			} else if r == ']' {
				esc = 3
			} else {
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
		rw := runeWidth(r)
		if w == col && rw > 0 {
			return r
		}
		w += rw
		if w > col {
			break
		}
	}
	return ' '
}

// parseMouse decodes the params of an SGR mouse report (btn;x;y).
func parseMouse(buf []byte) (btn, x, y int, ok bool) {
	parts := strings.Split(string(buf), ";")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var vals [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, 0, 0, false
		}
		vals[i] = n
	}
	return vals[0], vals[1], vals[2], true
}

// applyDelta patches a delta frame's changed rows into a cached full frame,
// reporting false when a delta pane has no cached counterpart (resync).
// Touched slices are copied, never mutated in place: the cache may share
// them with the painted state (scaleFrame returns unscaled frames as-is),
// and an in-place write would blind the prev-diff painter to the change.
func applyDelta(panes *[]framePane, delta []framePane) bool {
	out := append([]framePane(nil), *panes...)
	for _, dp := range delta {
		pi := -1
		for i := range out {
			if out[i].ID == dp.ID {
				pi = i
				break
			}
		}
		if pi < 0 || len(dp.Rows) != len(dp.Lines) {
			return false
		}
		out[pi].Cursor, out[pi].CursorX, out[pi].CursorY = dp.Cursor, dp.CursorX, dp.CursorY
		lines := append([]string(nil), out[pi].Lines...)
		for j, r := range dp.Rows {
			if r < 0 {
				return false
			}
			for len(lines) <= r {
				lines = append(lines, "")
			}
			lines[r] = dp.Lines[j]
		}
		out[pi].Lines = lines
	}
	*panes = out
	return true
}

func framesEqual(a, b []framePane) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Left != b[i].Left || a[i].Top != b[i].Top ||
			a[i].Width != b[i].Width || a[i].Height != b[i].Height ||
			a[i].Active != b[i].Active || a[i].Cursor != b[i].Cursor ||
			a[i].CursorX != b[i].CursorX || a[i].CursorY != b[i].CursorY ||
			len(a[i].Lines) != len(b[i].Lines) {
			return false
		}
		for j := range a[i].Lines {
			if a[i].Lines[j] != b[i].Lines[j] {
				return false
			}
		}
	}
	return true
}

// surfaceCols, when > 0, overrides the width read from the pty. The daemon
// sets it (surfaceMsg) around a scrub zoom because tmux does not reliably
// resize a zoomed pane's pty when other clients are attached — the ioctl
// would keep reporting the docked width and the billboard would never
// widen. Read and written only from the single TUI event-loop goroutine.
var surfaceCols int

func surfaceSize() (int, int) {
	cols, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols <= 0 {
		cols, height = 120, 40
	}
	if surfaceCols > 0 {
		cols = surfaceCols
	}
	return cols, height
}

// narrowMode: the surface is the docked 40-col sidebar, not the full-screen
// browser. The list takes the whole width (the tmux pane border is the
// separator) and the preview region simply doesn't exist.
func narrowMode() bool {
	cols, _ := surfaceSize()
	return cols <= listW+dividerPad
}

// listTop is the scroll offset paintList uses; click mapping shares it so
// a clicked y always lands on the row that was painted there.
func listTop(n, sel, height int) int {
	top := 0
	if n > height && sel > height/2 {
		top = sel - height/2
		if top > n-height {
			top = n - height
		}
	}
	return top
}

// listSplit is the tree/agents divider ratio (herdr's sidebar_section_split,
// default 0.5). Dragging the labeled rule adjusts it; the daemon persists it
// to @winch-agents-split and stamps it into every connect snapshot, so a
// freshly spawned TUI is born with the chosen split rather than reading it
// back from a file beside the socket.
var listSplit = 0.5

// listLayout is the list column's two-region geometry: the session tree
// scrolls in its own window while agent rows stay PINNED at the bottom
// under a labeled rule. Deterministic from (rows, sel, height) — paint and
// click mapping both derive it, so a clicked y always lands on the row
// painted there. No agents (or a tiny pane) collapses to one region.
type listLayout struct {
	nTree    int // rows[:nTree] = tree, rows[nTree:] = agents
	treeH    int
	sepY     int // separator screen row; -1 = no agents region
	agentH   int
	treeTop  int
	agentTop int
}

func layoutList(rows []row, sel, height int) listLayout {
	nA := 0
	for i := len(rows) - 1; i >= 0 && rows[i].arow; i-- {
		nA++
	}
	nT := len(rows) - nA
	if nA == 0 || height < 7 {
		return listLayout{nTree: len(rows), treeH: height, sepY: -1,
			treeTop: listTop(len(rows), sel, height)}
	}
	// herdr's model (sidebar_section_heights): a FIXED ratio split — tree
	// panel on top, agents panel below, each at least 3 rows. Stable
	// geometry: the divider never moves as content changes, only when the
	// user drags it. (herdr keeps both panels even with zero agents; we
	// collapse to a full tree instead — the no-agents case above.)
	avail := height - 1
	tH := int(float64(avail)*listSplit + 0.5)
	if tH < 3 {
		tH = 3
	}
	if tH > avail-3 {
		tH = avail - 3
	}
	aH := avail - tH
	selT, selA := sel, 0
	if sel >= nT {
		selT, selA = 0, sel-nT // tree unanchored while an agent row is selected
	}
	return listLayout{nTree: nT, treeH: tH, sepY: tH, agentH: aH,
		treeTop: listTop(nT, selT, tH), agentTop: listTop(nA, selA, aH)}
}

// rowAt maps a screen row to a rows index; -1 for the separator or blanks.
func (l listLayout) rowAt(y, nRows int) int {
	switch {
	case y < l.treeH:
		i := l.treeTop + y
		if i < l.nTree {
			return i
		}
	case y == l.sepY:
	case y <= l.sepY+l.agentH:
		i := l.nTree + l.agentTop + (y - l.sepY - 1)
		if i < nRows {
			return i
		}
	}
	return -1
}

// benchListPrev remembers the last painted list content (bench mode only):
// a "list flicker" is by definition rows whose cells actually changed —
// tmux ships nothing for identical repaints — so diffing consecutive
// paints names exactly what was written.
var benchListPrev []string

// lastListOut is the strip exactly as last written, so an identical repaint
// costs a string compare instead of a pty write. Cleared by paintAll, which
// runs precisely when the pane geometry moved under us and tmux's grid can
// no longer be assumed to hold what we last sent.
var lastListOut string

// paintList redraws the list column and border only. Fixed width, padded
// with spaces — no clears, so unchanged cells cost nothing downstream.
// Wrapped in synchronized output (DECSET 2026) so tmux applies it atomically.
func paintList(rows []row, sel int) {
	start := time.Now()
	cols, height := surfaceSize()
	lw, border := listW, true
	if cols <= listW+dividerPad {
		lw, border = cols, false
	}
	lay := layoutList(rows, sel, height)
	// A two-row entry highlights as one: the fill spans the selected row
	// plus its continuation lines.
	selEnd := sel
	for selEnd+1 < len(rows) && rows[selEnd+1].cont {
		selEnd++
	}
	var cur []string
	if benchLog != nil {
		cur = make([]string, 0, height)
	}
	var b strings.Builder
	b.WriteString("\033[?2026h\033[0m")
	for y := 0; y < height; y++ {
		i := lay.rowAt(y, len(rows))
		fmt.Fprintf(&b, "\033[%d;1H", y+1)
		if y == lay.sepY {
			// the pinned agents region's labeled rule
			rule := []rune("─ agents " + strings.Repeat("─", lw))[:lw]
			b.WriteString(pal.bg + pal.muted + string(rule) + "\033[49;39m")
			if benchLog != nil {
				cur = append(cur, "=agents")
			}
			if border {
				b.WriteString(pal.bg + pal.muted + "│\033[49;39m")
			}
			continue
		}
		// Every row sits on the sidebar's own ground (pal.bg, a step darker
		// than the terminal); selection and the active card override it.
		rowBG := pal.bg
		if i >= 0 {
			switch {
			case i >= sel && i <= selEnd:
				rowBG = pal.fill
			case rows[i].sess != "" && rows[i].sess == curSess:
				rowBG = pal.actFill
			case rows[i].arow && rows[i].pane != "" && rows[i].pane == curAgent:
				// The agent's whole card, continuation row included — they
				// carry the same pane. Selection is checked FIRST on purpose:
				// herdr keeps selection_bg and active_row_bg as separate
				// tokens so the cursor stays visible while sitting on the
				// active row, and ours have to stack the same way.
				rowBG = pal.actFill
			}
		}
		if i >= 0 {
			label := []rune(rows[i].label)
			if len(label) > lw {
				label = label[:lw]
			}
			pad := strings.Repeat(" ", lw-len(label))
			switch {
			case confirming() && confirmTargets(rows[i]):
				// The prompt replaces the row it was armed on, so the thing
				// about to die is named under the cursor while you answer.
				// Red, because y here does not ask twice.
				ask := fitTokens("   kill "+confirmName+"? y/n", lw)
				apad := ""
				if n := lw - len([]rune(ask)); n > 0 {
					apad = strings.Repeat(" ", n)
				}
				b.WriteString(pal.fill + pal.red + "\033[1m" + ask + apad + "\033[22;49;39m")
			case rows[i].create,
				editWhat == editRename && rows[i].session && rows[i].sess != "" && rows[i].sess == renSess:
				// The row IS the input field.
				edit := []rune("   " + renBuf)
				if len(edit) > lw-1 {
					edit = edit[len(edit)-(lw-1):]
				}
				epad := ""
				if n := lw - len(edit) - 1; n > 0 {
					epad = strings.Repeat(" ", n)
				}
				b.WriteString(pal.fill + pal.text + "\033[1m" + string(edit) +
					pal.accent + "█" + epad + "\033[22;49;39m")
			case i >= sel && i <= selEnd:
				// Row fill + bold, not reverse video: herdr's selection
				// style, and it leaves reverse free to mean "cursor" on
				// the billboard canvas. Continuation lines share the fill
				// but not the bold.
				style := rowBG + pal.text
				if i == sel {
					style += "\033[1m"
				}
				if s := rows[i].styled; s != "" && len([]rune(rows[i].label)) <= lw {
					b.WriteString(style + s + pad + "\033[22;49;39m")
				} else {
					b.WriteString(style + string(label) + pad + "\033[22;49;39m")
				}
			case rows[i].gap:
				b.WriteString(rowBG + string(label) + pad + "\033[49m")
			case rows[i].head:
				b.WriteString(rowBG + pal.muted + "\033[1m" + string(label) + pad + "\033[22;49;39m")
			case rows[i].sess != "" && rows[i].sess == curSess:
				// The client's real session: herdr's active_row_bg fill
				// across the whole card, name in full text + bold.
				style := rowBG
				if rows[i].session {
					style += pal.text + "\033[1m"
				}
				if s := rows[i].styled; s != "" && len([]rune(rows[i].label)) <= lw {
					b.WriteString(style + s + pad + "\033[22;49;39m")
				} else {
					b.WriteString(style + string(label) + pad + "\033[22;49;39m")
				}
			case rows[i].session:
				b.WriteString(rowBG + pal.subtext + string(label) + pad + "\033[49;39m")
			case rows[i].arow && !rows[i].cont:
				// Agent entry's WHERE row. herdr's ladder (sidebar.rs:1509):
				// the workspace name carries the weight — text for the agent
				// you are actually in, subtext for the rest, bold either way
				// — and everything after it is chrome. The styled form is
				// what mutes the `· tab · agent` tail; before this it was
				// only ever reached on the selected row, so the tail read at
				// full strength everywhere else.
				name := pal.subtext
				if rows[i].pane != "" && rows[i].pane == curAgent {
					name = pal.text
				}
				if s := rows[i].styled; s != "" && len([]rune(rows[i].label)) <= lw {
					b.WriteString(rowBG + name + "\033[1m" + s + pad + "\033[22;49;39m")
				} else {
					b.WriteString(rowBG + name + "\033[1m" + string(label) + pad + "\033[22;49;39m")
				}
			default:
				if s := rows[i].styled; s != "" && len([]rune(rows[i].label)) <= lw {
					b.WriteString(rowBG + s + pad + "\033[49m")
				} else {
					b.WriteString(rowBG + pal.subtext + "\033[2m" + string(label) + pad + "\033[22;49;39m")
				}
			}
			if benchLog != nil {
				cls := byte(' ')
				if i == sel {
					cls = '>'
				} else if rows[i].session {
					cls = 'S'
				}
				cur = append(cur, string(cls)+string(label))
			}
		} else {
			b.WriteString(pal.bg + strings.Repeat(" ", lw) + "\033[49m")
			if benchLog != nil {
				cur = append(cur, "")
			}
		}
		if border {
			b.WriteString(pal.bg + pal.muted + "│\033[49;39m")
		}
		if i >= 0 && !rows[i].inert() {
			// Col-2 dot (herdr's ` ● name` inset): the entry's agent state;
			// an attached session with no agents shows the accent dot.
			// Painted AFTER the border write on purpose — this repositions
			// the cursor, and the border relies on it sitting at col lw+1.
			orn, style := rowMark(rows[i], spinTick)
			if orn != "" {
				fmt.Fprintf(&b, "\033[%d;2H%s%s\033[0m", y+1, rowBG+style, orn)
			}
		}
	}
	b.WriteString("\033[?2026l")
	// Skip a write that would change nothing. Handlers paint for themselves,
	// so one settled keystroke produced ~2.2 list paints and 38% of all list
	// paints rewrote the strip byte-for-byte identically — tmux diffed them
	// back to nothing, but the escapes were still built, written, and
	// parsed. Comparing the rendered strip is far cheaper than emitting it.
	out := b.String()
	if out == lastListOut {
		benchf("paint_list skipped bytes=%d", len(out))
		return
	}
	lastListOut = out
	os.Stdout.WriteString(out)
	if benchLog != nil {
		logged := 0
		for y := 0; y < len(cur) || y < len(benchListPrev); y++ {
			o, n := "", ""
			if y < len(benchListPrev) {
				o = benchListPrev[y]
			}
			if y < len(cur) {
				n = cur[y]
			}
			if o != n {
				if logged < 8 {
					benchf("list row %d: %q -> %q", y, o, n)
				}
				logged++
			}
		}
		if logged > 8 {
			benchf("list rows changed: %d total", logged)
		}
		benchListPrev = cur
	}
	benchf("paint_list dur_us=%d bytes=%d", time.Since(start).Microseconds(), b.Len())
}

// agentGlyph maps a window's worst agent state to its list marker —
// herdr's dot language: attention states share ● and differ by hue
// (red needs you, yellow is live, teal finished unseen); confirmed idle
// hollows out to a green ○.
// spinners: herdr's frames, verbatim. The ten trace the perimeter of the
// braille cell, which is what makes it read as a rectangle being drawn
// rather than dots blinking.
//
//	const SPINNERS: &[&str] = &["⠋","⠙","⠹","⠸","⠼","⠴","⠦","⠧","⠇","⠏"];
//
// herdr later deleted this (81f355fa, "replace agent spinners with static
// status marks") to stop rendering continuously — but herdr was repainting
// its whole UI at 60fps to drive it. winch repaints one 26-column strip at
// 8fps and only while an agent is actually working, which is a different
// bill entirely.
var spinners = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinPeriod is herdr's cadence: it ticked at 60fps and advanced a frame
// every 8th tick, so ~8 frames a second. Driven here by a timer of its own
// rather than by the agent's title, which the detector only samples every
// 300ms — subsampling someone else's animation gives you neither their
// smoothness nor your own.
const spinPeriod = 125 * time.Millisecond

// spinTick advances only while the ticker runs, which is only while some
// agent is working.
var spinTick int

func spinnerFrame(tick int) string { return spinners[tick%len(spinners)] }

// rowMark is the glyph at column 2 and its colour: the agent state's dot,
// spinning while a turn runs; the accent dot for an attached session with no
// agents; and herdr's Unknown "·" for a session with neither.
//
// Never empty for a session row, and that is what pays for row_gap 0. With
// no blank line between cards the glyph column is the only thing that says
// "new card", so a session with nothing to report still needs a mark — with
// none, its name read as the previous card's continuation.
func rowMark(r row, tick int) (string, string) {
	orn, style := agentGlyph(r.agent)
	if r.arow && r.agent == "working" {
		// Agent rows only. herdr animated its agent entries and left the
		// workspace marks static: a session card is an aggregate, not a turn.
		orn = spinnerFrame(tick)
	}
	switch {
	case orn != "":
		return orn, style
	case !r.session:
		return "", ""
	case r.att:
		return "●", pal.accent
	default:
		return "·", pal.muted
	}
}

func agentGlyph(state string) (string, string) {
	switch state {
	case "blocked":
		return "●", pal.red
	case "done":
		return "●", pal.teal
	case "background":
		// Its own glyph, not a dot: the turn is over and the agent takes
		// input, so reading it as one more coloured dot in the ladder would
		// invite exactly the wrong conclusion about whether to go there.
		return "⚙", pal.peach
	case "working":
		return "●", pal.yellow
	case "idle":
		return "○", pal.green
	}
	return "", ""
}

// sameRects reports whether two frames tile identically — the precondition
// for line-level diff painting, since every cell one frame writes the other
// writes too.
//
// Deliberately NOT including Active: which pane is active only decides the
// border color, and folding it in here forced a full repaint on the single
// most common scrub step. Billboarding the docked window yields Active=false
// for its remaining pane (the active one is the sidebar, filtered out of its
// own frame) while any other window yields true, so identical single-pane
// layouts differed by that flag alone and repainted ~44KB instead of a diff.
func sameRects(a, b []framePane) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Left != b[i].Left || a[i].Top != b[i].Top ||
			a[i].Width != b[i].Width || a[i].Height != b[i].Height {
			return false
		}
	}
	return true
}

// sameActive reports whether the same pane is active in both frames; when it
// is not, the borders must be recolored even if the diff covers the content.
func sameActive(a, b []framePane) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Active != b[i].Active {
			return false
		}
	}
	return true
}

// activeBorderStyle matches the real tmux pane-active-border color
// (catppuccin lavender). Theming milestone can query the live style later.
const activeBorderStyle = "\033[38;2;183;189;251m"

// paintBorders draws the gaps between panes as tmux-style borders: dim
// │ ─ ┼, with the active pane's surrounding border in the active color.
func paintBorders(b *strings.Builder, frame []framePane, cols, height, offX int) {
	W, H := 0, 0
	for _, p := range frame {
		if p.Left+p.Width > W {
			W = p.Left + p.Width
		}
		if p.Top+p.Height > H {
			H = p.Top + p.Height
		}
	}
	if W <= 0 || H <= 0 || len(frame) < 2 {
		return // single pane: no internal borders
	}
	const (
		vert  = 1
		horiz = 2
	)
	grid := make([]byte, W*H)
	mark := func(x, y int, kind byte) {
		if x >= 0 && x < W && y >= 0 && y < H {
			grid[y*W+x] |= kind
		}
	}
	for _, p := range frame {
		if x := p.Left + p.Width; x < W {
			for y := p.Top; y < p.Top+p.Height; y++ {
				mark(x, y, vert)
			}
			mark(x, p.Top+p.Height, vert|horiz)
		}
		if y := p.Top + p.Height; y < H {
			for x := p.Left; x < p.Left+p.Width; x++ {
				mark(x, y, horiz)
			}
		}
	}
	// Border cells adjacent to the active pane render in the active colour
	// (activeBorderStyle), like tmux's pane-active-border-style.
	activeAt := func(x, y int) bool {
		for _, p := range frame {
			if !p.Active {
				continue
			}
			hSpan := x >= p.Left-1 && x <= p.Left+p.Width
			vSpan := y >= p.Top-1 && y <= p.Top+p.Height
			onV := (x == p.Left-1 || x == p.Left+p.Width) && vSpan
			onH := (y == p.Top-1 || y == p.Top+p.Height) && hSpan
			return onV || onH
		}
		return false
	}
	for y := 0; y < H && y < height; y++ {
		for x := 0; x < W && offX+x < cols; x++ {
			kind := grid[y*W+x]
			if kind == 0 {
				continue
			}
			ch := "│"
			switch kind {
			case horiz:
				ch = "─"
			case vert | horiz:
				ch = "┼"
			}
			style := "\033[2m"
			if activeAt(x, y) {
				style = activeBorderStyle
			}
			fmt.Fprintf(b, "\033[%d;%dH%s%s\033[0m", y+1, offX+x+1, style, ch)
		}
	}
}

// paintFrame redraws the preview region. With prev (same window, same
// geometry) only changed lines are erased-and-rewritten — a streaming pane
// repaints a few lines per frame instead of blanking the region, which is
// what made busy claude panes flicker. Without prev (new window, resize)
// the full region is prefilled so stale content from differently-shaped
// windows cannot linger. No screen clear either way — tmux diffs cells
// server-side and ships only real changes to the terminal.
// cursorIdx picks the ONE pane whose cursor the billboard shows: the pane
// the selection would focus on commit (an agent row's pane), else the
// window's active pane. A selected pane that hides its cursor shows none —
// entering it wouldn't show one either.
func cursorIdx(frame []framePane, pane string) int {
	active := -1
	for i, p := range frame {
		if pane != "" && p.ID == pane {
			if p.Cursor {
				return i
			}
			return -1
		}
		if p.Active && p.Cursor && active == -1 {
			active = i
		}
	}
	return active
}

// geoSig is a compact rendering of a frame's pane rectangles: whether two
// frames share geometry decides whether a paint can diff, so when a paint is
// unexpectedly full this is the first thing to look at.
func geoSig(frame []framePane) string {
	var b strings.Builder
	for i, p := range frame {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%d+%dx%dx%d/%d", p.Left, p.Top, p.Width, p.Height, len(p.Lines))
		if p.Active {
			b.WriteByte('*')
		}
	}
	return b.String()
}

// paneRows is how many rows of a pane the paint loop actually writes: past
// its captured lines the rectangle is not covered, so those cells still need
// erasing on a layout change.
func paneRows(p framePane) int {
	if n := len(p.Lines); n < p.Height {
		return n
	}
	return p.Height
}

// uncovered returns the column spans of region row y that no pane will
// write — the only cells a full repaint has to blank.
func uncovered(frame []framePane, y, avail int) [][2]int {
	var cov [][2]int
	for _, p := range frame {
		if y < p.Top || y >= p.Top+paneRows(p) {
			continue
		}
		l, r := max(p.Left, 0), min(p.Left+p.Width, avail)
		if r > l {
			cov = append(cov, [2]int{l, r})
		}
	}
	sort.Slice(cov, func(i, j int) bool { return cov[i][0] < cov[j][0] })
	var out [][2]int
	x := 0
	for _, c := range cov {
		if c[0] > x {
			out = append(out, [2]int{x, c[0]})
		}
		if c[1] > x {
			x = c[1]
		}
	}
	if x < avail {
		out = append(out, [2]int{x, avail})
	}
	return out
}

func paintFrame(frame, prev []framePane, curPane, prevCurPane string, borders bool) {
	start := time.Now()
	ci := cursorIdx(frame, curPane)
	pci := -1
	if prev != nil {
		pci = cursorIdx(prev, prevCurPane)
	}
	cols, height := surfaceSize()
	offX := listW + 1 // frame region starts right of the border
	avail := cols - offX
	if avail <= 0 {
		return
	}
	var b strings.Builder
	b.WriteString("\033[?2026h\033[0m")
	changed := 0
	if prev == nil {
		// Erase only the cells the incoming layout will NOT write. Panes
		// tile the region, so that is usually just the border columns.
		// Blanking the whole region first cost 43.8KB of spaces at 480x96
		// — 57% of a measured full paint — and every byte of it was
		// overwritten by pane content microseconds later.
		wide := strings.Repeat(" ", avail)
		for y := 0; y < height; y++ {
			for _, u := range uncovered(frame, y, avail) {
				fmt.Fprintf(&b, "\033[%d;%dH%s", y+1, offX+u[0]+1, wide[:u[1]-u[0]])
			}
		}
	}
	for pi, p := range frame {
		if offX+p.Left >= cols || p.Top >= height {
			continue
		}
		width := p.Width
		if offX+p.Left+width > cols {
			width = cols - offX - p.Left
		}
		blank := strings.Repeat(" ", width)
		lines := len(p.Lines)
		if prev != nil && len(prev[pi].Lines) > lines {
			lines = len(prev[pi].Lines) // erase rows the new frame lost
		}
		// A cursor move alone changes no content rows, but the OLD inverse
		// cell must be painted away and the new one painted in: force both
		// rows through the diff. Ownership counts as movement — switching
		// rows can hand the cursor to a different pane in the same frame.
		showNew, showOld := pi == ci, prev != nil && pi == pci
		forceOld, forceNew := -1, -1
		if showOld != showNew ||
			(showOld && (prev[pi].CursorX != p.CursorX || prev[pi].CursorY != p.CursorY)) {
			if showOld {
				forceOld = prev[pi].CursorY
			}
			if showNew {
				forceNew = p.CursorY
			}
		}
		for i := 0; i < lines; i++ {
			if i >= p.Height || p.Top+i >= height {
				break
			}
			ln := ""
			if i < len(p.Lines) {
				ln = p.Lines[i]
			}
			if prev != nil && i < len(prev[pi].Lines) && prev[pi].Lines[i] == ln &&
				i != forceOld && i != forceNew {
				continue
			}
			changed++
			// One write: content, then spaces out to the pane's cell edge
			// BEFORE the SGR reset. TUIs paint full-width bars via BCE
			// (set bg + \033[K) and capture-pane drops the fill entirely,
			// leaving the line ending with the bar's bg still open
			// (probe-verified) — padding in that live state reconstructs
			// the bar; lines ending in default state pad blank as before.
			// Reset per line so pane edges never bleed attributes.
			ln, dw := cleanLine(ln, width)
			pad := width - dw
			if pad < 0 {
				pad = 0
			}
			fmt.Fprintf(&b, "\033[%d;%dH%s%s\033[0m",
				p.Top+1+i, offX+p.Left+1, ln, blank[:pad])
		}
		// The pane's cursor, inverse over whatever the frame shows there —
		// a billboard without one reads as a screenshot at a glance.
		if showNew && p.CursorY < p.Height && p.Top+p.CursorY < height && p.CursorX < width {
			ch := ' '
			if p.CursorY < len(p.Lines) {
				ch = charAtCol(p.Lines[p.CursorY], p.CursorX)
			}
			benchf("cursor pane=%s cell=%d,%d ch=%q", p.ID, p.CursorX, p.CursorY, ch)
			fmt.Fprintf(&b, "\033[%d;%dH\033[7m%c\033[0m",
				p.Top+1+p.CursorY, offX+p.Left+1+p.CursorX, ch)
		}
	}
	if borders {
		// Borders last: scaled-frame rounding can collapse a gap onto a
		// pane's first column, and the border should win that cell.
		paintBorders(&b, frame, cols, height, offX)
	}
	if changed == 0 && !borders && ci < 0 && pci < 0 {
		// Nothing to say: a stream tick whose rows all matched. Emitting a
		// bare synchronized-output pair still costs a write and a parse.
		benchf("paint_frame skipped panes=%d", len(frame))
		return
	}
	b.WriteString("\033[?2026l")
	os.Stdout.WriteString(b.String())
	benchf("paint_frame dur_us=%d bytes=%d panes=%d diff=%v changed_lines=%d geo=%s",
		time.Since(start).Microseconds(), b.Len(), len(frame), prev != nil, changed, geoSig(frame))
}
