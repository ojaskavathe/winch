package rigs

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestCCStormAnatomy bisects the render storm: during each event pattern it
// records BOTH the client-presented stream (StartRecord) and CC's own pty
// writes (pipe-pane, chunk-timestamped). If CC writes ~N sync blocks and the
// client presents ~N, the storm is born in CC; if CC writes few and the
// client presents hundreds, tmux is amplifying. Captures are left on disk
// for offline analysis (paths in the test log).
func TestCCStormAnatomy(t *testing.T) {
	cc := os.Getenv("CCBIN")
	if cc == "" {
		t.Skip("set CCBIN=/path/to/claude")
	}
	out := os.Getenv("ANATDIR")
	if out == "" {
		out = t.TempDir()
	}
	r := NewLive(t)
	r.T("set-option", "-g", "focus-events", "on")
	r.T("respawn-pane", "-k", "-t", "play:0.0", cc)
	r.T("split-window", "-h", "-t", "play:0")
	sleep(6000)
	pane := r.T("display-message", "-p", "-t", "play:0.0", "#{pane_id}")
	other := r.T("display-message", "-p", "-t", "play:0.1", "#{pane_id}")
	r.T("switch-client", "-c", r.CL, "-t", "play", ";", "select-window", "-t", r.P1)
	sleep(800)

	frames := func(rec []byte) int { return bytes.Count(rec, []byte("\x1b[?2026h")) }
	w, _ := strconv.Atoi(r.T("display-message", "-p", "-t", pane, "#{pane_width}"))
	narrow, wide := strconv.Itoa(w-45), strconv.Itoa(w)

	// capture runs act while piping the CC pane's writes to files; setup ran
	// already (each case arranges its own starting geometry/focus)
	capture := func(name string, act func()) {
		raw := out + "/" + name + ".cc.raw"
		ts := out + "/" + name + ".cc.ts"
		// no literal % anywhere: tmux format-expands the pipe command and
		// eats percent signs, so the printf format is built from chr(37)
		pipe := fmt.Sprintf(
			`perl -MTime::HiRes=time -e 'my $f=chr(37).".4f ".chr(37)."d\n";open my $r,">","%s";open my $t,">","%s";binmode $r;my $b;while(sysread(STDIN,$b,65536)){printf $t $f,time,length $b;print $r $b}'`,
			raw, ts)
		r.T("pipe-pane", "-o", "-t", pane, pipe)
		sleep(200)
		r.StartRecord()
		act()
		sleep(900)
		recc := r.StopRecordT()
		var rec []byte
		last := time.Duration(0)
		for _, c := range recc {
			rec = append(rec, c.Data...)
			last = c.At
		}
		cli := frames(rec)
		os.WriteFile(out+"/"+name+".cli.raw", rec, 0o644)
		r.T("pipe-pane", "-t", pane) // stop piping (flushes on EOF)
		sleep(200)
		b, _ := os.ReadFile(raw)
		t.Logf("%-30s: client=%4d frames, settled=%7v, cc-writes=%4d syncs, cc-bytes=%7d",
			name, cli, last.Round(time.Millisecond), frames(b), len(b))
	}
	// open-shape cases start wide+focused
	openCase := func(name string, act func()) {
		r.T("select-pane", "-t", pane)
		r.T("resize-pane", "-t", pane, "-x", wide)
		sleep(800)
		capture(name, act)
	}

	openCase("resize-alone", func() {
		r.T("resize-pane", "-t", pane, "-x", narrow)
	})
	openCase("focus-out-alone", func() {
		r.T("select-pane", "-t", other)
	})
	openCase("resize+focus-out-same-batch", func() {
		r.T("resize-pane", "-t", pane, "-x", narrow, ";", "select-pane", "-t", other)
	})
	openCase("resize+focus-out+50ms", func() {
		r.T("resize-pane", "-t", pane, "-x", narrow)
		sleep(50)
		r.T("select-pane", "-t", other)
	})

	// close shape: start narrow+unfocused, then widen+focus-in same batch
	r.T("select-pane", "-t", other)
	r.T("resize-pane", "-t", pane, "-x", narrow)
	sleep(800)
	capture("widen+focus-in-same-batch", func() {
		r.T("resize-pane", "-t", pane, "-x", wide, ";", "select-pane", "-t", pane)
	})
}

// TestCCStormVerbose reproduces only the same-batch storm; run with
// RIGVV=<dir> to collect tmux's own -vv server log for that window.
func TestCCStormVerbose(t *testing.T) {
	cc := os.Getenv("CCBIN")
	if cc == "" {
		t.Skip("set CCBIN=/path/to/claude")
	}
	r := NewLive(t)
	r.T("set-option", "-g", "focus-events", "on")
	r.T("respawn-pane", "-k", "-t", "play:0.0", cc)
	r.T("split-window", "-h", "-t", "play:0")
	sleep(6000)
	pane := r.T("display-message", "-p", "-t", "play:0.0", "#{pane_id}")
	other := r.T("display-message", "-p", "-t", "play:0.1", "#{pane_id}")
	r.T("switch-client", "-c", r.CL, "-t", "play", ";", "select-window", "-t", r.P1)
	sleep(800)
	w, _ := strconv.Atoi(r.T("display-message", "-p", "-t", pane, "#{pane_width}"))
	r.T("select-pane", "-t", pane)
	sleep(800)
	r.T("display-message", "-p", "STORMMARK-BEGIN")
	r.StartRecord()
	r.T("resize-pane", "-t", pane, "-x", strconv.Itoa(w-45), ";", "select-pane", "-t", other)
	sleep(900)
	n := bytes.Count(r.StopRecord(), []byte("\x1b[?2026h"))
	r.T("display-message", "-p", "STORMMARK-END")
	t.Logf("storm frames: %d (pane=%s other=%s)", n, pane, other)
}

