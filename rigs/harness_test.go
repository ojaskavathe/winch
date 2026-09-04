// Package rigs is winch's integration test harness. Each test gets an
// ISOLATED tmux server (socket named after the test + pid), a fake attached
// client on a real pty, and a daemon — so the whole suite runs in parallel
// and never touches your real tmux.
//
//	go test ./...                # full suite, parallel
//	go test -run TestEqualize    # one test
//	go test -short               # skip the slow big-scrollback test
//	go test -v                   # per-assert output
package rigs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var winchBin string

// sideW mirrors the daemon's listWidth — the sidebar's fixed column width.
const sideW = 26

// tmuxDir is this uid's tmux socket directory with symlinks resolved (macOS
// /tmp -> /private/tmp) — the same canonical form the daemon embeds in its
// argv, so pkill/pgrep -f patterns built from it match.
var tmuxDir = func() string {
	d := os.Getenv("TMUX_TMPDIR")
	if d == "" {
		d = "/tmp"
	}
	if r, err := filepath.EvalSymlinks(d); err == nil {
		d = r
	}
	return filepath.Join(d, fmt.Sprintf("tmux-%d", os.Getuid()))
}()

// winchDir mirrors winchSocketPath's directory.
var winchDir = fmt.Sprintf("/tmp/winch-%d", os.Getuid())

