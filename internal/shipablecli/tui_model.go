//go:build tui

package shipablecli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type screen int

const (
	screenLoading screen = iota
	screenAuth
	screenDeviceLogin
	screenTokenLogin
	screenProjectPick
	screenStatus
	screenGenerate
	screenTemplates
	screenCreateName
	screenLogs
)

const maxLogLines = 5000

// logTruncationNotice is kept as the first line once the log buffer overflows,
// so the user is never silently shown a window that started mid-stream.
const logTruncationNotice = "… earlier log lines truncated …"

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	groupStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("250"))
	labelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	okStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	helpStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	spinnerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	boxStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("63")).Padding(0, 2)
)

// tuiEnv is a selectable backend the developer can switch between in the TUI.
// apiOverride is the value forced into SHIPABLE_API_URL ("" leaves the URL to
// the normal config-file/default resolution); configPath isolates each
// backend's stored token so switching does not clobber the other's auth.
type tuiEnv struct {
	name        string
	displayURL  string
	apiOverride string
	configPath  string
}

// buildEnvironments returns the backends offered in the TUI. "official" is the
// default (production, https://api.shipable.de unless overridden) and uses the
// shared config file; "local" is the dev backend (http://localhost:8080 unless
// overridden) with its own config file so the two tokens never collide.
func buildEnvironments(getenv func(string) string, baseConfigPath string) []tuiEnv {
	officialURL := firstNonEmpty(getenv("SHIPABLE_OFFICIAL_API_URL"), officialAPIURL, defaultAPIURL)
	localURL := firstNonEmpty(getenv("SHIPABLE_LOCAL_API_URL"), defaultLocalAPIURL)
	return []tuiEnv{
		{
			name:        "official",
			displayURL:  officialURL,
			apiOverride: officialURL,
			configPath:  baseConfigPath,
		},
		{
			name:        "local",
			displayURL:  localURL,
			apiOverride: localURL,
			configPath:  siblingConfigPath(baseConfigPath, "local"),
		},
	}
}

// initialEnvIndex starts on the backend matching an explicit SHIPABLE_API_URL
// (so a developer who exported the local URL lands on local), otherwise on the
// default (official) backend.
func initialEnvIndex(envs []tuiEnv, getenv func(string) string) int {
	want := strings.TrimRight(strings.TrimSpace(getenv("SHIPABLE_API_URL")), "/")
	if want == "" {
		return 0
	}
	for i, e := range envs {
		if strings.TrimRight(e.apiOverride, "/") == want {
			return i
		}
	}
	return 0
}

// siblingConfigPath derives a per-environment config file next to the base one,
// e.g. ".../config.json" + "official" -> ".../config-official.json".
func siblingConfigPath(base, suffix string) string {
	dir := filepath.Dir(base)
	name := filepath.Base(base)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	return filepath.Join(dir, stem+"-"+suffix+ext)
}

// model is the root Bubble Tea model. engine is a quiet runner clone whose
// long-running methods report progress as Events over m.events. Config is
// loaded once (afterAuth/initialLoad) and passed into every Cmd, so concurrent
// Cmds never race on a token refresh.
type model struct {
	engine runner
	ctx    context.Context
	cancel context.CancelFunc
	events chan Event
	done   chan struct{}

	screen screen
	width  int
	height int

	cfg       configFile
	authed    bool
	projectID string

	environments []tuiEnv
	envIdx       int

	status    statusReport
	hasStatus bool

	templates   []projectTemplate
	templateIdx int

	deviceFlow *deviceFlow
	logLines   []string
	logCancel  context.CancelFunc

	spinner      spinner.Model
	viewport     viewport.Model
	vpReady      bool
	projectInput textinput.Model
	promptInput  textinput.Model
	nameInput    textinput.Model
	tokenInput   textinput.Model

	busy    bool
	busyMsg string
	info    string
	errText string
}

func newModel(engine runner, ctx context.Context, cancel context.CancelFunc, events chan Event, done chan struct{}) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = spinnerStyle

	project := textinput.New()
	project.Placeholder = "proj_…"
	project.CharLimit = 128
	project.Width = 48

	prompt := textinput.New()
	prompt.Placeholder = "describe the change to generate…"
	prompt.CharLimit = 2000
	prompt.Width = 60

	name := textinput.New()
	name.Placeholder = "project name"
	name.CharLimit = 80
	name.Width = 40

	token := textinput.New()
	token.Placeholder = "paste access token…"
	token.EchoMode = textinput.EchoPassword
	token.CharLimit = 8192
	token.Width = 56

	environments := buildEnvironments(engine.getenv, engine.configPath())
	envIdx := initialEnvIndex(environments, engine.getenv)

	m := model{
		engine:       engine,
		ctx:          ctx,
		cancel:       cancel,
		events:       events,
		done:         done,
		screen:       screenLoading,
		busy:         true,
		busyMsg:      "Loading…",
		spinner:      sp,
		projectInput: project,
		promptInput:  prompt,
		nameInput:    name,
		tokenInput:   token,
		environments: environments,
		envIdx:       envIdx,
	}
	m.applyEnv(environments[envIdx])
	return m
}

