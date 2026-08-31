package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
)

// Winch overwrites options that belong to the user — the status format it
// shifts past the sidebar, the automatic rename it freezes, the border
// indicator it turns off — and every one of them has to go back exactly as it
// was, including after a crash, when the daemon's memory of "as it was" is
// gone.
//
// Doing that per option, at whichever call site needed it, is what this file
// replaces. That shape produced the same bug four separate times: a status
// format re-installed onto a session the dock had already left; a pad stranded
// by an upgrade, because the sweep that would have found it keyed on a flag the
// first restart cleared; a cache that had to be dropped by hand on two
// different paths; a restore emitted before the write it was undoing. Each fix
// was correct and none of them generalised, because there was nothing to
// generalise over.
//
// The rule here is that winch never writes an owned option directly. It
// declares what it WANTS to own — desiredOpts, a pure function of an intent
// struct — and asks the owner for the difference. Wanted but not owned yields a
// claim and an install; owned but no longer wanted yields a restore of the
// value saved at claim time. A session the dock leaves stops being wanted, so
// its restore is emitted by construction rather than by someone remembering to.
//
// plan returns the two halves SEPARATELY because sequencing is load-bearing:
// everything an arriving window needs must ride one control sequence before
// switch-client, and the restores for the window being left must not (dock.go).
// Callers splice each half into the batch it belongs in.
//
// Recovery is exact rather than heuristic because the saved value is persisted
// into tmux itself, in a @winch_saved_<option> user option on the same object
// (markCmd). The old content-keyed sweeps could recognise a status format
// nothing else would produce, but never `automatic-rename off` — a value the
// user might well have set themselves — so a window frozen by a dead daemon
// simply never thawed.

type optScope byte

const (
	scopeSession optScope = iota
	scopeWindow
	scopePane
)

func (s optScope) flag() string {
	switch s {
	case scopeWindow:
		return "-w"
	case scopePane:
		return "-p"
	}
	return ""
}

func (s optScope) String() string {
	switch s {
	case scopeWindow:
		return "window"
	case scopePane:
		return "pane"
	}
	return "session"
}

// optKey identifies one option on one tmux object.
type optKey struct {
	scope  optScope
	target string // session / window / pane id
	name   string
}

func (k optKey) String() string { return k.scope.String() + " " + k.target + " " + k.name }

// optOwn is one claimed option: what it held before winch touched it, and the
// argument tails winch last installed over it.
//
// saved holds RAW `show-options` lines, which replay verbatim. Empty means the
// option was unset AT THAT SCOPE — which is not the same as having no value,
// since tmux inherits from the global, and writing the effective value back
// would pin the object to whatever the global happened to be at claim time.
type optOwn struct {
	saved     []string
	installed []string
}

// owner is the registry. basis is state captured alongside a claim that the
// desired value is built FROM rather than replaced — see statusRows.
type owner struct {
	own   map[optKey]*optOwn
	basis map[optKey][]string
}

// optWant is a desired install: argument tails, in the same shape
// `show-options` prints them, so saved and desired are the same kind of thing.
type optWant struct {
	key  optKey
	args []string
}

// optReader reads several options in one go. Batched because a claim happens
// immediately before a latency-critical batch, and a round trip per option
// would put several stalls in front of it.
type optReader func(keys []optKey) [][]string

func newOwner() *owner {
	return &owner{own: map[optKey]*optOwn{}, basis: map[optKey][]string{}}
}