func TestMain(m *testing.M) {
	sweepStaleServers()
	tmp, err := os.MkdirTemp("", "winch-rig-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	winchBin = filepath.Join(tmp, "winch")
	cmd := exec.Command("go", "build", "-o", winchBin, ".")
	cmd.Dir = "../cmd/winch"
	if b, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build winch: %v\n%s", err, b)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

// rigProfile is the fake client's environment. Timing bugs (flicker,
// transitions) only reproduce against the shape of the REAL terminal: a
// 200x50 client with no synchronized-output capability redraws about a
// quarter of the cells and batches them differently, which hides artifacts
// that are plainly visible at 480x96 in kitty.
type rigProfile struct {
	cols, rows int
	term       string
	features   string // terminal-features to declare, "" for none
}

var (
	stdProfile = rigProfile{cols: 200, rows: 50, term: "xterm-256color"}
	// liveProfile mirrors Ojas's kitty window (see tmux.nix: kitty needs
	// the sync feature declared, tmux does not auto-detect it).
	liveProfile = rigProfile{cols: 480, rows: 96, term: "xterm-kitty", features: "xterm-kitty:sync"}
)

// Rig is one isolated tmux world. Standard shape: session work (w1 = a
// split holding a MARKW1 loop pane, beta, gamma), session play (2 windows),
// a fake client attached to work:beta, daemon running.
type Rig struct {
	t    *testing.T
	L    string // tmux socket name
	prof rigProfile

	W1, W2, W3, P1 string // window ids
	LW1, LW2, LW3  string // pre-dock layout baselines
	CL             string // fake client name
	Sock           string // winch socket path (log at Sock+".log")

	clientCmd *exec.Cmd
	ptyMaster *os.File

	recMu     sync.Mutex
	recording bool
	recBuf    []byte
	recChunks []recChunk
	recStart  time.Time
}

// recChunk is one read from the client pty, stamped with when it arrived —
// enough to replay the stream in time and ask "what was on screen, for how
// long", which is what a flicker actually is.
type recChunk struct {
	At   time.Duration
	Data []byte
}

func New(t *testing.T) *Rig { return newRig(t, stdProfile) }

// NewLive is New with a client shaped like the real one: 480x96, kitty,
// synchronized output declared. Use it for anything about what the user
// SEES during a transition — artifacts scale with the redraw.
func NewLive(t *testing.T) *Rig { return newRig(t, liveProfile) }

func newRig(t *testing.T, prof rigProfile) *Rig {
	t.Parallel()
	name := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(t.Name(), "")
	r := &Rig{t: t, L: strings.ToLower(name) + strconv.Itoa(os.Getpid()), prof: prof}
	t.Cleanup(r.teardown)
	r.setup()
	return r
}

func (r *Rig) teardown() {
	r.TQ("kill-server")
	// kill-server does not unlink the socket. Removing it here is what keeps
	// the sweep in sweepStaleServers a no-op instead of a growing tax on
	// every future run — see the note there.
	os.Remove(tmuxDir + "/" + r.L)
	// By socket path, not binary name: catches the daemon, the TUI it
	// spawned, and stragglers from a crashed earlier run of this test.
	exec.Command("pkill", "-f", tmuxDir+"/"+r.L).Run()
	if r.clientCmd != nil && r.clientCmd.Process != nil {
		r.clientCmd.Process.Kill()
	}
	if r.ptyMaster != nil {
		r.ptyMaster.Close()
	}
	if r.t != nil && r.t.Failed() {
		r.t.Logf("keeping daemon log: %s.log", r.Sock)
		return
	}
	for _, f := range glob(winchDir + "/" + r.L + "-*") {
		os.Remove(f)
	}
}

func glob(pat string) []string { m, _ := filepath.Glob(pat); return m }

// sweepStaleServers kills rig tmux servers leaked by interrupted earlier runs
// and DELETES their socket files. Sockets are named <testname><pid>; per-test
// teardown only knows its OWN pid, so a killed `go test` leaves its servers
// running forever.
//
// The delete is the whole point, and its absence was the single largest cost
// in the suite. A tmux server outlives the test that spawned it, so the kill
// has to happen — but kill-server does not unlink the socket, so every
// swept-but-not-removed file was swept again on every future run, forever.
// 6857 of them had accumulated, each costing one serial fork of tmux: 26
// seconds of a 47-second suite, before a single test ran, growing daily.
//
// Bounded-parallel because the work is one exec apiece and entirely
// I/O-bound, and because a backlog should cost seconds once rather than
// minutes.
func sweepStaleServers() {
	var stale []string
	for _, sock := range glob(tmuxDir + "/test*") {
		name := filepath.Base(sock)
		i := len(name)
		for i > 0 && name[i-1] >= '0' && name[i-1] <= '9' {
			i--
		}
		pid, err := strconv.Atoi(name[i:])
		if i == len(name) || err != nil || pid == os.Getpid() {
			continue
		}
		// signal 0: is the owning test process still alive? If it is, the
		// sockets under it belong to a concurrent run — leave them alone.
		if p, _ := os.FindProcess(pid); p != nil && p.Signal(syscall.Signal(0)) == nil {
			continue
		}
		stale = append(stale, sock)
	}
	if len(stale) == 0 {
		return
	}
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for _, sock := range stale {
		wg.Add(1)
		go func(sock string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			exec.Command("tmux", "-S", sock, "kill-server").Run()
			os.Remove(sock)
		}(sock)
	}
	wg.Wait()
	fmt.Fprintf(os.Stderr, "swept %d stale rig servers\n", len(stale))
}

// envSansTmux strips TMUX/TMUX_PANE so nothing ever looks nested.
func envSansTmux() []string {
	var out []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "TMUX=") || strings.HasPrefix(e, "TMUX_PANE=") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// KillDaemon kills the daemon (and the TUI it spawned) the way a crash or a
// deploy's pkill does — no teardown, no restore. The next D() respawns it.
// Matched by socket path, exactly as teardown does.
func (r *Rig) KillDaemon() {
	r.t.Helper()
	exec.Command("pkill", "-f", tmuxDir+"/"+r.L).Run()
	r.await(3000, "daemon gone", func() bool {
		return exec.Command("pgrep", "-f", tmuxDir+"/"+r.L).Run() != nil
	})
}

// T runs a tmux command against this rig's server; fatal on error.
func (r *Rig) T(args ...string) string {
	r.t.Helper()
	out, err := r.TQ(args...)
	if err != nil {
		r.t.Fatalf("tmux %v: %v\n%s", args, err, out)
	}
	return out
}

// TQ is T but quiet: returns the error instead of failing the test.
func (r *Rig) TQ(args ...string) (string, error) {
	pre := []string{"-L", r.L}
	// RIGVV=<dir>: run tmux with -vv so the server writes its debug log
	// (tmux-server-*.log) into <dir>. The server inherits log level and cwd
	// from the first client, so this must be on from the rig's first
	// command. Used by the ccanatomy probes to read tmux's own account of a
	// render storm.
	if vv := os.Getenv("RIGVV"); vv != "" {
		pre = append(pre, "-vv")
	}
	cmd := exec.Command("tmux", append(pre, args...)...)
	cmd.Dir = os.Getenv("RIGVV") // "" means inherit
	cmd.Env = envSansTmux()
	b, err := cmd.CombinedOutput()
	return strings.TrimRight(string(b), "\n"), err
}

// D runs the winch client binary against this rig's server. WINCH_BENCH
// rides along so the (auto-spawned) daemon logs its µs instrumentation —
// free diagnostics whenever a rig fails.
func (r *Rig) D(args ...string) string {
	r.t.Helper()
	cmd := exec.Command(winchBin, append([]string{"-L", r.L}, args...)...)
	cmd.Env = append(envSansTmux(), "WINCH_BENCH=1", "WINCH_TEST_FAST=1")
	b, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("winch %v: %v\n%s", args, err, b)
	}
	return strings.TrimRight(string(b), "\n")
}

func (r *Rig) setup() {
	r.t.Helper()
	r.teardown()
	// A server from a crashed earlier run may still be dying; wait it out.
	r.await(3000, "old server gone", func() bool {
		_, err := r.TQ("has-session")
		return err != nil
	})
	sx, sy := strconv.Itoa(r.prof.cols), strconv.Itoa(r.prof.rows)
	r.T("-f", "/dev/null", "new-session", "-d", "-s", "work", "-x", sx, "-y", sy)
	if r.prof.features != "" {
		r.T("set-option", "-as", "terminal-features", r.prof.features)
	}
	// No notification flap guard by default. The guard is a real second of
	// waiting per blocked agent, and every rig that touches detection would
	// pay it to test something else entirely. The guard itself is covered
	// where it belongs, by turning it back UP in notify_test.go.
	r.T("set-option", "-g", "@winch-notify-delay", "0")
	r.W1 = r.T("display-message", "-p", "-t", "work:", "#{window_id}")
	r.T("split-window", "-h", "-t", r.W1, "while :; do echo MARKW1; sleep 2; done")
	r.T("new-window", "-t", "work:", "-n", "beta")
	r.T("new-window", "-t", "work:", "-n", "gamma")
	for _, ln := range strings.Split(r.T("list-windows", "-t", "work", "-F", "#{window_id} #{window_name}"), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		switch f[1] {
		case "beta":
			r.W2 = f[0]
		case "gamma":
			r.W3 = f[0]
		}
	}
	r.T("new-session", "-d", "-s", "play", "-x", sx, "-y", sy)
	r.P1 = r.T("display-message", "-p", "-t", "play:", "#{window_id}")
	r.T("new-window", "-t", "play:", "-n", "ptwo")
	r.T("select-window", "-t", r.W2)
	r.attachFakeClient()
	// Attach is done when the client's status line has squeezed the current
	// window to 49 rows and the client shows up in list-clients.
	statusH := r.prof.rows - 1 // one row goes to the status line
	r.await(5000, "client attached", func() bool {
		return r.winH(r.W2) == statusH && r.realClient() != ""
	})
	r.CL = r.realClient()
	// Baselines AFTER attach, each while ITS window is current — the
	// client's status line resizes only the current window (window-size
	// latest).
	r.T("select-window", "-t", r.W1)
	r.await(3000, "w1 sized", func() bool { return r.winH(r.W1) == statusH })
	r.LW1 = r.T("display-message", "-p", "-t", r.W1, "#{window_layout}")
	r.T("select-window", "-t", r.W3)
	r.await(3000, "w3 sized", func() bool { return r.winH(r.W3) == statusH })
	r.LW3 = r.T("display-message", "-p", "-t", r.W3, "#{window_layout}")
	r.T("select-window", "-t", r.W2)
	r.await(3000, "w2 sized", func() bool { return r.winH(r.W2) == statusH })
	r.LW2 = r.T("display-message", "-p", "-t", r.W2, "#{window_layout}")
	// Pin the sidebar's session order to the historical arrangement (play,
	// work) so tests that navigate the list by position stay stable now that
	// the default is creation order, not alphabetical. The creation-order
	// default is covered by the unit tests, reordering by TestReorderSessions.
	r.T("set-option", "-g", "@winch-session-order", `["play","work"]`)
	// ls starts the daemon and round-trips one world snapshot — its success
	// is the ready signal.
	r.D("ls")
	for _, ln := range strings.Split(r.D("sock"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "winch:") {
			r.Sock = strings.Fields(ln)[1]
		}
	}
}

// attachFakeClient attaches a real (non-control) 200x50 tmux client on a pty.
func (r *Rig) attachFakeClient() {
	r.t.Helper()
	master, slave, err := openPty(r.prof.rows, r.prof.cols)
	if err != nil {
		r.t.Fatalf("openPty: %v", err)
	}
	cmd := exec.Command("tmux", "-L", r.L, "attach", "-t", "work")
	cmd.Env = append(envSansTmux(), "TERM="+r.prof.term)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = sysProcAttrTTY()
	if err := cmd.Start(); err != nil {
		r.t.Fatalf("attach client: %v", err)
	}
	slave.Close()
	go func() { // drain so tmux never blocks writing to the client
		buf := make([]byte, 65536)
		for {
			n, err := master.Read(buf)
			if err != nil {
				return
			}
			r.recMu.Lock()
			if r.recording {
				r.recBuf = append(r.recBuf, buf[:n]...)
				r.recChunks = append(r.recChunks, recChunk{
					At:   time.Since(r.recStart),
					Data: append([]byte(nil), buf[:n]...),
				})
			}
			r.recMu.Unlock()
		}
	}()
	r.clientCmd, r.ptyMaster = cmd, master
}

// StartRecord begins capturing everything tmux writes to the fake client's
// terminal — the ground truth of what a user would SEE, intermediate frames
// included.
func (r *Rig) StartRecord() {
	r.recMu.Lock()
	r.recBuf = nil
	r.recChunks = nil
	r.recStart = time.Now()
	r.recording = true
	r.recMu.Unlock()
}

// StopRecordT ends the capture and returns the stream as time-stamped
// chunks (StopRecord's flat bytes lose WHEN, and a flicker is defined by
// how long the wrong thing stayed on screen).
func (r *Rig) StopRecordT() []recChunk {
	r.recMu.Lock()
	defer r.recMu.Unlock()
	r.recording = false
	out := r.recChunks
	r.recChunks, r.recBuf = nil, nil
	return out
}

// StopRecord ends the capture and returns the raw byte stream.
func (r *Rig) StopRecord() []byte {
	r.recMu.Lock()
	defer r.recMu.Unlock()
	r.recording = false
	out := r.recBuf
	r.recBuf = nil
	return out
}

// ---- assertions & helpers ----

func sleep(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// Settle blocks until the daemon has processed everything queued before this
// call. The daemon is single-threaded — every field is touched only from the
// consume loop — so a command that comes back is proof that every control-mode
// notification ahead of it has already been folded into the world.
//
// This is what most `sleep(600)` calls in this suite were really asking for,
// and it costs a process spawn (~15ms) instead of six hundred. It does NOT
// prove the TUI has repainted: the sidebar is a separate process reading a
// socket, so anything asserting on painted cells still has to await the cells.
func (r *Rig) Settle() {
	r.t.Helper()
	r.D("ls")
}

func (r *Rig) Chk(name string, cond bool) {
	r.t.Helper()
	if cond {
		r.t.Logf("ok - %s", name)
	} else {
		r.t.Errorf("FAIL - %s", name)
	}
}

// await polls f every 15ms until true; fatal after ms elapse. Generous
// ceilings cost nothing when the condition is already met.
func (r *Rig) await(ms int, what string, f func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	for {
		if f() {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("await %s: not true after %dms", what, ms)
		}
		sleep(15)
	}
}

// winH is a window's height ("" or unparsable -> 0).
func (r *Rig) winH(win string) int {
	out, _ := r.TQ("display-message", "-p", "-t", win, "#{window_height}")
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

// realClient returns the first non-control-mode client's name.
func (r *Rig) realClient() string {
	out, _ := r.TQ("list-clients", "-F", "#{client_name} #{client_control_mode}")
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) == 2 && f[1] == "0" {
			return f[0]
		}
	}
	return ""
}

// WaitUntil polls f at 10ms until ms elapse, reporting whether it came true.
//
// The argument is MILLISECONDS, matching await. It used to be a poll count,
// which every caller in the suite read as milliseconds — `WaitUntil(2000)`
// meant twenty seconds, not two. That costs nothing when the condition is
// already true and everything when it is not, which is precisely the case a
// negative assertion or a search loop hits on every iteration.
func (r *Rig) WaitUntil(ms int, f func() bool) bool {
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	for {
		if f() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		sleep(10)
	}
}

// Sidebar is the winch TUI pane, wherever it currently lives.
type Sidebar struct {
	Pane, Win           string
	Left, Width, Active int
}

func (r *Rig) Side() Sidebar {
	out := r.T("list-panes", "-a", "-F",
		"#{pane_id} #{pane_current_command} #{window_id} #{pane_left} #{pane_width} #{pane_active}")
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) == 6 && strings.Contains(f[1], "winch") {
			left, _ := strconv.Atoi(f[3])
			width, _ := strconv.Atoi(f[4])
			active, _ := strconv.Atoi(f[5])
			return Sidebar{Pane: f[0], Win: f[2], Left: left, Width: width, Active: active}
		}
	}
	return Sidebar{}
}

