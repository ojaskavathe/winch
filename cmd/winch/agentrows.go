package main

import "strings"

// Configurable agent-card rows, ported in spirit from herdr's
// AgentsSidebarConfig (config/sidebar.rs): the card is a list of ROWS, each
// row a list of TOKENS, and the user owns both.
//
// Two deliberate differences from herdr:
//
//   - No state_icon token. herdr composes the glyph into a row; winch paints
//     it in the mark column, which is also where session and window rows put
//     theirs, and that column is what makes a card legible as one thing. It
//     is not the row's to place.
//   - The redundancy strip is a property of the DEFAULT layout, not of the
//     token (herdr's default_agent_rows_remove_redundant_state_text). Ask for
//     state_text explicitly and you always get it; leave the default alone
//     and the word appears only where the glyph cannot explain itself.
//
// Syntax: rows separated by "|", tokens by whitespace.
//
//	set -g @winch-agent-rows "state_text workspace tab agent | title"
const optAgentRows = "@winch-agent-rows"

const (
	tokState     = "state_text" // blocked | background | working | done | idle
	tokWorkspace = "workspace"  // session name
	tokTab       = "tab"        // window label
	tokAgent     = "agent"      // manifest id: claude, codex, ...
	tokTitle     = "title"      // the agent's own name for this conversation
	tokReason    = "reason"     // blocked/background label ("shell still running")
)

func knownAgentToken(t string) bool {
	switch t {
	case tokState, tokWorkspace, tokTab, tokAgent, tokTitle, tokReason:
		return true
	}
	return false
}

// agentRows is a parsed layout plus whether it came from the default. The
// flag is not decoration: it is what decides if state_text self-suppresses.
type agentRows struct {
	rows     [][]string
	explicit bool // the user asked for this; do not second-guess it
}

// defaultAgentRows is today's card: context on the head row, the
// conversation name under it. state_text rides along but stays quiet unless
// the state is one the colour ladder cannot carry.
func defaultAgentRows() agentRows {
	return agentRows{rows: [][]string{
		{tokState, tokWorkspace, tokTab, tokAgent},
		{tokTitle},
	}}
}

// parseAgentRows reads the option. Anything unparseable returns the default
// and reports why, rather than rendering a card with holes in it — a sidebar
// that silently drops the conversation name because of one typo is worse
// than one that ignores the setting and says so.
func parseAgentRows(s string) (agentRows, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultAgentRows(), ""
	}
	var rows [][]string
	for _, part := range strings.Split(s, "|") {
		var toks []string
		for _, t := range strings.Fields(part) {
			t = strings.ToLower(t)
			if !knownAgentToken(t) {
				return defaultAgentRows(), "unknown token " + t
			}
			toks = append(toks, t)
		}
		if len(toks) > 0 {
			rows = append(rows, toks)
		}
	}
	if len(rows) == 0 {
		return defaultAgentRows(), "no tokens"
	}
	return agentRows{rows: rows, explicit: true}, ""
}

// rowVal is one rendered token: its text and the colour it wears. Styles
// are per TOKEN, not per position, so inserting state_text at the front does
// not silently demote the workspace to muted the way a positional rule would.
type rowVal struct {
	text  string
	style string
}

// values renders one row spec against a pane. Tokens with nothing to say
// produce nothing — an agent with no detected kind should not leave a
// dangling " · " where its name would go.
func (r agentRows) values(spec []string, st *store, p pane) []rowVal {
	var out []rowVal
	add := func(text, style string) {
		if text != "" {
			out = append(out, rowVal{text, style})
		}
	}
	for _, t := range spec {
		switch t {
		case tokState:
			s := r.visibleState(p.AgentState)
			add(s, stateColour(p.AgentState))
		case tokWorkspace:
			// "" means AMBIENT, and it is load-bearing. paintList writes
			// the row's colour before the styled string — pal.text for the
			// agent you are in, subtext for the rest, and the selection
			// fill over the top — so the workspace name colouring itself
			// would flatten all three distinctions into one. Leading
			// tokens inherit; fitAgentRow substitutes for later ones,
			// which have a colour ahead of them to undo.
			add(st.sessions[p.SessionID].Name, "")
		case tokTab:
			add(st.tabLabel(p.SessionID, p.WindowID), pal.muted)
		case tokAgent:
			add(p.Agent, pal.muted)
		case tokReason:
			add(p.AgentReason, pal.subtext)
		case tokTitle:
			// A blocked pane's title is the STALE pre-prompt task, so the
			// reason ("permission prompt") speaks instead — a winch
			// addition worth keeping, since the stale title actively
			// misleads about why it stopped. background does NOT do this:
			// its turn ended normally, so the name is still true, and the
			// glyph plus state word already carry the rest.
			text := agentTaskTitle(p.Title)
			if p.AgentState == "blocked" && p.AgentReason != "" {
				text = p.AgentReason
			}
			add(text, pal.subtext)
		}
	}
	return out
}