// TestCCStormNoDaemon: the same-batch collision with NO control client
// attached (daemon killed). If the storm needs the winch daemon's control
// channel to stretch presentation, this stays clean.
func TestCCStormNoDaemon(t *testing.T) {
	cc := os.Getenv("CCBIN")
	if cc == "" {
		t.Skip("set CCBIN=/path/to/claude")
	}
	r := NewLive(t)
	r.T("set-option", "-g", "focus-events", "on")
	r.T("respawn-pane", "-k", "-t", "play:0.0", cc)
	r.T("split-window", "-h", "-t", "play:0")
	sleep(6000)
	pane := r.T("display-message", "-p", "-t", "play:0.0", "#{pane_id}")
	other := r.T("display-message", "-p", "-t", "play:0.1", "#{pane_id}")
	r.T("switch-client", "-c", r.CL, "-t", "play", ";", "select-window", "-t", r.P1)
	sleep(800)
	r.KillDaemon()
	sleep(500)
	t.Logf("clients: %s", r.T("list-clients", "-F", "#{client_name} #{client_flags}"))
	w, _ := strconv.Atoi(r.T("display-message", "-p", "-t", pane, "#{pane_width}"))
	r.T("select-pane", "-t", pane)
	sleep(800)
	r.StartRecord()
	r.T("resize-pane", "-t", pane, "-x", strconv.Itoa(w-45), ";", "select-pane", "-t", other)
	sleep(900)
	chunks := r.StopRecordT()
	n, total := 0, 0
	for _, c := range chunks {
		n += bytes.Count(c.Data, []byte("\x1b[?2026h"))
		total += len(c.Data)
	}
	t.Logf("no-daemon storm frames: %d, bytes=%d, chunks=%d", n, total, len(chunks))
	// time profile: first/last chunk, and arrival of any large chunk
	if len(chunks) > 0 {
		t.Logf("first=%v last=%v", chunks[0].At, chunks[len(chunks)-1].At)
		for i, c := range chunks {
			if len(c.Data) > 400 {
				t.Logf("  big chunk #%d at %v: %d bytes", i, c.At, len(c.Data))
			}
		}
	}
}

// TestCCStormFake replays the same-batch collision against a fake CC that
// emits its whole ~4.7KB repaint as ONE syswrite. If the client still sees
// hundreds of frames, the fragmentation is tmux/kernel-side, not the app.
func TestCCStormFake(t *testing.T) {
	fake := os.Getenv("FAKEBIN")
	if fake == "" {
		t.Skip("set FAKEBIN='perl /path/fakecc2.pl'")
	}
	out := os.Getenv("ANATDIR")
	if out == "" {
		out = t.TempDir()
	}
	r := NewLive(t)
	if os.Getenv("FAKE_FOCUS_EVENTS_OFF") != "" {
		r.T("set-option", "-g", "focus-events", "off")
	} else {
		r.T("set-option", "-g", "focus-events", "on")
	}
	r.T("respawn-pane", "-k", "-t", "play:0.0", fake)
	r.T("split-window", "-h", "-t", "play:0")
	sleep(2000)
	pane := r.T("display-message", "-p", "-t", "play:0.0", "#{pane_id}")
	other := r.T("display-message", "-p", "-t", "play:0.1", "#{pane_id}")
	r.T("switch-client", "-c", r.CL, "-t", "play", ";", "select-window", "-t", r.P1)
	sleep(800)
	w, _ := strconv.Atoi(r.T("display-message", "-p", "-t", pane, "#{pane_width}"))
	r.T("select-pane", "-t", pane)
	sleep(800)
	raw := out + "/fake-storm.cc.raw"
	ts := out + "/fake-storm.cc.ts"
	pipe := fmt.Sprintf(
		`perl -MTime::HiRes=time -e 'my $f=chr(37).".4f ".chr(37)."d\n";open my $r,">","%s";open my $t,">","%s";binmode $r;my $b;while(sysread(STDIN,$b,65536)){printf $t $f,time,length $b;print $r $b}'`,
		raw, ts)
	r.T("pipe-pane", "-o", "-t", pane, pipe)
	sleep(200)
	r.StartRecord()
	if os.Getenv("FAKE_RESIZE_ONLY") != "" {
		r.T("resize-pane", "-t", pane, "-x", strconv.Itoa(w-45))
	} else {
		r.T("resize-pane", "-t", pane, "-x", strconv.Itoa(w-45), ";", "select-pane", "-t", other)
	}
	sleep(900)
	chunks := r.StopRecordT()
	r.T("pipe-pane", "-t", pane)
	sleep(200)
	n := 0
	for _, c := range chunks {
		n += bytes.Count(c.Data, []byte("\x1b[?2026h"))
	}
	b, _ := os.ReadFile(raw)
	t.Logf("fake single-write storm: client=%d frames, cc-bytes=%d (%s)", n, len(b), ts)
	if len(chunks) > 0 {
		t.Logf("first=%v last=%v", chunks[0].At, chunks[len(chunks)-1].At)
	}
}