// applyEnv points the engine at the given backend: it forces the API URL (when
// the environment specifies one) and isolates that backend's stored token.
func (m *model) applyEnv(e tuiEnv) {
	m.engine.env["SHIPABLE_API_URL"] = e.apiOverride
	m.engine.env["SHIPABLE_CONFIG"] = e.configPath
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.initialLoadCmd(), waitForEvent(m.events, m.done))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureViewport()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			m.cancel()
			return m, tea.Quit
		}
		return m.handleKey(msg)

	case eventMsg:
		return m.handleEvent(Event(msg))

	case errMsg:
		m.busy = false
		m.busyMsg = ""
		m.errText = msg.err.Error()
		return m, nil

	case loadedMsg:
		if msg.err != nil || !msg.authed {
			m.authed = false
			m.busy = false
			m.busyMsg = ""
			m.screen = screenAuth
			if msg.err != nil {
				m.errText = "not authenticated: " + msg.err.Error()
			}
			return m, nil
		}
		m.cfg = msg.cfg
		m.authed = true
		m.projectID = msg.projectID
		return m.afterAuth()

	case statusLoadedMsg:
		m.status = msg.status
		m.hasStatus = true
		m.busy = false
		m.busyMsg = ""
		m.errText = ""
		if m.screen == screenLoading {
			m.screen = screenStatus
		}
		return m, nil

	case deployDoneMsg:
		m.info = fmt.Sprintf("Deploy to %s finished", msg.target)
		m.busy = true
		m.busyMsg = "Refreshing status…"
		return m, m.fetchStatusCmd()

	case syncDoneMsg:
		m.busy = false
		m.busyMsg = ""
		m.info = fmt.Sprintf("Synced %d files as version %s", msg.res.Uploaded, msg.res.VersionID)
		return m, nil

	case genDoneMsg:
		m.info = fmt.Sprintf("Generation %s %s", msg.run.ID, msg.run.Status)
		m.busy = true
		m.busyMsg = "Refreshing status…"
		return m, m.fetchStatusCmd()

	case templatesLoadedMsg:
		m.templates = groupTemplates(msg.templates)
		m.templateIdx = 0
		m.busy = false
		m.busyMsg = ""
		m.screen = screenTemplates
		return m, nil

	case createdMsg:
		m.projectID = msg.project.ID
		m.info = fmt.Sprintf("Created %s (%s)", firstNonEmpty(msg.project.Name, msg.project.ID), msg.project.ID)
		m.screen = screenStatus
		m.busy = true
		m.busyMsg = "Loading status…"
		return m, m.fetchStatusCmd()

	case deviceStartedMsg:
		flow := msg.flow
		m.deviceFlow = &flow
		m.screen = screenDeviceLogin
		m.busy = true
		m.busyMsg = "Waiting for browser approval…"
		return m, m.completeDeviceCmd(flow)

	case loginDoneMsg:
		m.cfg = msg.cfg
		m.authed = true
		m.deviceFlow = nil
		m.info = "Authenticated"
		if cwd, err := os.Getwd(); err == nil {
			if link, err := readProjectLink(cwd); err == nil {
				m.projectID = link.ProjectID
			}
		}
		return m.afterAuth()

	case logStreamEndedMsg:
		m.busy = false
		m.busyMsg = ""
		if msg.err != nil && m.ctx.Err() == nil && (m.logCancel == nil || m.screen == screenLogs) {
			m.info = "log stream ended: " + msg.err.Error()
		}
		// The stream goroutine has returned, so its child context is done; drop
		// the stale cancel so re-opening logs cannot overwrite a live one.
		m.logCancel = nil
		return m, nil
	}

	return m.forwardToActive(msg)
}

// canSwitchEnv reports whether the backend switcher is usable right now: only
// on non-text screens, and never mid-operation (so an in-flight deploy/login
// isn't orphaned against the old backend).
func (m model) canSwitchEnv() bool {
	if len(m.environments) < 2 || m.busy {
		return false
	}
	switch m.screen {
	case screenStatus, screenAuth, screenLoading:
		return true
	}
	return false
}

