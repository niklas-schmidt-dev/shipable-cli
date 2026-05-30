//go:build tui

package shipablecli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// maxLogLine bounds the line buffer of lineWriter so a producer that never
// emits a newline cannot grow memory without limit; the partial line is
// flushed once it crosses the threshold.
const maxLogLine = 64 * 1024

// runUI launches the interactive terminal UI. It is the only command that
// consults a terminal: it requires stdin, stdout, and stderr to all be TTYs
// and refuses to start under CI/dumb-terminal/SHIPABLE_NO_TUI so it can never
// dump escape codes into an automation pipeline. Bubble Tea renders to stderr,
// keeping stdout reserved for machine output across the rest of the CLI.
func (r runner) runUI(_ []string) error {
	inFile, _ := r.stdin.(*os.File)
	outFile, _ := r.stdout.(*os.File)
	errFile, _ := r.stderr.(*os.File)
	if inFile == nil || outFile == nil || errFile == nil ||
		!term.IsTerminal(int(inFile.Fd())) ||
		!term.IsTerminal(int(outFile.Fd())) ||
		!term.IsTerminal(int(errFile.Fd())) {
		return errors.New("shipable ui requires an interactive terminal (stdin, stdout, and stderr must all be a TTY)")
	}
	if reason := tuiDisabledReason(r); reason != "" {
		return fmt.Errorf("shipable ui is disabled: %s", reason)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan Event, 256)
	done := make(chan struct{})

	// Quiet engine clone: domain methods write to io.Discard (the TUI renders
	// its own view from returned structs and Events) and report progress to the
	// Bubble Tea program via the channel-backed reporter. The env is seeded from
	// the OS as a private, mutable map so the backend switcher can override
	// SHIPABLE_API_URL / SHIPABLE_CONFIG per environment without touching the
	// process environment (and without losing the other vars getenv reads).
	engine := r
	engine.stdout = io.Discard
	engine.stderr = io.Discard
	engine.report = teaReporter{ch: events, done: done}
	engine.env = osEnvMap()

	program := tea.NewProgram(
		newModel(engine, ctx, cancel, events, done),
		tea.WithAltScreen(),
		tea.WithInput(inFile),
		tea.WithOutput(errFile),
	)
	_, err := program.Run()
	// Release any engine goroutine blocked emitting an event, and cancel any
	// in-flight poll/stream so nothing outlives the UI.
	cancel()
	close(done)
	return err
}

// tuiDisabledReason returns a non-empty reason if the environment forbids the
// TUI even on a real terminal.
func tuiDisabledReason(r runner) string {
	if isTruthyEnv(r.getenv("SHIPABLE_NO_TUI")) {
		return "SHIPABLE_NO_TUI is set"
	}
	if isTruthyEnv(r.getenv("CI")) {
		return "running in CI"
	}
	if strings.EqualFold(strings.TrimSpace(r.getenv("TERM")), "dumb") {
		return "TERM is dumb"
	}
	return ""
}

func isTruthyEnv(value string) bool {
	v := strings.TrimSpace(value)
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// teaReporter forwards engine Events into the Bubble Tea program through a
// buffered channel. The select on done ensures an engine goroutine blocked on
// a full channel is released when the program quits, instead of leaking.
type teaReporter struct {
	ch   chan Event
	done chan struct{}
}

func (t teaReporter) Emit(e Event) {
	select {
	case t.ch <- e:
	case <-t.done:
	}
}

// osEnvMap snapshots the process environment into a private, mutable map. The
// TUI uses it as the engine's env so backend switching can override specific
// keys while every other variable getenv reads still resolves.
func osEnvMap() map[string]string {
	environ := os.Environ()
	out := make(map[string]string, len(environ))
	for _, entry := range environ {
		if i := strings.IndexByte(entry, '='); i > 0 {
			out[entry[:i]] = entry[i+1:]
		}
	}
	return out
}

// waitForEvent yields the next engine Event as a tea.Msg. The model re-arms it
// after each event so the channel is drained continuously.
func waitForEvent(ch chan Event, done chan struct{}) tea.Cmd {
	return func() tea.Msg {
		select {
		case e := <-ch:
			return eventMsg(e)
		case <-done:
			return nil
		}
	}
}

// lineWriter splits a byte stream into newline-delimited lines and hands each
// to emit. It is the bridge between apiStream's raw io.Copy and the log
// viewport. The buffer is bounded by maxLogLine.
type lineWriter struct {
	emit func(string)
	buf  []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		w.emit(line)
	}
	if len(w.buf) > maxLogLine {
		w.emit(string(w.buf))
		w.buf = w.buf[:0]
	}
	return len(p), nil
}

// Message types delivered to the model's Update loop.
type (
	eventMsg  Event
	errMsg    struct{ err error }
	loadedMsg struct {
		cfg       configFile
		authed    bool
		projectID string
		err       error
	}
	statusLoadedMsg    struct{ status statusReport }
	deployDoneMsg      struct{ target string }
	syncDoneMsg        struct{ res syncResult }
	genDoneMsg         struct{ run generationRunInfo }
	templatesLoadedMsg struct{ templates []projectTemplate }
	createdMsg         struct {
		project projectInfo
		dir     string
	}
	deviceStartedMsg  struct{ flow deviceFlow }
	loginDoneMsg      struct{ cfg configFile }
	logStreamEndedMsg struct{ err error }
)