func (r *Rig) ClientWin() string {
	for _, ln := range strings.Split(r.T("list-clients", "-F", "#{client_name} #{window_id}"), "\n") {
		f := strings.Fields(ln)
		if len(f) == 2 && f[0] == r.CL {
			return f[1]
		}
	}
	return ""
}

// Undock closes the sidebar wherever the keyboard happens to be.
//
// M-s is contextual: from a content pane it FOCUSES the sidebar, and only
// closes once the keyboard is in it (TestMsIsContextual). So a test that
// moved focus — to check a border colour, or by committing with Enter —
// cannot assume one press undocks. This says "closed" and means it.
func (r *Rig) Undock() {
	if sp := r.Side().Pane; sp != "" && r.ClientPane() != sp {
		r.D("toggle", r.CL)
		r.WaitUntil(2000, func() bool { return r.ClientPane() == sp })
	}
	r.D("toggle", r.CL)
}

// ClientPane is the pane the client's keyboard is in.
func (r *Rig) ClientPane() string {
	for _, ln := range strings.Split(r.T("list-clients", "-F", "#{client_name} #{pane_id}"), "\n") {
		f := strings.Fields(ln)
		if len(f) == 2 && f[0] == r.CL {
			return f[1]
		}
	}
	return ""
}

func (r *Rig) ClientSess() string {
	for _, ln := range strings.Split(r.T("list-clients", "-F", "#{client_name} #{session_name}"), "\n") {
		f := strings.Fields(ln)
		if len(f) == 2 && f[0] == r.CL {
			return f[1]
		}
	}
	return ""
}