// switchEnvironment cycles to the next backend, re-points the engine, and
// reloads auth/status against it.
func (m model) switchEnvironment() (tea.Model, tea.Cmd) {
	m.envIdx = (m.envIdx + 1) % len(m.environments)
	env := m.environments[m.envIdx]
	m.applyEnv(env)
	if m.logCancel != nil {
		m.logCancel()
		m.logCancel = nil
	}
	m.authed = false
	m.projectID = ""
	m.hasStatus = false
	m.status = statusReport{}
	m.errText = ""
	m.info = "switched to " + env.name + " backend"
	m.screen = screenLoading
	m.busy = true
	m.busyMsg = "Loading " + env.name + " backend…"
	return m, m.initialLoadCmd()
}

// afterAuth routes to the status dashboard when a project is linked, otherwise
// to the project picker.
func (m model) afterAuth() (tea.Model, tea.Cmd) {
	if strings.TrimSpace(m.projectID) == "" {
		m.screen = screenProjectPick
		m.busy = false
		m.busyMsg = ""
		m.projectInput.Focus()
		return m, textinput.Blink
	}
	m.screen = screenStatus
	m.busy = true
	m.busyMsg = "Loading status…"
	return m, m.fetchStatusCmd()
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	// Backend switch is available on non-text screens that aren't mid-operation.
	if key == "e" && m.canSwitchEnv() {
		return m.switchEnvironment()
	}
	switch m.screen {
	case screenLoading:
		if key == "q" {
			m.cancel()
			return m, tea.Quit
		}
		return m, nil

	case screenAuth:
		switch key {
		case "q":
			m.cancel()
			return m, tea.Quit
		case "l", "enter":
			m.errText = ""
			m.busy = true
			m.busyMsg = "Requesting device code…"
			return m, m.startDeviceCmd()
		case "t":
			m.errText = ""
			m.tokenInput.SetValue("")
			m.tokenInput.Focus()
			m.screen = screenTokenLogin
			return m, textinput.Blink
		}
		return m, nil

	case screenDeviceLogin:
		if key == "q" {
			m.cancel()
			return m, tea.Quit
		}
		return m, nil

	case screenTokenLogin:
		switch key {
		case "esc":
			m.tokenInput.Blur()
			m.screen = screenAuth
			return m, nil
		case "enter":
			token := strings.TrimSpace(m.tokenInput.Value())
			if token == "" {
				m.errText = "paste an access token"
				return m, nil
			}
			m.errText = ""
			m.tokenInput.Blur()
			m.busy = true
			m.busyMsg = "Saving token…"
			return m, m.tokenLoginCmd(token)
		}
		var cmd tea.Cmd
		m.tokenInput, cmd = m.tokenInput.Update(msg)
		return m, cmd

	case screenProjectPick:
		switch key {
		case "esc":
			m.cancel()
			return m, tea.Quit
		case "enter":
			value := strings.TrimSpace(m.projectInput.Value())
			if value == "" {
				m.errText = "enter a project id"
				return m, nil
			}
			m.projectID = value
			m.errText = ""
			m.projectInput.Blur()
			m.screen = screenStatus
			m.busy = true
			m.busyMsg = "Loading status…"
			return m, m.fetchStatusCmd()
		case "ctrl+t":
			m.errText = ""
			m.busy = true
			m.busyMsg = "Loading templates…"
			return m, m.fetchTemplatesCmd()
		}
		var cmd tea.Cmd
		m.projectInput, cmd = m.projectInput.Update(msg)
		return m, cmd

	case screenStatus:
		if m.busy {
			if key == "q" {
				m.cancel()
				return m, tea.Quit
			}
			return m, nil
		}
		switch key {
		case "q":
			m.cancel()
			return m, tea.Quit
		case "r":
			m.errText, m.info = "", ""
			m.busy = true
			m.busyMsg = "Refreshing…"
			return m, m.fetchStatusCmd()
		case "d":
			m.errText, m.info = "", ""
			m.busy = true
			m.busyMsg = "Deploying preview…"
			return m, m.deployCmd("preview")
		case "p":
			m.errText, m.info = "", ""
			m.busy = true
			m.busyMsg = "Deploying production…"
			return m, m.deployCmd("production")
		case "s":
			m.errText, m.info = "", ""
			m.busy = true
			m.busyMsg = "Syncing…"
			return m, m.syncCmd()
		case "g":
			m.errText, m.info = "", ""
			m.promptInput.SetValue("")
			m.promptInput.Focus()
			m.screen = screenGenerate
			return m, textinput.Blink
		case "l":
			m.errText, m.info = "", ""
			m.logLines = nil
			m.ensureViewport()
			m.viewport.SetContent("")
			m.screen = screenLogs
			m.busy = true
			m.busyMsg = "Connecting to preview logs…"
			logCtx, cancel := context.WithCancel(m.ctx)
			m.logCancel = cancel
			return m, m.streamLogsCmd(logCtx, "preview")
		case "t":
			m.errText, m.info = "", ""
			m.busy = true
			m.busyMsg = "Loading templates…"
			return m, m.fetchTemplatesCmd()
		}
		return m, nil

	case screenGenerate:
		switch key {
		case "esc":
			m.promptInput.Blur()
			m.screen = screenStatus
			return m, nil
		case "enter":
			prompt := strings.TrimSpace(m.promptInput.Value())
			if prompt == "" {
				m.errText = "enter a prompt"
				return m, nil
			}
			m.errText = ""
			m.promptInput.Blur()
			m.screen = screenStatus
			m.busy = true
			m.busyMsg = "Generating…"
			return m, m.generateCmd(prompt)
		}
		var cmd tea.Cmd
		m.promptInput, cmd = m.promptInput.Update(msg)
		return m, cmd

	case screenTemplates:
		switch key {
		case "q":
			m.cancel()
			return m, tea.Quit
		case "esc":
			if strings.TrimSpace(m.projectID) == "" {
				m.screen = screenProjectPick
			} else {
				m.screen = screenStatus
			}
			return m, nil
		case "up", "k":
			if m.templateIdx > 0 {
				m.templateIdx--
			}
			return m, nil
		case "down", "j":
			if m.templateIdx < len(m.templates)-1 {
				m.templateIdx++
			}
			return m, nil
		case "enter":
			if len(m.templates) == 0 {
				return m, nil
			}
			m.errText = ""
			m.nameInput.SetValue("")
			m.nameInput.Focus()
			m.screen = screenCreateName
			return m, textinput.Blink
		}
		return m, nil

	case screenCreateName:
		switch key {
		case "esc":
			m.nameInput.Blur()
			m.screen = screenTemplates
			return m, nil
		case "enter":
			name := strings.TrimSpace(m.nameInput.Value())
			if name == "" {
				m.errText = "enter a project name"
				return m, nil
			}
			if m.templateIdx >= len(m.templates) {
				m.errText = "no template selected"
				return m, nil
			}
			m.errText = ""
			m.nameInput.Blur()
			m.busy = true
			m.busyMsg = "Creating project…"
			return m, m.createCmd(m.templates[m.templateIdx].ID, name)
		}
		var cmd tea.Cmd
		m.nameInput, cmd = m.nameInput.Update(msg)
		return m, cmd

	case screenLogs:
		switch key {
		case "q":
			m.cancel()
			return m, tea.Quit
		case "esc", "b":
			if m.logCancel != nil {
				m.logCancel()
				m.logCancel = nil
			}
			m.busy = false
			m.busyMsg = ""
			m.screen = screenStatus
			return m, nil
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) handleEvent(e Event) (tea.Model, tea.Cmd) {
	switch e.Kind {
	case EvtDeploymentPolled:
		if m.busy {
			m.busyMsg = fmt.Sprintf("Deploying %s… (%s)", e.Target, firstNonEmpty(e.Status, "pending"))
		}
	case EvtJobPolled:
		if m.busy {
			m.busyMsg = fmt.Sprintf("Building… (%s)", firstNonEmpty(e.Status, "pending"))
		}
	case EvtGenerationPolled:
		if m.busy {
			m.busyMsg = fmt.Sprintf("Generating… (%s)", firstNonEmpty(e.Status, "pending"))
		}
	case EvtLogLine:
		m.busy = false
		m.busyMsg = ""
		m.appendLog(e.Line)
	}
	return m, waitForEvent(m.events, m.done)
}

func (m model) forwardToActive(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.screen {
	case screenProjectPick:
		m.projectInput, cmd = m.projectInput.Update(msg)
	case screenGenerate:
		m.promptInput, cmd = m.promptInput.Update(msg)
	case screenCreateName:
		m.nameInput, cmd = m.nameInput.Update(msg)
	case screenTokenLogin:
		m.tokenInput, cmd = m.tokenInput.Update(msg)
	case screenLogs:
		if m.vpReady {
			m.viewport, cmd = m.viewport.Update(msg)
		}
	}
	return m, cmd
}

func (m *model) ensureViewport() {
	width := m.width
	if width < 20 {
		width = 80
	}
	height := m.height - 6
	if height < 3 {
		height = 3
	}
	if !m.vpReady {
		m.viewport = viewport.New(width, height)
		m.vpReady = true
		return
	}
	m.viewport.Width = width
	m.viewport.Height = height
}

func (m *model) appendLog(line string) {
	m.logLines = append(m.logLines, line)
	if len(m.logLines) > maxLogLines {
		// Keep the most recent lines plus a persistent notice at the top so the
		// user can tell that older output was dropped rather than assuming the
		// stream started here.
		keep := m.logLines[len(m.logLines)-(maxLogLines-1):]
		trimmed := make([]string, 0, maxLogLines)
		trimmed = append(trimmed, logTruncationNotice)
		trimmed = append(trimmed, keep...)
		m.logLines = trimmed
	}
	if m.vpReady {
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom()
	}
}

// ---- Commands (each runs in its own goroutine; all share the root ctx) ----

func (m model) initialLoadCmd() tea.Cmd {
	return func() tea.Msg {
		cfg, err := m.engine.loadConfig()
		if err != nil {
			return loadedMsg{authed: false, err: err}
		}
		projectID := ""
		if cwd, err := os.Getwd(); err == nil {
			if link, err := readProjectLink(cwd); err == nil {
				projectID = link.ProjectID
			}
		}
		return loadedMsg{cfg: cfg, authed: true, projectID: projectID}
	}
}

func (m model) fetchStatusCmd() tea.Cmd {
	cfg, id := m.cfg, m.projectID
	return func() tea.Msg {
		status, err := m.engine.fetchStatus(m.ctx, cfg, id)
		if err != nil {
			return errMsg{err}
		}
		return statusLoadedMsg{status: status}
	}
}

func (m model) deployCmd(target string) tea.Cmd {
	cfg, id := m.cfg, m.projectID
	return func() tea.Msg {
		if _, err := m.engine.queueDeployment(m.ctx, cfg, id, target); err != nil {
			return errMsg{err}
		}
		if target == "preview" {
			if _, err := m.engine.waitForLatestJob(m.ctx, cfg, id); err != nil {
				return errMsg{err}
			}
		}
		if _, err := m.engine.waitForDeployment(m.ctx, cfg, id, target); err != nil {
			return errMsg{err}
		}
		return deployDoneMsg{target: target}
	}
}

func (m model) syncCmd() tea.Cmd {
	cfg, id := m.cfg, m.projectID
	return func() tea.Msg {
		root, err := os.Getwd()
		if err != nil {
			return errMsg{err}
		}
		res, err := m.engine.syncOnce(m.ctx, syncInput{
			Config:    cfg,
			ProjectID: id,
			Root:      root,
			Message:   "tui sync",
			Deploy:    "none",
		})
		if err != nil {
			return errMsg{err}
		}
		return syncDoneMsg{res: res}
	}
}

func (m model) generateCmd(prompt string) tea.Cmd {
	cfg, id := m.cfg, m.projectID
	return func() tea.Msg {
		var run generationRunInfo
		if err := m.engine.apiJSON(m.ctx, cfg, http.MethodPost, "/v1/projects/"+encodeID(id)+"/generations", map[string]any{"prompt": prompt}, &run); err != nil {
			return errMsg{err}
		}
		run, err := m.engine.waitForGeneration(m.ctx, cfg, id, run.ID)
		if err != nil {
			return errMsg{err}
		}
		return genDoneMsg{run: run}
	}
}

func (m model) fetchTemplatesCmd() tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		var templates []projectTemplate
		if err := m.engine.apiJSON(m.ctx, cfg, http.MethodGet, "/v1/project-templates", nil, &templates); err != nil {
			return errMsg{err}
		}
		return templatesLoadedMsg{templates: templates}
	}
}

