package shipablecli

// Reporter receives in-flight progress and terminal-result signals emitted by
// the engine's long-running operations (deploy/build/generate waits, device
// login, sync). It is the single seam every frontend shares.
//
// The headless CLI installs a discardReporter: its human-readable results
// already reach stdout through the existing prints, so the events are simply
// dropped and behavior is byte-for-byte unchanged. The TUI installs a reporter
// that forwards every Event to the Bubble Tea program, turning engine progress
// into tea.Msg values. Because the engine only ever emits typed Events (it
// never formats and, in TUI mode, writes to no terminal), both frontends
// consume the same stream and cannot diverge.
type Reporter interface {
	Emit(Event)
}

// EventKind classifies an Event. Intermediate "*Polled" kinds are progress;
// the "*Succeeded"/"*Ready" kinds carry a terminal result.
type EventKind int

const (
	EvtMessage EventKind = iota
	EvtJobPolled
	EvtJobSucceeded
	EvtDeploymentPolled
	EvtDeploymentReady
	EvtGenerationPolled
	EvtGenerationSucceeded
	EvtSyncCompleted
	EvtDeviceCode
	EvtLogLine
)

// Event carries in-flight progress and terminal-result signals. Final result
// values are still returned by the engine method that emits them; the Event
// duplicates them only so a frontend (the TUI) gets a live signal without
// scraping stdout.
type Event struct {
	Kind       EventKind
	Command    string // "deploy", "sync", "generate", "build", "login"
	Target     string // "preview" | "production"
	Status     string // e.g. deploymentInfo.Status
	Message    string
	Line       string // log line for EvtLogLine
	Deployment *deploymentInfo
	Job        *latestJobInfo
	Generation *generationRunInfo
	Sync       *syncResult
	Device     *deviceAuthorizeResponse
}

// discardReporter is the headless default; it drops every event.
type discardReporter struct{}

func (discardReporter) Emit(Event) {}

// emit is a nil-safe helper so engine methods can report unconditionally.
func (r runner) emit(e Event) {
	if r.report == nil {
		return
	}
	r.report.Emit(e)
}