func (r *Rig) Zoomed(win string) bool {
	return r.T("display-message", "-p", "-t", win, "#{window_zoomed_flag}") == "1"
}

// Spacers counts spacer panes server-wide (their distinctive start command;
// tmux 3.7 wraps #{pane_start_command} in double quotes).
func (r *Rig) Spacers() int {
	n := 0
	for _, ln := range strings.Split(r.T("list-panes", "-a", "-F", "#{pane_start_command}"), "\n") {
		if strings.Trim(ln, `"`) == "sleep 100000001" {
			n++
		}
	}
	return n
}

// Layout returns a window's layout with the checksum prefix stripped.
func (r *Rig) Layout(win string) string {
	l := r.T("display-message", "-p", "-t", win, "#{window_layout}")
	if i := strings.Index(l, ","); i >= 0 {
		return l[i+1:]
	}
	return l
}

func (r *Rig) SendKeys(pane string, keys ...string) {
	r.T(append([]string{"send-keys", "-t", pane}, keys...)...)
}

// Type writes raw bytes to the CLIENT's pty, which is the only way to exercise
// a root key binding. send-keys hands bytes straight to a pane and never
// consults the key table, so a test built on it cannot tell a working bind
// from a missing one — the very thing that decides whether C-j reaches the
// sidebar or moves the tmux pane focus out of it.
func (r *Rig) Type(s string) {
	r.t.Helper()
	if r.ptyMaster == nil {
		r.t.Fatal("Type: no client pty")
	}
	if _, err := r.ptyMaster.WriteString(s); err != nil {
		r.t.Fatalf("Type %q: %v", s, err)
	}
}