func (m model) createCmd(templateID, name string) tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		root, err := os.Getwd()
		if err != nil {
			return errMsg{err}
		}
		if err := ensureScaffoldTarget(root, false); err != nil {
			return errMsg{err}
		}
		template, err := m.engine.fetchTemplateFiles(m.ctx, cfg, templateID)
		if err != nil {
			return errMsg{err}
		}
		var project projectInfo
		body := map[string]any{"name": name, "template": templateID, "creationMode": "template"}
		if err := m.engine.apiJSON(m.ctx, cfg, http.MethodPost, "/v1/projects", body, &project); err != nil {
			return errMsg{err}
		}
		if strings.TrimSpace(project.ID) == "" {
			return errMsg{errors.New("create project response did not include id")}
		}
		if err := writeScaffoldFiles(root, template.Files, false); err != nil {
			return errMsg{err}
		}
		if err := writeJSONFile(filepath.Join(root, ".shipable", "project.json"), projectLinkFile{ProjectID: project.ID}, 0o644); err != nil {
			return errMsg{err}
		}
		return createdMsg{project: project, dir: root}
	}
}

func (m model) tokenLoginCmd(token string) tea.Cmd {
	apiURL := m.engine.getenv("SHIPABLE_API_URL")
	return func() tea.Msg {
		cfg, err := m.engine.saveTokenConfig(token, apiURL)
		if err != nil {
			return errMsg{err}
		}
		return loginDoneMsg{cfg: cfg}
	}
}