// plan diffs what winch owns against what it wants to own, and hands back the
// bookkeeping as a closure instead of doing it.
//
// commit exists because the commands come back to be spliced into somebody
// else's batch, and that batch can fail. Recording the change first and finding
// out afterwards is how a session ends up believed-released with its pad still
// installed — nothing will restore it, because as far as the registry is
// concerned it already did. Callers commit once the batch has landed; a caller
// that returns early simply never does, and the next plan recomputes from
// unchanged state.
//
// read is consulted only for keys being claimed for the first time, so a caller
// that is only giving things up never needs one.
func (o *owner) plan(read optReader, want []optWant) (install, restore []string, commit func()) {
	wanted := make(map[optKey]bool, len(want))
	var fresh []optKey
	for _, wa := range want {
		if wanted[wa.key] {
			continue
		}
		wanted[wa.key] = true
		if _, held := o.own[wa.key]; !held {
			fresh = append(fresh, wa.key)
		}
	}

	claimed := make(map[optKey]*optOwn, len(fresh))
	// Marks first, values second, always. A batch that dies between the two
	// leaves a mark whose saved value is still what the option holds, so the
	// sweep that finds it restores a no-op. The other order would leave winch's
	// write with nothing recording what it displaced.
	if len(fresh) > 0 {
		var saved [][]string
		if read != nil {
			saved = read(fresh)
		}
		for i, k := range fresh {
			var s []string
			if i < len(saved) {
				s = saved[i]
			}
			claimed[k] = &optOwn{saved: s}
			install = append(install, markCmd(k, s))
		}
	}

	type update struct {
		e    *optOwn
		args []string
	}
	var updates []update
	done := make(map[optKey]bool, len(want))
	for _, wa := range want {
		if done[wa.key] || len(wa.args) == 0 {
			continue
		}
		done[wa.key] = true
		e := claimed[wa.key]
		if e == nil {
			e = o.own[wa.key]
		}
		if e == nil || sameArgs(e.installed, wa.args) {
			continue
		}
		updates = append(updates, update{e: e, args: wa.args})
		install = append(install, setCmds(wa.key, wa.args)...)
	}

	var stale []optKey
	for k := range o.own {
		if !wanted[k] {
			stale = append(stale, k)
		}
	}
	sortKeys(stale)
	for _, k := range stale {
		restore = append(restore, restoreCmds(k, o.own[k].saved)...)
	}

	commit = func() {
		for k, e := range claimed {
			o.own[k] = e
		}
		for _, u := range updates {
			u.e.installed = u.args
		}
		for _, k := range stale {
			delete(o.own, k)
			delete(o.basis, k)
		}
	}
	return install, restore, commit
}

// leadWithRestores puts option restores at the HEAD of a batch, ahead of
// everything that can fail.
//
// tmux aborts a command sequence at the first error. Everything restoreCmds
// emits is infallible — `set-option -uq`, then replays of values tmux itself
// printed — but the commands they used to sit behind fail routinely:
// select-pane on a pane the user has since closed, kill-pane on a sidebar that
// already died. Behind one of those, every restore is dropped silently, and the
// registry has already recorded them as given back, so nothing tries again.
//
// That is not hypothetical. A live daemon logged
//
//	scrub restore @47: tmux: can't find pane: %2072
//
// and left a session rendering a DIFFERENT session's window list for ten
// minutes — closing panes in it changed nothing, because its bar had stopped
// describing it. rigs/restoreorder_test.go holds both shapes.
func leadWithRestores(restore []string, then ...string) []string {
	return append(append([]string(nil), restore...), then...)
}

// releaseAll gives back everything winch owns, and commits immediately: undock
// and the sidebar-pane-died cleanup are the same question asked twice, and
// neither has anywhere to roll back to.
func (o *owner) releaseAll() []string {
	_, restore, commit := o.plan(nil, nil)
	commit()
	o.basis = map[optKey][]string{}
	return restore
}

// forget drops every claim on an object WITHOUT restoring it, for an object
// that is about to stop existing.
//
// Restoring an option on a window that has gone is an error, and tmux aborts a
// sequence at the first error — so a dead target left in a restore list takes
// the live ones down with it. There is nothing to put back either: the options
// die with the object.
func (o *owner) forget(scope optScope, target string) {
	for k := range o.own {
		if k.scope == scope && k.target == target {
			delete(o.own, k)
			delete(o.basis, k)
		}
	}
}

// owns reports whether a key is currently claimed. Diagnostics and tests only —
// nothing in the daemon should be branching on this.
func (o *owner) owns(k optKey) bool {
	_, ok := o.own[k]
	return ok
}

func sameArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortKeys(ks []optKey) {
	sort.Slice(ks, func(i, j int) bool {
		if ks[i].scope != ks[j].scope {
			return ks[i].scope < ks[j].scope
		}
		if ks[i].target != ks[j].target {
			return ks[i].target < ks[j].target
		}
		return ks[i].name < ks[j].name
	})
}

func setOpt(sc optScope, target, tail string) string {
	if f := sc.flag(); f != "" {
		return "set-option " + f + " -t " + q(target) + " " + tail
	}
	return "set-option -t " + q(target) + " " + tail
}

