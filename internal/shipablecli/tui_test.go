//go:build tui

package shipablecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newTestModel() model {
	engine := runner{
		stdout: io.Discard,
		stderr: io.Discard,
		env:    map[string]string{},
		report: discardReporter{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	return newModel(engine, ctx, cancel, make(chan Event, 8), make(chan struct{}))
}

func step(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	next, _ := m.Update(msg)
	updated, ok := next.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", next)
	}
	return updated
}

func TestTUIInitialModelIsLoading(t *testing.T) {
	m := newTestModel()
	if m.screen != screenLoading {
		t.Fatalf("initial screen = %d, want screenLoading", m.screen)
	}
	if strings.TrimSpace(m.View()) == "" {
		t.Fatal("initial View() is empty")
	}
}

func TestTUIUnauthenticatedGoesToAuthScreen(t *testing.T) {
	m := step(t, newTestModel(), loadedMsg{authed: false, err: errors.New("not authenticated")})
	if m.screen != screenAuth {
		t.Fatalf("screen = %d, want screenAuth", m.screen)
	}
	if m.authed {
		t.Fatal("authed should be false")
	}
	if !strings.Contains(m.View(), "not authenticated") {
		t.Fatalf("auth view missing hint: %q", m.View())
	}
}

func TestTUIAuthedWithoutProjectGoesToProjectPicker(t *testing.T) {
	m := step(t, newTestModel(), loadedMsg{authed: true, cfg: configFile{APIURL: "https://api.test", AccessToken: "t"}})
	if m.screen != screenProjectPick {
		t.Fatalf("screen = %d, want screenProjectPick", m.screen)
	}
	if !m.authed {
		t.Fatal("authed should be true")
	}
}

func TestTUIAuthedWithProjectLoadsStatus(t *testing.T) {
	m := newTestModel()
	next, cmd := m.Update(loadedMsg{authed: true, projectID: "proj_1", cfg: configFile{APIURL: "https://api.test", AccessToken: "t"}})
	m = next.(model)
	if m.screen != screenStatus {
		t.Fatalf("screen = %d, want screenStatus", m.screen)
	}
	if m.projectID != "proj_1" {
		t.Fatalf("projectID = %q", m.projectID)
	}
	if !m.busy {
		t.Fatal("should be busy loading status")
	}
	if cmd == nil {
		t.Fatal("expected a fetch-status command")
	}
}

func TestTUIStatusViewRendersEndpoints(t *testing.T) {
	m := newTestModel()
	m.authed = true
	m.projectID = "proj_1"
	status := statusReport{
		Project:   projectInfo{ID: "proj_1", Name: "Demo", Status: "active", LatestVersionID: "ver_9"},
		Readiness: readinessInfo{Status: "blocked", Blockers: []readinessBlockerInfo{{Code: "env", Message: "missing var", Action: "set FOO"}}},
		Preview:   deploymentInfo{Status: "ready", URL: "https://preview.test"},
	}
	m = step(t, m, statusLoadedMsg{status: status})
	if m.screen != screenStatus || m.busy {
		t.Fatalf("screen=%d busy=%v after status", m.screen, m.busy)
	}
	view := m.View()
	for _, want := range []string{"Demo", "ver_9", "missing var", "set FOO", "https://preview.test"} {
		if !strings.Contains(view, want) {
			t.Fatalf("status view missing %q:\n%s", want, view)
		}
	}
}

func TestTUITemplateNavigationAndSelection(t *testing.T) {
	m := newTestModel()
	m.authed = true
	m = step(t, m, templatesLoadedMsg{templates: []projectTemplate{{ID: "a"}, {ID: "b"}, {ID: "c"}}})
	if m.screen != screenTemplates {
		t.Fatalf("screen = %d, want screenTemplates", m.screen)
	}
	m = step(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = step(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.templateIdx != 2 {
		t.Fatalf("templateIdx = %d, want 2", m.templateIdx)
	}
	m = step(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.templateIdx != 1 {
		t.Fatalf("templateIdx = %d, want 1", m.templateIdx)
	}
	m = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenCreateName {
		t.Fatalf("screen = %d, want screenCreateName", m.screen)
	}
}

func TestTUITokenLoginFlow(t *testing.T) {
	m := step(t, newTestModel(), loadedMsg{authed: false, err: errors.New("not authenticated")})
	if m.screen != screenAuth {
		t.Fatalf("screen = %d, want screenAuth", m.screen)
	}
	// 't' opens the token entry screen.
	m = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if m.screen != screenTokenLogin {
		t.Fatalf("screen = %d, want screenTokenLogin", m.screen)
	}
	// Empty submit is rejected.
	empty := step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if empty.errText == "" {
		t.Fatal("expected an error submitting an empty token")
	}
	// Typing a token then submitting kicks off the login command.
	m = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tok_abc123")})
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if !m.busy {
		t.Fatal("expected busy after submitting a token")
	}
	if cmd == nil {
		t.Fatal("expected a token-login command")
	}
}

func TestTUIEnvironmentsDefaultToOfficial(t *testing.T) {
	envs := buildEnvironments(func(string) string { return "" }, "/cfg/config.json")
	if len(envs) != 2 {
		t.Fatalf("environments = %d, want 2", len(envs))
	}
	if envs[0].name != "official" || envs[0].apiOverride != defaultAPIURL {
		t.Fatalf("official env = %+v (defaultAPIURL=%s)", envs[0], defaultAPIURL)
	}
	if envs[0].configPath != "/cfg/config.json" {
		t.Fatalf("official config path = %q", envs[0].configPath)
	}
	if envs[1].name != "local" || envs[1].apiOverride != defaultLocalAPIURL {
		t.Fatalf("local env = %+v", envs[1])
	}
	if envs[1].configPath != "/cfg/config-local.json" {
		t.Fatalf("local config path = %q", envs[1].configPath)
	}
	if defaultAPIURL != "https://api.shipable.de" {
		t.Fatalf("default endpoint = %q, want https://api.shipable.de", defaultAPIURL)
	}
	// No SHIPABLE_API_URL -> official (index 0); explicit local URL -> local.
	if i := initialEnvIndex(envs, func(string) string { return "" }); i != 0 {
		t.Fatalf("initial index = %d, want 0 (official)", i)
	}
	if i := initialEnvIndex(envs, func(k string) string {
		if k == "SHIPABLE_API_URL" {
			return defaultLocalAPIURL
		}
		return ""
	}); i != 1 {
		t.Fatalf("initial index = %d, want 1 (local)", i)
	}
}

func TestTUISwitchBackendReloads(t *testing.T) {
	m := newTestModel()
	if m.envIdx != 0 {
		t.Fatalf("start env index = %d, want 0", m.envIdx)
	}
	m.screen = screenStatus
	m.hasStatus = true
	m.busy = false
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = next.(model)
	if m.envIdx != 1 {
		t.Fatalf("after switch env index = %d, want 1 (local)", m.envIdx)
	}
	if m.engine.env["SHIPABLE_API_URL"] != defaultLocalAPIURL {
		t.Fatalf("engine SHIPABLE_API_URL = %q, want %q", m.engine.env["SHIPABLE_API_URL"], defaultLocalAPIURL)
	}
	if m.screen != screenLoading || !m.busy {
		t.Fatalf("expected reload state, got screen=%d busy=%v", m.screen, m.busy)
	}
	if cmd == nil {
		t.Fatal("expected a reload command after switching backend")
	}
}

func TestTUIViewNeverPanicsAcrossScreens(t *testing.T) {
	screens := []screen{
		screenLoading, screenAuth, screenDeviceLogin, screenTokenLogin, screenProjectPick,
		screenStatus, screenGenerate, screenTemplates, screenCreateName, screenLogs,
	}
	for _, s := range screens {
		m := newTestModel()
		m.screen = s
		m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
		_ = m.View() // must not panic
	}
}

func TestTUIDisabledReason(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want bool // disabled?
	}{
		{map[string]string{}, false},
		{map[string]string{"TERM": "xterm-256color"}, false},
		{map[string]string{"SHIPABLE_NO_TUI": "1"}, true},
		{map[string]string{"SHIPABLE_NO_TUI": "0"}, false},
		{map[string]string{"CI": "true"}, true},
		{map[string]string{"CI": "false"}, false},
		{map[string]string{"TERM": "dumb"}, true},
	}
	for _, tc := range cases {
		r := runner{env: tc.env}
		got := tuiDisabledReason(r) != ""
		if got != tc.want {
			t.Errorf("tuiDisabledReason(%v) disabled=%v, want %v", tc.env, got, tc.want)
		}
	}
}

func TestLineWriterSplitsLines(t *testing.T) {
	var got []string
	w := &lineWriter{emit: func(line string) { got = append(got, line) }}
	_, _ = w.Write([]byte("alpha\nbeta\r\npar"))
	_, _ = w.Write([]byte("tial\ngamma\n"))
	want := []string{"alpha", "beta", "partial", "gamma"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

func TestTUILogBufferBoundedWithTruncationNotice(t *testing.T) {
	m := newTestModel()
	for i := 0; i < maxLogLines+50; i++ {
		m.appendLog(fmt.Sprintf("line-%d", i))
	}
	if len(m.logLines) > maxLogLines {
		t.Fatalf("log buffer = %d lines, want <= %d", len(m.logLines), maxLogLines)
	}
	if m.logLines[0] != logTruncationNotice {
		t.Fatalf("first line = %q, want truncation notice", m.logLines[0])
	}
	last := m.logLines[len(m.logLines)-1]
	if last != fmt.Sprintf("line-%d", maxLogLines+49) {
		t.Fatalf("last line = %q, want most recent", last)
	}
}

func TestFormatDeploymentLinesEmptyWhenNoEndpoints(t *testing.T) {
	if lines := formatDeploymentLines("Preview", deploymentInfo{}); len(lines) != 0 {
		t.Fatalf("expected no lines for empty deployment, got %v", lines)
	}
	lines := formatDeploymentLines("Preview", deploymentInfo{Services: []deploymentServiceInfo{{ComponentID: "web", Status: "ready", URL: "https://x.test"}}})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "web") || !strings.Contains(joined, "https://x.test") {
		t.Fatalf("service lines missing content: %q", joined)
	}
}