func (m model) startDeviceCmd() tea.Cmd {
	return func() tea.Msg {
		flow, err := m.engine.startDeviceFlow(m.ctx, deviceLoginInput{APIURL: m.engine.getenv("SHIPABLE_API_URL")})
		if err != nil {
			return errMsg{err}
		}
		return deviceStartedMsg{flow: flow}
	}
}

func (m model) completeDeviceCmd(flow deviceFlow) tea.Cmd {
	return func() tea.Msg {
		cfg, err := m.engine.completeDeviceFlow(m.ctx, flow)
		if err != nil {
			return errMsg{err}
		}
		return loginDoneMsg{cfg: cfg}
	}
}

func (m model) streamLogsCmd(ctx context.Context, target string) tea.Cmd {
	cfg, id := m.cfg, m.projectID
	events, done := m.events, m.done
	return func() tea.Msg {
		dep, err := m.engine.fetchDeployment(ctx, cfg, id, target)
		if err != nil {
			return errMsg{err}
		}
		deploymentID := strings.TrimSpace(dep.DeploymentID)
		if deploymentID == "" {
			return errMsg{fmt.Errorf("no %s deployment to stream logs from", target)}
		}
		component, err := selectServiceLogsComponent(dep, "")
		if err != nil {
			return errMsg{err}
		}
		path := "/v1/projects/" + encodeID(id) + "/deployments/" + encodeID(deploymentID) + "/service-logs"
		query := url.Values{}
		if component != "" {
			query.Set("componentId", component)
		}
		query.Set("follow", "1")
		path += "?" + query.Encode()
		writer := &lineWriter{emit: func(line string) {
			select {
			case events <- Event{Kind: EvtLogLine, Line: line}:
			case <-done:
			}
		}}
		err = m.engine.apiStream(ctx, cfg, path, writer, true)
		return logStreamEndedMsg{err: err}
	}
}