func unsetOpt(sc optScope, target, name string) string {
	if f := sc.flag(); f != "" {
		return "set-option " + f + " -uq -t " + q(target) + " " + name
	}
	return "set-option -uq -t " + q(target) + " " + name
}

func setCmds(k optKey, args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		out = append(out, setOpt(k.scope, k.target, a))
	}
	return out
}

// restoreCmds clears everything winch wrote and replays whatever the object had
// of its own. The unset comes first and names the option WITHOUT an index, so
// an array option loses every row winch added, not just the ones it overwrote.
// An unset falls back to the global, which is the untouched truth.
func restoreCmds(k optKey, saved []string) []string {
	out := []string{
		unsetOpt(k.scope, k.target, k.name),
		unsetOpt(k.scope, k.target, markName(k.name)),
	}
	for _, ln := range saved {
		out = append(out, setOpt(k.scope, k.target, ln))
	}
	return out
}

// markCmd persists a claim into tmux, so a daemon that dies still leaves behind
// everything its successor needs to undo it.
func markCmd(k optKey, saved []string) string {
	if saved == nil {
		saved = []string{}
	}
	b, err := json.Marshal(saved)
	if err != nil {
		b = []byte("[]")
	}
	return setOpt(k.scope, k.target, markName(k.name)+" "+q(string(b)))
}

