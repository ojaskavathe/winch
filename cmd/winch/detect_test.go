package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixtures mirror REAL claude chrome captured live 2026-08-21 (plain
// capture-pane, no -e): the input box is bounded by ─ runs, the working
// status line is "✻ Churning… (7m 22s · ↓ 24.7k tokens)".

var screenIdle = []string{
	"  explicit next step?",
	"✻ Crunched for 2m 13s",
	"────────────────────────────────",
	"❯ do the spike first, add it to the doc",
	"────────────────────────────────",
	"  Opus 4.8 · demo-copilot-v45",
}

var screenWorking = []string{
	"✻ Churning… (7m 22s · ↓ 24.7k tokens)",
	"────────────────────────────────",
	"❯ ",
	"────────────────────────────────",
	"  Opus 4.8 · demo-copilot-v45",
}

var screenBlocked = []string{
	"  Bash command",
	"  rm -rf ./build",
	"  Do you want to proceed?",
	"❯ 1. Yes",
	"  2. No, and tell Claude what to do differently (esc)",
}

var screenViewer = []string{
	"  some transcript content",
	"  Showing detailed transcript",
	"  ctrl+o to toggle · ↑↓ scroll",
}

// The turn ended but a background shell lives: working, and steady even
// when the footer chip truncates (the still-running line carries it).
var screenBgShell = []string{
	"✻ Crunched for 3m 10s · 1 shell still running",
	"────────────────────────────────",
	"❯ prune those two local branches",
	"────────────────────────────────",
	"  Opus 4.8 · demo-copilot-v45",
}

// Both captured live on 2026-09-01 from panes that fired a false
// "claude finished". The retry screen is STILL while it counts down, so
// only its text can save it; the scrolled one is a view we cannot judge.
var screenAPIRetry = []string{
	"✻ Waiting for API response · will retry in 2m 40s · check your network",
	"────────────────────────────────",
	"❯ ",
	"────────────────────────────────",
	"  Opus 4.8 · demo-api · ctx 12% (123k/1.0M)",
	"  ⏵⏵ bypass permissions on (shift+tab to cycle) · PR #123",
}

var screenScrolled = []string{
	"  ⎿  $ app docs list 2>&1 | head -60          Jump to bottom (click) ↓",
	"────────────────────────────────",
	"❯ ",
	"────────────────────────────────",
	"  Opus 4.8 · demo-api · ctx 13% (129k/1.0M)",
	"  ⏵⏵ bypass permissions on (shift+tab to cycle)",
}

// Captured live 2026-09-01 from the pane that fired a false "claude
// finished" mid-turn. The shells chip is LAST on the footer with nothing
// after it — the case background_shell_working's trailing \s+ could not
// match, which left NO screen rule matching and handed the verdict to the
// weak ✳-idle title.
var screenBgShellChipLast = []string{
	"  1.",
	"──────────── Database and server startup ─",
	"❯ ",
	"────────────",
	"  Opus 4.8 · example-svc · ctx 38% (382k/1.0M)",
	"  ⏵⏵ bypass permissions on · ← 2 agents · PR #7 · 2 shells",
}

// Every one of these was captured live on 2026-09-01 leading the tail of a
// logged completion — i.e. each was classified idle, mid-turn, and fired a
// false "claude finished". They differ from the fixtures above only in the
// bullet glyph, which herdr's six-literal class does not contain.
var spinnerBullets = []string{
	"✳ Whirring… (16m 14s · ↓ 69.0k tokens)",
	"✳ Elucidating… (1m 3s · ↓ 3.6k tokens)",
	"✳ Puzzling… (25s · ↓ 1.2k tokens)",
	"✳ Sautéing… (3m 44s · ↓ 16.8k tokens)",
	"✻ Cogitating… (1m 14s · ↓ 2.0k tokens)",
	"✽ Pondering… (34s · ↓ 2.1k tokens · thinking)",
}

func TestEverySpinnerBulletReadsWorking(t *testing.T) {
	m := claudeManifest(t)
	for _, line := range spinnerBullets {
		screen := []string{
			line,
			"────────────────────────────────",
			"❯ ",
			"────────────────────────────────",
			"  Opus 4.8 · demo-web · ctx 8% (76k/1.0M)",
			"  ⏵⏵ bypass permissions on (shift+tab to cycle)",
		}
		v, ok := m.eval(newSnapshot(screen, "✳ JS heap leak"), false)
		if !ok || v.state != "working" {
			t.Errorf("%q: got ok=%v state=%q rule=%s, want working",
				line, ok, v.state, v.rule)
		}
	}
}

func claudeManifest(t *testing.T) *cManifest {
	t.Helper()
	m := loadManifests()["claude"]
	if m == nil {
		t.Fatal("bundled claude manifest missing")
	}
	return m
}

func TestBundledManifestsCompile(t *testing.T) {
	ms := loadManifests()
	for _, id := range []string{"claude", "codex", "gemini", "grok", "opencode", "claude-code"} {
		if ms[id] == nil {
			t.Errorf("manifest %s missing", id)
		}
	}
}

func TestClaudeScreenStates(t *testing.T) {
	m := claudeManifest(t)
	for _, tc := range []struct {
		name  string
		lines []string
		title string
		state string
		skip  bool
	}{
		{"idle prompt box", screenIdle, "", "idle", false},
		// the working line outranks the (always-present) prompt box
		{"working beats prompt box", screenWorking, "", "working", false},
		{"permission prompt", screenBlocked, "", "blocked", false},
		{"transcript viewer freezes", screenViewer, "", "", true},
		// Not "working": the turn ended, the agent takes input, only the
		// shell is still going. Calling it working cost the completion
		// notification entirely — the agent never reached one.
		{"bg shell is background, not working", screenBgShell, "", "background", false},
		// The chip ends the line. Must still outrank the ✳-idle title.
		{"bg shell chip last on footer", screenBgShellChipLast,
			"✳ Database and server startup", "background", false},
		{"api retry is still the turn", screenAPIRetry, "", "working", false},
		{"scrolled transcript freezes", screenScrolled, "", "", true},
		// blocked screen evidence outranks the weak ✳-idle title
		{"blocked beats idle title", screenBlocked, "✳ some task", "blocked", false},
	} {
		v, ok := m.eval(newSnapshot(tc.lines, tc.title), false)
		if !ok && tc.state != "" {
			t.Errorf("%s: no rule matched", tc.name)
			continue
		}
		if ok && (v.state != tc.state || v.skip != tc.skip) {
			t.Errorf("%s: got state=%q skip=%v (rule %s) want %q/%v", tc.name, v.state, v.skip, v.rule, tc.state, tc.skip)
		}
	}
}

func TestTitleTier(t *testing.T) {
	m := claudeManifest(t)
	// spinner title outranks every screen rule: conclusive without capture
	v, ok := m.eval(newSnapshot(nil, "⠂ Build herdr-like tool"), true)
	if !ok || v.state != "working" || v.prio <= m.maxScreenPrio {
		t.Fatalf("spinner title not conclusive working: %+v max=%d", v, m.maxScreenPrio)
	}
	v, ok = m.eval(newSnapshot(nil, "◐ Reviewing changes"), true)
	if !ok || v.state != "working" {
		t.Fatalf("half-circle spinner (2.1.228) missed: %+v", v)
	}
	// ✳ idle is a WEAK verdict: matches, but never outranks the screen
	v, ok = m.eval(newSnapshot(nil, "✳ Design modular workbench"), true)
	if !ok || v.state != "idle" || v.prio > m.maxScreenPrio {
		t.Fatalf("✳ title should be weak idle: %+v max=%d", v, m.maxScreenPrio)
	}
	if _, ok := m.eval(newSnapshot(nil, "…/some/path"), true); ok {
		t.Fatal("plain path title matched a claude title rule")
	}

	g := loadManifests()["grok"]
	v, ok = g.eval(newSnapshot(nil, "Action Required - grok"), true)
	if !ok || v.state != "blocked" {
		t.Fatalf("grok Action Required: %+v", v)
	}
	v, ok = g.eval(newSnapshot(nil, "Build Codebase Command - grok"), true)
	if !ok || v.state != "idle" {
		t.Fatalf("grok idle title: %+v", v)
	}
}

func TestManifestOverride(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "winch", "agents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ov := `
id = "claude"
version = "9999.custom"
[[rules]]
id = "everything_is_blocked"
state = "blocked"
priority = 10
regex = ['(?s).*']
`
	if err := os.WriteFile(filepath.Join(sub, "claude.toml"), []byte(ov), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	m := loadManifests()["claude"]
	if m == nil || m.version != "9999.custom" {
		t.Fatalf("override not loaded: %+v", m)
	}
	if v, ok := m.eval(newSnapshot([]string{"anything"}, ""), false); !ok || v.state != "blocked" {
		t.Fatalf("override rules not in effect: %+v", v)
	}
}

func TestAgentKindNormalization(t *testing.T) {
	d := &daemon{}
	d.det.manifests = loadManifests()
	for cmd, want := range map[string]string{
		".claude-wrapped": "claude", // nix wrapper argv0, live-verified
		"claude":          "claude",
		"grok":            "grok",
		"codex":           "codex",
		"zsh":             "",
		"node":            "", // wrapper: resolved via process walk, not name
	} {
		if got := d.agentKind(cmd); got != want {
			t.Errorf("agentKind(%q) = %q want %q", cmd, got, want)
		}
	}
}

func TestPromptBoxRegion(t *testing.T) {
	s := newSnapshot(screenIdle, "")
	body := s.region("prompt_box_body")
	if len(body) != 1 || body[0] != "❯ do the spike first, add it to the doc" {
		t.Fatalf("prompt_box_body = %q", body)
	}
	if got := s.region("after_last_horizontal_rule"); len(got) != 1 || got[0] != "  Opus 4.8 · demo-copilot-v45" {
		t.Fatalf("after_last_horizontal_rule = %q", got)
	}
	if got := s.region("bottom_non_empty_lines(2)"); len(got) != 2 {
		t.Fatalf("bottom_non_empty_lines(2) = %q", got)
	}
}

// screenStreaming is the real screen from pane %3291 at 14:01:28 on
// 2026-09-01, the tick that fired a false "claude finished" while the agent
// was mid-turn. Claude was streaming a long message: the text has taken the
// row the spinner would use, so live_turn_working matches nothing, the
// footer carries the permissions-mode hint instead of "esc to interrupt",
// and live_prompt_box wins at 950 on the empty box. Idle, mid-turn.
//
// Kept verbatim because the point of the fixture is that it is NOT
// distinguishable from a finished turn by looking at it.
var screenStreaming = []string{
	"⏺ Nailed it. The local (dev) trace deanonymized everything — the leak is in AgentsModelsSessionStore.startProgressUpdateTimer.",
	"",
	"  Root cause",
	"",
	"  // AgentsModelsSessionStore.ts:9401",
	"  private startProgressUpdateTimer() {",
	"    const updateLoop = () => {",
	"      if (this.projectDataStore.hasActiveWork) {",
	"",
	"────────────────────────────────────────────────────────────",
	"❯ ",
	"────────────────────────────────────────────────────────────",
	"  Opus 4.8 · demo-web · ctx 8% (76k/1.0M)",
	"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← 2 agents",
}

// The screen that fooled us reads idle to the manifest, and always will —
// so assert the manifest really does say idle here, then assert the gate
// above it holds anyway while the text is still moving.
func TestStreamingScreenReadsIdle(t *testing.T) {
	m := claudeManifest(t)
	v, ok := m.eval(newSnapshot(screenStreaming, "✳ JS heap leak"), false)
	if !ok || v.state != "idle" {
		t.Fatalf("streaming screen: want an idle verdict (the bug), got ok=%v %+v", ok, v)
	}
}

// The still-screen gate. Idle verdicts over a MOVING screen never accrue,
// however many arrive, because a streaming turn produces exactly that; the
// same verdicts over a still screen complete the hold as before.
func TestIdleNeedsAStillScreen(t *testing.T) {
	d := &daemon{}
	novis := map[string]bool{}

	a := &agentInfo{kind: "claude", state: "working", win: "@2"}
	for i := 0; i < idleConfirms*4; i++ {
		if d.applyAgentState("%1", a, "idle", false, novis, true) {
			t.Fatalf("sample %d: idle published over a moving screen", i)
		}
	}
	if a.state != "working" {
		t.Fatalf("streaming agent left working, got %s", a.state)
	}
	// The screen stops: the turn really did end, and it lands as before.
	for i := 0; i < idleConfirms-1; i++ {
		if d.applyAgentState("%1", a, "idle", false, novis, false) {
			t.Fatalf("still sample %d published early", i)
		}
	}
	if !d.applyAgentState("%1", a, "idle", false, novis, false) || a.state != "done" {
		t.Fatalf("still screen should complete the hold, got %s", a.state)
	}
}

// Regression, from six live completions logged on 2026-09-01: every false
// one carried confirms=0 or 1 and every genuine one confirms=2, because
// motion left the still-run's clock set and idleCap then read as already
// expired — so the first still sample after any stretch of streaming
// completed the turn with no confirmations at all. A pause in the middle of
// a stream is exactly that shape, and it must still take the full hold.
func TestMotionDoesNotBypassTheHold(t *testing.T) {
	d := &daemon{}
	novis := map[string]bool{}
	a := &agentInfo{kind: "claude", state: "working", win: "@2"}

	// A still sample starts a run, so the run's clock is now set...
	if d.applyAgentState("%1", a, "idle", false, novis, false) {
		t.Fatal("one still sample completed the turn")
	}
	// ...the stream resumes and keeps going past idleCap...
	a.pendingAt = a.pendingAt.Add(-idleCap - time.Second)
	if d.applyAgentState("%1", a, "idle", false, novis, true) {
		t.Fatal("moving sample completed the turn")
	}
	if !a.pendingAt.IsZero() {
		t.Fatal("motion left the still-run clock set — idleCap now reads as expired")
	}
	// ...and the next pause must still serve the full hold, not walk
	// straight through the escape hatch on a clock nobody reset.
	if d.applyAgentState("%1", a, "idle", false, novis, false) {
		t.Fatal("first still sample after motion completed the turn")
	}
	if d.applyAgentState("%1", a, "idle", false, novis, false) {
		t.Fatal("second still sample after motion completed the turn")
	}
	if a.state != "working" {
		t.Fatalf("agent left working without the full hold, got %s", a.state)
	}
}

// A turn that ends with a background shell running is a turn that ended:
// it publishes immediately (positive evidence, unlike inferred idle), and
// the side work finishing afterwards must NOT look like a second
// completion — one turn, one notification.
func TestBackgroundIsACompletion(t *testing.T) {
	d := &daemon{}
	novis := map[string]bool{}

	a := &agentInfo{kind: "claude", state: "working", win: "@2"}
	// Confirmed like any other soft transition. It publishes on positive
	// text, but that text is a footer chip that truncates as the pane
	// rewraps — see TestBackgroundDoesNotFlap.
	for i := 0; i < idleConfirms-1; i++ {
		if d.applyAgentState("%1", a, "background", false, novis, true) {
			t.Fatalf("background published on sample %d", i)
		}
	}
	if !d.applyAgentState("%1", a, "background", false, novis, true) {
		t.Fatal("background never published")
	}
	if a.state != "background" {
		t.Fatalf("want background, got %s", a.state)
	}
	// Shells exit: idle, NOT done. done here would arm a second
	// notification for a turn that already announced itself.
	for i := 0; i < idleConfirms; i++ {
		d.applyAgentState("%1", a, "idle", false, novis, false)
	}
	if a.state != "idle" {
		t.Fatalf("side work finishing should land on idle, got %s", a.state)
	}
}

// Measured live on 2026-09-01, pane %2824:
//
//	16:02:07.261  working->background
//	16:02:07.561  background->working    300ms, one tick
//	16:02:09.274  working->background
//	16:02:09.574  background->working
//
// The footer chip truncates in and out as the pane rewraps, so the verdict
// alternates every scan. Neither side may publish while it does — and the
// confirm count is per target, or three samples split across two verdicts
// would publish whichever happened to land third.
func TestBackgroundDoesNotFlap(t *testing.T) {
	d := &daemon{}
	novis := map[string]bool{}
	a := &agentInfo{kind: "claude", state: "working", win: "@2"}

	for i := 0; i < 20; i++ {
		want := "background"
		if i%2 == 1 {
			want = "working"
		}
		if d.applyAgentState("%1", a, want, false, novis, true) {
			t.Fatalf("sample %d (%s) published mid-flap", i, want)
		}
	}
	if a.state != "working" {
		t.Fatalf("flap moved the agent to %s", a.state)
	}
	// The chip settles: three in a row now earns it.
	for i := 0; i < idleConfirms-1; i++ {
		d.applyAgentState("%1", a, "background", false, novis, true)
	}
	if !d.applyAgentState("%1", a, "background", false, novis, true) || a.state != "background" {
		t.Fatalf("settled chip never published, state=%s", a.state)
	}
}

// The confirm count is per TARGET, and this is the case that needs it:
// working -> idle and working -> background are BOTH held, so alternating
// between them never hits the unconditional reset that an unheld sample
// would trigger. Without a per-target counter, three samples split across
// two disagreeing verdicts publish whichever lands third — an agent
// declared finished by votes that never agreed on anything.
func TestSplitVerdictsDoNotAccumulate(t *testing.T) {
	d := &daemon{}
	novis := map[string]bool{}
	a := &agentInfo{kind: "claude", state: "working", win: "@2"}

	for i := 0; i < 20; i++ {
		want := "idle"
		if i%2 == 1 {
			want = "background"
		}
		if d.applyAgentState("%1", a, want, false, novis, false) {
			t.Fatalf("sample %d (%s) published on a split verdict", i, want)
		}
	}
	if a.state != "working" {
		t.Fatalf("split verdicts moved the agent to %s", a.state)
	}
}

// The still-screen requirement must NOT reach the reflow transitions. A
// running turn repaints constantly, so demanding stillness for
// background -> working is a condition that can never be met, and the agent
// would sit in background for the whole next turn.
func TestReflowDoesNotRequireAStillScreen(t *testing.T) {
	d := &daemon{}
	novis := map[string]bool{}
	a := &agentInfo{kind: "claude", state: "background", win: "@2"}

	for i := 0; i < idleConfirms-1; i++ {
		d.applyAgentState("%1", a, "working", false, novis, true) // moving
	}
	if !d.applyAgentState("%1", a, "working", false, novis, true) {
		t.Fatal("a moving screen could not confirm background -> working")
	}
	if a.state != "working" {
		t.Fatalf("stuck in %s", a.state)
	}
}

// A screen that never settles must NEVER complete the turn, however long it
// goes on. A 30s cap that "believed" the idle verdict after enough motion
// shipped on 2026-09-01 and immediately notified against a live progress bar
// at 40%; the premise was backwards. Waiting cannot turn evidence of work
// into evidence of completion.
func TestMovingScreenNeverCompletes(t *testing.T) {
	d := &daemon{}
	novis := map[string]bool{}

	a := &agentInfo{kind: "claude", state: "working", win: "@2"}
	for i := 0; i < 500; i++ {
		if d.applyAgentState("%1", a, "idle", false, novis, true) {
			t.Fatalf("sample %d completed the turn over a moving screen", i)
		}
	}
	if a.state != "working" {
		t.Fatalf("agent left working, got %s", a.state)
	}
}

// The anti-flap hold: working -> idle needs idleConfirms consecutive
// samples — visible evidence included (alternating screens flap through
// any bypass). Blocked publishes instantly. Completions in an unwatched
// window become "done" and stick.
func TestIdleHoldAndDone(t *testing.T) {
	d := &daemon{}
	novis := map[string]bool{}
	vis := map[string]bool{"@1": true}

	a := &agentInfo{kind: "claude", state: "working", win: "@1"}
	if d.applyAgentState("%1", a, "idle", true, vis, false) {
		t.Fatal("first idle sample published")
	}
	if d.applyAgentState("%1", a, "idle", true, vis, false) {
		t.Fatal("second idle sample published")
	}
	if !d.applyAgentState("%1", a, "idle", true, vis, false) {
		t.Fatal("third idle sample held")
	}
	if a.state != "idle" {
		t.Fatalf("visible-window completion should be idle, got %s", a.state)
	}

	// completion in an unwatched window -> done, and later idle samples
	// must not clear the flag
	a = &agentInfo{kind: "claude", state: "working", win: "@2"}
	d.applyAgentState("%2", a, "idle", false, novis, false)
	d.applyAgentState("%2", a, "idle", false, novis, false)
	if !d.applyAgentState("%2", a, "idle", false, novis, false) || a.state != "done" {
		t.Fatalf("unwatched completion should be done, got %s", a.state)
	}
	if d.applyAgentState("%2", a, "idle", false, novis, false) || a.state != "done" {
		t.Fatal("idle sample cleared the done flag")
	}

	// blocked publishes instantly, from anywhere
	a = &agentInfo{kind: "claude", state: "working", win: "@1"}
	if !d.applyAgentState("%3", a, "blocked", true, vis, false) {
		t.Fatal("blocked must publish instantly")
	}
	// blocked -> idle unwatched is also a completion -> done, no hold
	if !d.applyAgentState("%3", a, "idle", false, novis, false) || a.state != "done" {
		t.Fatalf("blocked completion should be done instantly, got %s", a.state)
	}

	// an interleaved working sample clears the pending hold
	a = &agentInfo{kind: "claude", state: "working", win: "@1"}
	d.applyAgentState("%4", a, "idle", false, vis, false)
	d.applyAgentState("%4", a, "working", true, vis, false)
	if d.applyAgentState("%4", a, "idle", false, vis, false) {
		t.Fatal("hold did not restart after working interrupted it")
	}
}

func TestBlockedReasonLabel(t *testing.T) {
	m := claudeManifest(t)
	v, ok := m.eval(newSnapshot(screenBlocked, ""), false)
	if !ok || v.label != "bash permission" {
		t.Fatalf("bash prompt label: %+v", v)
	}
	// same prompt without the Bash header: the anywhere rule, generic label
	v, ok = m.eval(newSnapshot(screenBlocked[2:], ""), false)
	if !ok || v.label != "permission prompt" {
		t.Fatalf("anywhere prompt label: %+v", v)
	}
	// no explicit label in the TOML: the rule id humanizes
	v, ok = loadManifests()["opencode"].eval(newSnapshot([]string{"△ Permission required"}, ""), false)
	if !ok || v.label != "permission required" {
		t.Fatalf("fallback label: %+v", v)
	}
}

// A blocked pane's TITLE is the task it was doing before it stopped, which
// says nothing about why it stopped and actively misleads. The matched rule's
// reason takes the name row instead. herdr would show the stale title here;
// this is a winch departure and the reason it is worth keeping is that the
// title is wrong, not merely less useful.
func TestBlockedRowShowsReason(t *testing.T) {
	rows := agentCardFixture(t, pane{
		ID: "%1", WindowID: "@1", SessionID: "$1", Title: "✳ Old task",
		Agent: "claude", AgentState: "blocked", AgentReason: "permission prompt",
	})
	if got := rows[1].label; !strings.Contains(got, "permission prompt") {
		t.Errorf("name row = %q, want the blocked reason", got)
	}
	for _, r := range rows {
		if strings.Contains(r.label, "Old task") {
			t.Errorf("the stale pre-prompt title is still on the card: %q", r.label)
		}
	}
}

// The card is herdr's claude layout: workspace+tab, then the agent's own
// conversation name, then the agent kind. The name row is the whole point —
// it is the only field that separates two agents in one window.
func TestAgentCardIsNameLed(t *testing.T) {
	rows := agentCardFixture(t, pane{
		ID: "%1", WindowID: "@1", SessionID: "$1", Title: "⠧ Build herdr-like tool for tmux",
		Agent: "claude", AgentState: "working",
	})
	for _, c := range []struct {
		row  int
		want string
	}{
		{0, "main · 2 · claude"},              // workspace · tab · agent, one line
		{1, "Build herdr-like tool for tmux"}, // terminal_title_stripped
	} {
		if got := strings.TrimSpace(rows[c.row].label); got != c.want {
			t.Errorf("row %d = %q, want %q", c.row, got, c.want)
		}
	}
	// Only the first row takes the selection; the rest ride with it.
	if rows[0].cont || !rows[1].cont {
		t.Errorf("wrong continuation flags: %v %v", rows[0].cont, rows[1].cont)
	}
	// row_gap 0, as herdr settled on: no blank line inside the section.
	for _, r := range rows {
		if r.gap {
			t.Errorf("agent card still emits a gap row")
		}
	}
}

// agentCardFixture builds a one-agent world and returns just that agent's
// rows, so the tests above read as claims about the card.
func agentCardFixture(t *testing.T, p pane) []row {
	t.Helper()
	// Pin the width: the card's SHAPE is what these tests are about, and at
	// the 26-column default a realistic conversation name token-drops (see
	// TestFitTokens, which is where that belongs).
	save := listW
	t.Cleanup(func() { listW = save })
	listW = 55

	st := &store{
		sessions: map[string]session{"$1": {ID: "$1", Name: "main"}},
		windows: map[string]window{
			"@1": {ID: "@1", SessionID: "$1", Index: 2, Active: true},
			// A second window, so the `tab` token is not elided — herdr
			// drops it for single-window sessions, where an index that
			// reads the same on every card says nothing.
			"@9": {ID: "@9", SessionID: "$1", Index: 9},
		},
		panes: map[string]pane{p.ID: p},
	}
	var out []row
	for _, r := range st.rows(nil) {
		if r.arow && !r.gap { // the card, not the blank row before it
			out = append(out, r)
		}
	}
	if len(out) != 2 {
		t.Fatalf("want a 2-row agent card, got %d: %+v", len(out), out)
	}
	return out
}

// The session card's second row: git branch + ahead/behind; absent when
// the session isn't in a repo.
func TestSessionGitRow(t *testing.T) {
	st := &store{
		sessions: map[string]session{
			"$1": {ID: "$1", Name: "dots", Branch: "winch", Ahead: 2, Behind: 1},
			"$2": {ID: "$2", Name: "scratch"},
		},
		windows: map[string]window{"@1": {ID: "@1", SessionID: "$1", Active: true}},
		panes:   map[string]pane{},
	}
	rows := st.rows(nil)
	var git []string
	for _, r := range rows {
		if r.cont && r.sess != "" {
			git = append(git, r.label)
		}
	}
	if len(git) != 1 || !strings.Contains(git[0], "winch ↑2 ↓1") {
		t.Fatalf("git rows = %q", git)
	}
}

func TestAgentTaskTitle(t *testing.T) {
	for in, want := range map[string]string{
		"⠂ Build herdr-like tool": "Build herdr-like tool",
		"✳ Convert async tests":   "Convert async tests",
		"◐ Reviewing":             "Reviewing",
		"plain title":             "plain title",
	} {
		if got := agentTaskTitle(in); got != want {
			t.Errorf("agentTaskTitle(%q) = %q want %q", in, got, want)
		}
	}
}

// The agents region is PINNED: whatever the tree length and selection,
// every agent row (up to the region cap) has a screen row, and rowAt
// round-trips the geometry paint uses.
func TestListLayoutPinsAgents(t *testing.T) {
	rows := make([]row, 0, 43)
	for i := 0; i < 40; i++ {
		rows = append(rows, row{label: "tree", window: "@1"})
	}
	for i := 0; i < 3; i++ {
		rows = append(rows, row{label: "agent", window: "@2", arow: true})
	}
	for _, sel := range []int{0, 20, 39, 40, 42} {
		lay := layoutList(rows, sel, 20)
		// equal halves (herdr default ratio 0.5): 19 usable -> 10 tree,
		// rule, 9 agents
		if lay.sepY != 10 || lay.agentH != 9 {
			t.Fatalf("sel=%d: not an equal split: %+v", sel, lay)
		}
		seen := map[int]bool{}
		for y := 0; y < 20; y++ {
			if i := lay.rowAt(y, len(rows)); i >= 0 {
				if seen[i] {
					t.Fatalf("sel=%d: row %d painted twice", sel, i)
				}
				seen[i] = true
			}
		}
		for a := 40; a < 43; a++ {
			if !seen[a] {
				t.Fatalf("sel=%d: agent row %d has no screen row", sel, a)
			}
		}
		if !seen[sel] {
			t.Fatalf("sel=%d: selected row not on screen", sel)
		}
	}
	// no agents: single region, full height
	if lay := layoutList(rows[:40], 5, 20); lay.sepY != -1 || lay.treeH != 20 {
		t.Fatalf("no-agent layout wrong: %+v", lay)
	}
	// the split is FIXED: a short tree doesn't move the divider (stable
	// geometry is the point — herdr's model)
	short := append(append([]row(nil), rows[:6]...), rows[40:]...)
	if lay := layoutList(short, 0, 50); lay.sepY != 25 || lay.agentH != 24 {
		t.Fatalf("divider should stay at the equal split: %+v", lay)
	}
}

// The divider follows the drag ratio, clamped to 3 rows per panel.
func TestListSplitRatio(t *testing.T) {
	old := listSplit
	t.Cleanup(func() { listSplit = old })
	rows := make([]row, 0, 43)
	for i := 0; i < 40; i++ {
		rows = append(rows, row{label: "tree", window: "@1"})
	}
	for i := 0; i < 3; i++ {
		rows = append(rows, row{label: "agent", window: "@2", arow: true})
	}
	listSplit = 0.7
	if lay := layoutList(rows, 0, 20); lay.sepY != 13 {
		t.Fatalf("ratio 0.7 -> sepY %d, want 13", lay.sepY)
	}
	listSplit = 0.1
	if lay := layoutList(rows, 0, 20); lay.sepY != 3 {
		t.Fatalf("ratio 0.1 should clamp to 3-row tree, got %d", lay.sepY)
	}
}