// ---- Views ----

func (m model) View() string {
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteString("\n\n")
	b.WriteString(m.bodyView())
	b.WriteString("\n\n")
	b.WriteString(m.footerView())
	return b.String()
}

func (m model) headerView() string {
	left := titleStyle.Render("⬢ shipable")
	env := ""
	if len(m.environments) > 0 {
		e := m.environments[m.envIdx]
		env = "   " + labelStyle.Render("["+e.name+"] ") + helpStyle.Render(e.displayURL)
	}
	var right string
	if m.authed {
		right = "   " + labelStyle.Render("project ") + firstNonEmpty(m.projectID, "—")
	} else {
		right = "   " + warnStyle.Render("not authenticated")
	}
	return left + env + right
}

func (m model) bodyView() string {
	switch m.screen {
	case screenLoading:
		return m.spinner.View() + " " + m.busyMsg
	case screenAuth:
		return "You are not authenticated.\n\n" +
			helpStyle.Render("Press ") + "l" + helpStyle.Render(" for browser login (WorkOS device flow), or ") + "t" + helpStyle.Render(" to paste an access token.\n") +
			helpStyle.Render("You can also set ") + "SHIPABLE_TOKEN" + helpStyle.Render(" (and ") + "SHIPABLE_API_URL" + helpStyle.Render(") before launching.")
	case screenDeviceLogin:
		return m.deviceLoginView()
	case screenTokenLogin:
		return labelStyle.Render("Paste your Shipable access token:") + "\n\n" + m.tokenInput.View() + "\n\n" +
			helpStyle.Render("Stored in your config — same as ") + "shipable auth login --token-stdin" + helpStyle.Render(".")
	case screenProjectPick:
		return "No linked project in this directory.\n\n" +
			labelStyle.Render("Enter a project id:") + "\n" + m.projectInput.View() + "\n\n" +
			helpStyle.Render("…or press ctrl+t to create one from a template.")
	case screenStatus:
		if !m.hasStatus {
			return m.spinner.View() + " " + firstNonEmpty(m.busyMsg, "Loading…")
		}
		return m.statusView()
	case screenGenerate:
		return labelStyle.Render("Describe the change to generate:") + "\n\n" + m.promptInput.View()
	case screenTemplates:
		return m.templatesView()
	case screenCreateName:
		name := ""
		if m.templateIdx < len(m.templates) {
			t := m.templates[m.templateIdx]
			name = templateFamily(t)
			if t.VariantLabel != "" {
				name += " · " + t.VariantLabel
			}
		}
		return "Create project from template " + titleStyle.Render(name) + "\n\n" +
			labelStyle.Render("Project name:") + "\n" + m.nameInput.View() + "\n\n" +
			helpStyle.Render("Scaffolds into the current directory and links it.")
	case screenLogs:
		if !m.vpReady {
			return m.spinner.View() + " " + m.busyMsg
		}
		return m.viewport.View()
	}
	return ""
}