// markName is the user option a claim on `opt` is recorded in. Non-alphanumeric
// characters become underscores: tmux user options may not contain a hyphen,
// and every option winch owns has one.
func markName(opt string) string {
	var b strings.Builder
	b.WriteString("@winch_saved_")
	for _, r := range opt {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ownedOptions is every option winch may take from the user. Closed on purpose:
// a sweep reads the marks back through list-sessions / list-windows FORMATS —
// two round trips for the whole server rather than one per object — and that
// needs the names up front.
//
// The @winch_ ones are winch's own and never hold anything worth saving, but
// they are listed anyway so that one sweep clears everything rather than one
// sweep per kind of leftover.
var ownedOptions = []optKey{
	{scope: scopeSession, name: "status-format"},
	// message-style and message-command-style are no longer CLAIMED — winch
	// wants nothing from them (desiredOpts). They stay on this list so the
	// startup sweep still finds and restores a claim written by an older
	// daemon: a running tmux out there is wearing `width=`/`align=right`/
	// `fill=` on a session right now, and dropping the names would strand it
	// with no way back short of setting the option by hand.
	//
	// Winch confined the prompt to the right of the sidebar so prefix-: would
	// stop painting over the strip and the seam. It worked, and it cost more
	// than it was worth. `align` does double duty — status_prompt_area reads it
	// to place the AREA, and message-format embeds the same style so format_draw
	// reads it again to place the TEXT — so forcing align=right to push the area
	// past the sidebar also shoved the user's prompt to the far edge. Underneath
	// that: the paint and the area come from two DIFFERENT options, editing
	// either means splitting on commas that a conditional style does not use as
	// separators, and set-option validates styles so a mangled one aborts the
	// dock batch and the sidebar fails to open.
	//
	// The prompt now paints over the sidebar for as long as it is open, which is
	// what tmux does to every other pane's status. That is a visible cost for a
	// few seconds against a permanent class of bug.
	{scope: scopeSession, name: "message-style"},
	{scope: scopeSession, name: "message-command-style"},
	{scope: scopeSession, name: "@winch_docked"},
	{scope: scopeSession, name: "@winch_win"},
	{scope: scopeWindow, name: "automatic-rename"},
	{scope: scopeWindow, name: "pane-border-indicators"},
}

// ------------------------------------------------------------------
// What winch wants to own
// ------------------------------------------------------------------

// optIntent is everything the owned set is a function of. Pure data, so
// desiredOpts is a pure function and the whole policy is table-testable without
// a tmux server — which is most of what the dock rigs used to prove one
// spawned server at a time.
type optIntent struct {
	sess  string   // session hosting the sidebar; "" = not docked
	win   string   // window hosting the sidebar
	held  []string // every window winch holds geometry in, including win
	width int      // sidebar width in columns

	// rows is the session's OWN status-format, one effective string per
	// rendered row — what the pad wraps. Not the same as the saved lines the
	// restore replays: a session inheriting the global reports nothing of its
	// own, but the pad still has to wrap the global's text.
	rows []string

	// scrubbing is winch holding the sidebar ZOOMED for billboards. It changes
	// what draws the sidebar.s edge, and so what the pad.s last column has to
	// match — see padCell.
	scrubbing bool

	// scrubWin / scrubSess point row 0 at a billboard target instead of at the
	// session's own format. Empty means row 0 says what it normally says.
	scrubWin  string
	scrubSess string
}

// desiredOpts is the whole policy: given the intent, every option winch wants
// to hold and what it wants it to say.
func desiredOpts(in optIntent) []optWant {
	if in.sess == "" {
		return nil
	}
	want := []optWant{
		{optKey{scopeSession, in.sess, "@winch_docked"}, []string{"@winch_docked 1"}},
		{optKey{scopeSession, in.sess, "@winch_win"}, []string{"@winch_win " + q(in.win)}},
	}

	var rows []string
	for i, base := range in.rows {
		body := base
		if i == 0 && in.scrubWin != "" {
			// The scrub override replaces what the row SAYS, not where it
			// starts, so it is padded like any other row.
			body = scrubStatusFormat(in.scrubSess, in.scrubWin)
		}
		if body == "" {
			continue
		}
		rows = append(rows, fmt.Sprintf("status-format[%d] %s", i, q(padPrefix(in.width, i, in.scrubbing)+body)))
	}
	if len(rows) > 0 {
		want = append(want, optWant{optKey{scopeSession, in.sess, "status-format"}, rows})
	}

	// The command prompt is deliberately NOT touched. See the note on
	// ownedOptions: winch used to confine it past the sidebar, and giving that
	// up is what stopped this file editing the user's styles at all.

	// Window options apply to every window winch holds, not just the one the
	// sidebar is in: a spacer-held window is still wearing the dock's geometry
	// and still has the sidebar's border in it.
	held := append([]string(nil), in.held...)
	sort.Strings(held)
	for i, wid := range held {
		if wid == "" || (i > 0 && wid == held[i-1]) {
			continue
		}
		want = append(want,
			// Frozen because the sidebar takes focus when it lands, and an
			// automatic-rename window would flip its name to the sidebar's.
			optWant{optKey{scopeWindow, wid, "automatic-rename"}, []string{"automatic-rename off"}},
			// Off because tmux colours only HALF a divider in a window with
			// exactly two panes, flipping which half as focus moves — and
			// docked, sidebar-plus-one-content IS two panes. Confirmed in
			// tmux's screen_redraw_pane_border(): with hsplit set, the active
			// pane owns its divider only for py <= wp->sy/2 when it is the left
			// pane, py > wp->sy/2 when it is the right one.
			optWant{optKey{scopeWindow, wid, "pane-border-indicators"}, []string{"pane-border-indicators off"}})
	}
	return want
}

// ------------------------------------------------------------------
// Recovery
// ------------------------------------------------------------------

// sweepOwned puts back everything a previous daemon claimed and never gave up,
// having died or been killed while docked. Its memory of the original values
// went with it; the marks it wrote did not.
//
// Two round trips for the whole server, not one per object: every mark is read
// as a FORMAT VARIABLE off list-sessions / list-windows, which is why
// ownedOptions has to be a closed list. A mark holding "[]" is still a mark —
// it says the option was unset when winch claimed it, and unset is what it goes
// back to.
//
// Runs before the daemon serves anything, because statusRows reads a session's
// status format on the assumption that nobody has wrapped it.
func (d *daemon) sweepOwned(ctl *control) {
	d.sweepScope(ctl, scopeSession, "list-sessions", "#{session_id}")
	d.sweepScope(ctl, scopeWindow, "list-windows -a", "#{window_id}")
}

func (d *daemon) sweepScope(ctl *control, sc optScope, list, idVar string) {
	var keys []optKey
	fields := []string{idVar}
	for _, k := range ownedOptions {
		if k.scope != sc {
			continue
		}
		keys = append(keys, k)
		fields = append(fields, "#{"+markName(k.name)+"}")
	}
	if len(keys) == 0 {
		return
	}
	rows, err := ctl.run(list + " -F " + f(fields...))
	if err != nil {
		return
	}
	for _, row := range rows {
		p := strings.Split(row, sep)
		if len(p) != len(keys)+1 {
			continue
		}
		var cmds []string
		for i, k := range keys {
			mark := p[i+1]
			if mark == "" {
				continue
			}
			k.target = p[0]
			if d.opts.owns(k) {
				continue // a live claim of this daemon's own, not a leftover
			}
			var saved []string
			if err := json.Unmarshal([]byte(mark), &saved); err != nil {
				// Unreadable mark: the option still has to come off, and an
				// unset is the honest fallback — it falls through to the
				// global, which winch never touches.
				log.Printf("sweep %s: unreadable mark %q, unsetting", k, mark)
				saved = nil
			}
			cmds = append(cmds, restoreCmds(k, saved)...)
			log.Printf("swept leaked %s", k)
		}
		if len(cmds) > 0 {
			if _, err := ctl.runSeq(cmds...); err != nil {
				log.Printf("sweep %s: %v", p[0], err)
			}
		}
	}
}

// ------------------------------------------------------------------
// Reading
// ------------------------------------------------------------------

// readOpts is the production optReader. runPipelined rather than runSeq: an
// array option answers with several lines, and runSeq flattens every command's
// reply into one list with no way to tell where each began.
func readOpts(ctl *control) optReader {
	return func(keys []optKey) [][]string {
		if len(keys) == 0 {
			return nil
		}
		cmds := make([][]string, len(keys))
		for i, k := range keys {
			cmds[i] = []string{showOpt(k)}
		}
		outs, errs := ctl.runPipelined(cmds...)
		for i := range outs {
			if errs[i] != nil {
				outs[i] = nil
			}
		}
		return outs
	}
}

func showOpt(k optKey) string {
	if f := k.scope.flag(); f != "" {
		return "show-options -q " + f + " -t " + q(k.target) + " " + k.name
	}
	return "show-options -q -t " + q(k.target) + " " + k.name
}

// statusRows reports the EFFECTIVE text of a session's rendered status rows —
// what the pad wraps. Cached against the status-format claim and dropped the
// moment that claim is released, which is the invalidation the old standalone
// cache had to be given by hand on two separate paths (and was, wrongly, on a
// third).
//
// Must be called BEFORE the pad is installed, or it captures winch's own wrap
// as the base. Two things make that safe: the cache means a session is only
// ever read while unwrapped, and sweepOwned unwraps anything a dead daemon left
// behind before the daemon serves its first request.
func (o *owner) statusRows(ctl *control, sid string) []string {
	k := optKey{scopeSession, sid, "status-format"}
	if b, ok := o.basis[k]; ok {
		return b
	}
	b := effectiveStatusRows(ctl, sid)
	o.basis[k] = b
	return b
}

// effectiveStatusRows reads how many rows tmux renders for a session and the
// text of each, as the session actually sees it.
//
// Every read is doubled — session scope then global — because `show-options -v`
// does NOT inherit: a session with nothing of its own answers empty rather than
// with the global value, and the global is where a tmux.conf setting lives.
// Both halves go out in one pipelined write, so the whole thing is two round
// trips whatever the row count.
func effectiveStatusRows(ctl *control, sid string) []string {
	n := statusRowCount(effPair(ctl, sid, "status"))
	if n == 0 {
		return nil
	}
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("status-format[%d]", i)
	}
	return effPairs(ctl, sid, names)
}

// statusRowCount is how many status rows tmux renders: `on` is one, `off` is
// none, otherwise the number. Only rendered rows are worth wrapping —
// status-format ships three entries whatever `status` says.
func statusRowCount(v string) int {
	switch v = strings.TrimSpace(v); v {
	case "off", "0":
		return 0
	case "", "on", "1":
		return 1
	default:
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5 {
			return n
		}
		return 1
	}
}

func effPair(ctl *control, sid, name string) string {
	got := effPairs(ctl, sid, []string{name})
	if len(got) == 1 {
		return got[0]
	}
	return ""
}

func effPairs(ctl *control, sid string, names []string) []string {
	cmds := make([][]string, 0, len(names)*2)
	for _, n := range names {
		cmds = append(cmds,
			[]string{"show-options -qv -t " + q(sid) + " " + n},
			[]string{"show-options -gqv " + n})
	}
	outs, errs := ctl.runPipelined(cmds...)
	out := make([]string, len(names))
	for i := range names {
		for _, j := range []int{i * 2, i*2 + 1} {
			if j < len(outs) && errs[j] == nil && len(outs[j]) == 1 && outs[j][0] != "" {
				out[i] = outs[j][0]
				break
			}
		}
	}
	return out
}