// fitAgentRow lays one row out inside avail columns, and the two rows
// degrade differently on purpose.
//
// A head row DROPS trailing tokens: everything after the workspace is
// context the row can live without, and a half-written agent name reads as
// breakage. An identity row TRUNCATES on whole tokens (herdr's solver),
// because it has to degrade rather than vanish — the billboard still holds
// the full text either way.
func fitAgentRow(vals []rowVal, avail int, head bool, ambient string) (string, string) {
	width := func(v []rowVal) int {
		n := 0
		for i, x := range v {
			if i > 0 {
				n += 3 // " · "
			}
			n += len([]rune(x.text))
		}
		return n
	}
	// rowWidth is the rendered width of a token list joined by " · ".
	rowWidth := func(toks []string) int {
		n := 0
		for i, t := range toks {
			if i > 0 {
				n += 3 // " · "
			}
			n += len([]rune(t))
		}
		return n
	}
	if head {
		for len(vals) > 1 && width(vals) > avail {
			vals = vals[:len(vals)-1]
		}
	}
	var plain, styled []string
	for _, v := range vals {
		plain = append(plain, v.text)
	}
	if !head {
		// The identity row degrades by dropping whole trailing tokens, then
		// word-truncates whichever token is left last. It must NOT round-trip
		// through a strings.Split(text, " · "): a single token can itself
		// contain " · " — claude's resume-picker sets its terminal title to
		// literally "claude · resume" — and splitting on the separator sliced
		// that title in two, then the len(vals) clamp dropped the tail,
		// leaving a bare "claude" with the rest of the row unpainted (stale
		// cells bled through). Fit on the token slice directly instead.
		for len(plain) > 1 && rowWidth(plain) > avail {
			plain = plain[:len(plain)-1]
		}
		if last := len(plain) - 1; last >= 0 {
			used := rowWidth(plain[:last])
			if last > 0 {
				used += 3 // the separator before the last token
			}
			plain[last] = fitTokens(plain[last], avail-used)
		}
	}
	text := strings.Join(plain, " · ")
	for i, s := range plain {
		st := pal.subtext
		if i < len(vals) {
			st = vals[i].style
		}
		// An ambient token at the front inherits the painter's colour and
		// so emits nothing; anywhere else something has already set a
		// colour that has to be undone, and only then does it cost a code.
		if st == "" {
			if i == 0 {
				st = ""
			} else {
				st = ambient
			}
		}
		styled = append(styled, st+s)
	}
	sep := pal.muted + " · "
	return "   " + text, "   " + strings.Join(styled, sep) + "\033[39m"
}

// stateWord is the card's vocabulary, and it is herdr's (ui/status.rs:227)
// plus winch's own background. Kept lowercase for the same reason herdr does:
// the row is a sentence fragment, not a heading.
func stateWord(state string) string {
	switch state {
	case "blocked", "background", "working", "done", "idle":
		return state
	}
	return ""
}

// stateColour matches herdr's state_label_color (ui/status.rs:237) exactly
// for the four states they have, so a herdr user reads our sidebar without
// relearning anything. background is ours, and takes the glyph's peach.
func stateColour(state string) string {
	switch state {
	case "blocked":
		return pal.red
	case "working":
		return pal.yellow
	case "done":
		return pal.teal
	case "idle":
		return pal.green
	case "background":
		return pal.peach
	}
	return ""
}

// visibleState decides whether the state word earns its width on this row.
// Under the default layout it does not when the glyph already says it:
// working, done and idle are three points on a colour ladder people read at
// a glance, while blocked and background are the two that need a word —
// background because it is winch-only and no glyph convention exists for it,
// blocked because "why did it stop" is the whole question.
func (r agentRows) visibleState(state string) string {
	w := stateWord(state)
	if w == "" {
		return ""
	}
	if r.explicit {
		return w
	}
	switch state {
	case "blocked", "background":
		return w
	}
	return ""
}