func (m model) deviceLoginView() string {
	if m.deviceFlow == nil {
		return m.spinner.View() + " Starting login…"
	}
	content := "Open this URL in your browser:\n" +
		titleStyle.Render(m.deviceFlow.deviceVerificationURL()) + "\n\n" +
		labelStyle.Render("Code: ") + titleStyle.Render(m.deviceFlow.device.UserCode)
	return boxStyle.Render(content)
}

func (m model) statusView() string {
	s := m.status
	var lines []string
	lines = append(lines, titleStyle.Render(firstNonEmpty(s.Project.Name, s.Project.ID, "(unknown project)"))+"  "+labelStyle.Render(firstNonEmpty(s.Project.Status, "unknown")))
	if s.Project.LatestVersionID != "" {
		lines = append(lines, labelStyle.Render("latest version  ")+s.Project.LatestVersionID)
	}
	if s.Readiness.Status != "" {
		readiness := "readiness  " + s.Readiness.Status
		if s.Readiness.Phase != "" {
			readiness += " (" + s.Readiness.Phase + ")"
		}
		lines = append(lines, labelStyle.Render(readiness))
		for _, blocker := range s.Readiness.Blockers {
			lines = append(lines, warnStyle.Render("  • "+blocker.Code+": "+blocker.Message))
			if blocker.Action != "" {
				lines = append(lines, helpStyle.Render("    → "+blocker.Action))
			}
		}
	}
	if preview := formatDeploymentLines("Preview", s.Preview); len(preview) > 0 {
		lines = append(lines, "")
		lines = append(lines, preview...)
	}
	if production := formatDeploymentLines("Production", s.Production); len(production) > 0 {
		lines = append(lines, "")
		lines = append(lines, production...)
	}
	return strings.Join(lines, "\n")
}

// groupTemplates reorders templates so variants of the same family (Name) are
// contiguous, preserving the order in which families first appear. This lets the
// picker render one header per family while flat templateIdx still maps to the
// visible row.
func groupTemplates(templates []projectTemplate) []projectTemplate {
	order := map[string]int{}
	for _, t := range templates {
		fam := templateFamily(t)
		if _, ok := order[fam]; !ok {
			order[fam] = len(order)
		}
	}
	sorted := append([]projectTemplate(nil), templates...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return order[templateFamily(sorted[i])] < order[templateFamily(sorted[j])]
	})
	return sorted
}

func templateFamily(t projectTemplate) string {
	return firstNonEmpty(t.Name, t.Category, "Templates")
}