// Mouse injects an SGR mouse report straight into a pane's stdin via
// send-keys -H — deterministic for TUI tests, bypassing tmux's own mouse
// routing (which needs a real client pointer).
func (r *Rig) Mouse(pane string, btn, x, y int, press bool) {
	r.t.Helper()
	c := byte('M')
	if !press {
		c = 'm'
	}
	seq := fmt.Sprintf("\x1b[<%d;%d;%d%c", btn, x, y, c)
	args := []string{"send-keys", "-t", pane, "-H"}
	for _, b := range []byte(seq) {
		args = append(args, fmt.Sprintf("%02x", b))
	}
	r.T(args...)
}

// Click is a full press+release at pane coordinates.
func (r *Rig) Click(pane string, x, y int) {
	r.Mouse(pane, 0, x, y, true)
	r.Mouse(pane, 0, x, y, false)
}

func (r *Rig) Capture(pane string) string {
	out, _ := r.TQ("capture-pane", "-p", "-t", pane)
	return out
}

// ShowOpt returns a session/window option's value ("" when unset — tmux
// errors on unset user options, which counts as unset here).
func (r *Rig) ShowOpt(flags ...string) string {
	out, err := r.TQ(append([]string{"show-options"}, flags...)...)
	if err != nil {
		return ""
	}
	return out
}

// LogHas reports whether the daemon log contains pat.
func (r *Rig) LogHas(pat string) bool {
	b, err := os.ReadFile(r.Sock + ".log")
	return err == nil && regexp.MustCompile(pat).Match(b)
}

// TuiBenchHas greps the TUI's bench log — a separate file from the daemon
// log (the TUI is its own process inside a tmux pane).
func (r *Rig) TuiBenchHas(pat string) bool {
	b, err := os.ReadFile(r.Sock + ".tui-bench.log")
	return err == nil && regexp.MustCompile(pat).Match(b)
}

// WinchPanes counts winch TUI panes inside the given target's panes.
func (r *Rig) WinchPanes(flags ...string) int {
	out, err := r.TQ(append(append([]string{"list-panes"}, flags...), "-F", "#{pane_current_command}")...)
	if err != nil {
		return 0
	}
	n := 0
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "winch") {
			n++
		}
	}
	return n
}