func (m model) templatesView() string {
	if len(m.templates) == 0 {
		return "No templates available."
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	title := labelStyle.Render("Create a project — pick a template")
	detail := m.templateDetail(m.templates[m.templateIdx], width)

	// Build every display row (group headers + variants) and remember which one
	// is selected, so we can scroll a window around it that fits the terminal.
	type row struct {
		text string
		sel  int // index into m.templates, or -1 for headers/spacers
	}
	var rows []row
	selRow := 0
	lastFamily := ""
	for i, t := range m.templates {
		if fam := templateFamily(t); fam != lastFamily {
			if len(rows) > 0 {
				rows = append(rows, row{sel: -1}) // blank between groups
			}
			rows = append(rows, row{text: groupStyle.Render(fam), sel: -1})
			lastFamily = fam
		}
		variant := firstNonEmpty(t.VariantLabel, t.ID)
		if i == m.templateIdx {
			rows = append(rows, row{text: titleStyle.Render("  › " + variant), sel: i})
			selRow = len(rows) - 1
		} else {
			rows = append(rows, row{text: labelStyle.Render("    " + variant), sel: i})
		}
	}

	// Height budget for the scrollable list: total minus the chrome View() adds
	// (header + blank lines + footer) and our own title + the detail box.
	detailLines := strings.Count(detail, "\n") + 1
	reserved := 1 /*header*/ + 2 /*View spacers*/ + 2 /*footer*/ + 2 /*title+blank*/ + 1 /*blank before detail*/ + detailLines
	budget := height - reserved
	if budget < 3 {
		budget = 3
	}

	var listLines []string
	for _, r := range rows {
		listLines = append(listLines, r.text)
	}
	if len(listLines) > budget {
		listLines = windowLines(listLines, selRow, budget)
	}

	return title + "\n\n" + strings.Join(listLines, "\n") + "\n\n" + detail
}

// windowLines returns a vertical slice of lines of length budget that always
// contains the focused line, with "↑/↓ more" markers when content is clipped.
func windowLines(lines []string, focus, budget int) []string {
	if budget < 1 {
		budget = 1
	}
	start := focus - budget/2
	if start < 0 {
		start = 0
	}
	if start+budget > len(lines) {
		start = len(lines) - budget
	}
	if start < 0 {
		start = 0
	}
	end := start + budget
	if end > len(lines) {
		end = len(lines)
	}
	out := append([]string(nil), lines[start:end]...)
	if start > 0 {
		out[0] = helpStyle.Render("    ↑ more")
	}
	if end < len(lines) {
		out[len(out)-1] = helpStyle.Render("    ↓ more")
	}
	return out
}

// templateDetail renders the full, width-wrapped description, stack tags, and id
// of the selected template so the user understands what they're about to create.
func (m model) templateDetail(t projectTemplate, width int) string {
	inner := width - 8
	if inner < 24 {
		inner = 24
	}
	if inner > 96 {
		inner = 96
	}

	heading := templateFamily(t)
	if t.VariantLabel != "" {
		heading += " · " + t.VariantLabel
	}
	parts := []string{titleStyle.Render(heading)}
	if desc := strings.TrimSpace(t.Description); desc != "" {
		parts = append(parts, "", lipgloss.NewStyle().Width(inner).Render(desc))
	}
	if len(t.Tags) > 0 {
		parts = append(parts, "", helpStyle.Width(inner).Render(strings.Join(t.Tags, " · ")))
	}
	parts = append(parts, "", helpStyle.Render("id "+t.ID))
	return boxStyle.Render(strings.Join(parts, "\n"))
}

func (m model) footerView() string {
	var status string
	switch {
	case m.errText != "":
		status = errStyle.Render("✗ " + m.errText)
	case m.busy:
		status = m.spinner.View() + " " + m.busyMsg
	case m.info != "":
		status = okStyle.Render("✓ " + m.info)
	}
	help := helpStyle.Render(m.keyHelp())
	if status == "" {
		return help
	}
	return status + "\n" + help
}

func (m model) keyHelp() string {
	envHint := ""
	if len(m.environments) > 1 {
		envHint = " • e backend"
	}
	switch m.screen {
	case screenAuth:
		return "l/enter browser login • t token" + envHint + " • q quit"
	case screenDeviceLogin:
		return "q cancel"
	case screenTokenLogin:
		return "enter save • esc back"
	case screenProjectPick:
		return "enter open • ctrl+t templates • esc quit"
	case screenStatus:
		if m.busy {
			return "q quit"
		}
		return "r refresh • d deploy preview • p deploy prod • s sync • g generate • l logs • t templates" + envHint + " • q quit"
	case screenGenerate:
		return "enter generate • esc back"
	case screenTemplates:
		return "↑/↓ select • enter create • esc back • q quit"
	case screenCreateName:
		return "enter create • esc back"
	case screenLogs:
		return "↑/↓ scroll • esc back • q quit"
	}
	return "q quit"
}

func formatDeploymentLines(label string, d deploymentInfo) []string {
	var out []string
	if strings.TrimSpace(d.URL) != "" {
		out = append(out, labelStyle.Render(label+" URL  ")+strings.TrimSpace(d.URL))
	}
	if len(d.Services) > 0 {
		out = append(out, labelStyle.Render(label+" services:"))
		for _, service := range d.Services {
			component := firstNonEmpty(service.ComponentID, "primary")
			status := firstNonEmpty(service.Status, "unknown")
			row := "  - " + component + " " + status
			if strings.TrimSpace(service.URL) != "" {
				row += "  " + strings.TrimSpace(service.URL)
			}
			out = append(out, row)
		}
		return out
	}
	if strings.TrimSpace(d.ServiceURL) != "" {
		out = append(out, labelStyle.Render(label+" service URL  ")+strings.TrimSpace(d.ServiceURL))
	}
	return out
}
