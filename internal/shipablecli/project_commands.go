package shipablecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const defaultWaitTimeout = 10 * time.Minute

const (
	defaultFullstackTemplateID = "dashboard-go-workos-postgres-redis"
	defaultFrontendTemplateID  = "starter-static"
	defaultServiceTemplateID   = "starter-node-api"
)

type projectTemplate struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Category     string   `json:"category"`
	Tags         []string `json:"tags"`
	VariantLabel string   `json:"variantLabel"`
	SortOrder    int      `json:"sortOrder"`
	Status       string   `json:"status"`
}

type templateFilesResponse struct {
	Template projectTemplate `json:"template"`
	Files    []templateFile  `json:"files"`
}

type templateFile struct {
	Path        string `json:"path"`
	ContentHash string `json:"contentHash"`
	Content     string `json:"content"`
}

type projectInfo struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	TemplateType    string `json:"templateType"`
	LatestVersionID string `json:"latestVersionId"`
	ActiveVersionID string `json:"activeVersionId"`
}

type deploymentInfo struct {
	DeploymentID           string                  `json:"deploymentId"`
	ProjectID              string                  `json:"projectId"`
	VersionID              string                  `json:"versionId"`
	Status                 string                  `json:"status"`
	RuntimeKind            string                  `json:"runtimeKind"`
	ServiceStatus          string                  `json:"serviceStatus"`
	URL                    string                  `json:"url"`
	ServiceURL             string                  `json:"serviceUrl"`
	ServiceLogsURL         string                  `json:"serviceLogsUrl"`
	Error                  string                  `json:"error"`
	ServiceError           string                  `json:"serviceError"`
	ServicePort            int                     `json:"servicePort"`
	ServiceHealthCheckPath string                  `json:"serviceHealthCheckPath"`
	ServiceFramework       string                  `json:"serviceFramework"`
	Services               []deploymentServiceInfo `json:"services,omitempty"`
}

type deploymentServiceInfo struct {
	ComponentID      string `json:"componentId"`
	RuntimeKind      string `json:"runtimeKind"`
	Status           string `json:"status"`
	URL              string `json:"url"`
	LogsURL          string `json:"logsUrl,omitempty"`
	Error            string `json:"error,omitempty"`
	DatabaseKind     string `json:"databaseKind,omitempty"`
	ServicePort      int    `json:"servicePort"`
	HealthCheckPath  string `json:"healthCheckPath,omitempty"`
	ServiceFramework string `json:"serviceFramework,omitempty"`
	UpdatedAt        string `json:"updatedAt,omitempty"`
}

type latestJobInfo struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	VersionID string `json:"versionId"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Error     string `json:"error"`
}

type generationRunInfo struct {
	ID              string `json:"id"`
	ProjectID       string `json:"projectId"`
	Status          string `json:"status"`
	Prompt          string `json:"prompt"`
	TargetVersionID string `json:"targetVersionId"`
	Error           string `json:"error"`
}

type readinessInfo struct {
	Status   string                        `json:"status"`
	Phase    string                        `json:"phase"`
	Blockers []readinessBlockerInfo        `json:"blockers"`
	Warnings []readinessBlockerInfo        `json:"warnings"`
	Checks   map[string]readinessCheckInfo `json:"checks"`
}

type readinessBlockerInfo struct {
	Code        string `json:"code"`
	Severity    string `json:"severity"`
	ComponentID string `json:"componentId,omitempty"`
	Message     string `json:"message"`
	Action      string `json:"action"`
}

type readinessCheckInfo struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Action  string `json:"action,omitempty"`
}

// statusReport is the assembled project status the status command renders and
// the TUI consumes. The json tags are kept identical to the previous inline
// struct in runStatus so `status --json` output stays byte-for-byte the same.
type statusReport struct {
	Project    projectInfo    `json:"project"`
	Readiness  readinessInfo  `json:"readiness"`
	Preview    deploymentInfo `json:"preview,omitempty"`
	Production deploymentInfo `json:"production,omitempty"`
}

func (r runner) runTemplates(args []string) error {
	fs := newFlagSet("shipable templates", r.stderr)
	jsonOutput := fs.Bool("json", false, "print templates as JSON")
	shape := fs.String("shape", "", "filter by project shape: frontend, service, or fullstack")
	if err := fs.Parse(args); err != nil {
		return err
	}
	normalizedShape, err := normalizeProjectShape(*shape, false)
	if err != nil {
		return err
	}
	cfg, err := r.loadConfig()
	if err != nil {
		return err
	}
	var templates []projectTemplate
	if err := r.apiJSON(context.Background(), cfg, http.MethodGet, "/v1/project-templates", nil, &templates); err != nil {
		return err
	}
	if normalizedShape != "" {
		templates = filterTemplatesByShape(templates, normalizedShape)
	}
	if *jsonOutput {
		content, err := json.MarshalIndent(templates, "", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(r.stdout, string(content))
		return nil
	}
	for _, template := range templates {
		label := firstNonEmpty(template.Name, template.VariantLabel)
		if template.VariantLabel != "" && template.VariantLabel != template.Name {
			label += " (" + template.VariantLabel + ")"
		}
		_, _ = fmt.Fprintf(r.stdout, "%s\t%s\t%s\n", template.ID, label, template.Description)
	}
	return nil
}

func (r runner) runCreate(args []string) error {
	fs := newFlagSet("shipable create", r.stderr)
	name := fs.String("name", "", "project name")
	templateID := fs.String("template", "", "template id or alias; defaults from --shape")
	shape := fs.String("shape", "fullstack", "project shape for the default template: frontend, service, or fullstack")
	dir := fs.String("dir", ".", "local project directory")
	deploy := fs.String("deploy", "none", "deploy target: none, preview, or production")
	wait := fs.Bool("wait", false, "wait for deployment to become ready")
	force := fs.Bool("force", false, "allow scaffolding into a non-empty directory and overwriting template paths")
	if err := fs.Parse(args); err != nil {
		return err
	}
	projectName := strings.TrimSpace(*name)
	if projectName == "" {
		return errors.New("missing --name")
	}
	if err := validateDeployTarget(*deploy); err != nil {
		return err
	}
	selectedTemplate, err := templateForCreate(*templateID, *shape)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if err := ensureScaffoldTarget(root, *force); err != nil {
		return err
	}
	cfg, err := r.loadConfig()
	if err != nil {
		return err
	}
	template, err := r.fetchTemplateFiles(context.Background(), cfg, selectedTemplate)
	if err != nil {
		return err
	}
	var project projectInfo
	createBody := map[string]any{
		"name":         projectName,
		"template":     selectedTemplate,
		"creationMode": "template",
	}
	if err := r.apiJSON(context.Background(), cfg, http.MethodPost, "/v1/projects", createBody, &project); err != nil {
		return err
	}
	if project.ID == "" {
		return errors.New("create project response did not include id")
	}
	if err := writeScaffoldFiles(root, template.Files, *force); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(root, ".shipable", "project.json"), projectLinkFile{ProjectID: project.ID}, 0o644); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(r.stdout, "Created project %s (%s)\n", project.Name, project.ID)
	_, _ = fmt.Fprintf(r.stdout, "Scaffolded %d files in %s\n", len(template.Files), root)
	if *deploy != "none" {
		deployment, err := r.queueDeployment(context.Background(), cfg, project.ID, *deploy)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(r.stdout, "Queued %s deploy\n", *deploy)
		if !*wait {
			r.printDeploymentEndpoints(deploymentLabel(*deploy), deployment)
		}
		if *wait {
			if *deploy == "preview" {
				if _, err := r.waitForLatestJob(context.Background(), cfg, project.ID); err != nil {
					return err
				}
			}
			if _, err := r.waitForDeployment(context.Background(), cfg, project.ID, *deploy); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r runner) runDeploy(args []string) error {
	fs := newFlagSet("shipable deploy", r.stderr)
	projectID := fs.String("project", "", "Shipable project ID")
	dir := fs.String("dir", ".", "local project directory")
	target := fs.String("target", "preview", "deploy target: preview or production")
	wait := fs.Bool("wait", false, "wait for deployment to become ready")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *target != "preview" && *target != "production" {
		return errors.New("--target must be preview or production")
	}
	resolvedProjectID, root, err := r.resolveProjectID(*projectID, *dir)
	if err != nil {
		return err
	}
	_ = root
	cfg, err := r.loadConfig()
	if err != nil {
		return err
	}
	if deployment, err := r.queueDeployment(context.Background(), cfg, resolvedProjectID, *target); err != nil {
		return err
	} else if !*wait {
		r.printDeploymentEndpoints(deploymentLabel(*target), deployment)
	}
	_, _ = fmt.Fprintf(r.stdout, "Queued %s deploy for %s\n", *target, resolvedProjectID)
	if *wait {
		if *target == "preview" {
			if _, err := r.waitForLatestJob(context.Background(), cfg, resolvedProjectID); err != nil {
				return err
			}
		}
		if _, err := r.waitForDeployment(context.Background(), cfg, resolvedProjectID, *target); err != nil {
			return err
		}
	}
	return nil
}

func (r runner) runPull(args []string) error {
	fs := newFlagSet("shipable pull", r.stderr)
	projectID := fs.String("project", "", "Shipable project ID")
	dir := fs.String("dir", ".", "local project directory")
	version := fs.String("version", "latest", "version id or latest")
	force := fs.Bool("force", false, "overwrite differing local files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolvedProjectID, root, err := r.resolveProjectID(*projectID, *dir)
	if err != nil {
		return err
	}
	cfg, err := r.loadConfig()
	if err != nil {
		return err
	}
	files, err := r.fetchProjectFiles(context.Background(), cfg, resolvedProjectID, *version)
	if err != nil {
		return err
	}
	written, skipped, err := writeProjectFiles(root, files, *force)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(r.stdout, "Pulled %d files from %s\n", written, resolvedProjectID)
	if skipped > 0 {
		_, _ = fmt.Fprintf(r.stdout, "Skipped %d ignored files\n", skipped)
	}
	return nil
}

func (r runner) runGenerate(args []string) error {
	fs := newFlagSet("shipable generate", r.stderr)
	projectID := fs.String("project", "", "Shipable project ID")
	dir := fs.String("dir", ".", "local project directory")
	prompt := fs.String("prompt", "", "generation prompt")
	wait := fs.Bool("wait", false, "wait for generation to finish")
	pull := fs.Bool("pull", false, "pull generated files after success")
	deploy := fs.String("deploy", "none", "deploy target: none, preview, or production")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*prompt) == "" {
		return errors.New("missing --prompt")
	}
	if err := validateDeployTarget(*deploy); err != nil {
		return err
	}
	if (*pull || *deploy != "none") && !*wait {
		return errors.New("--pull and --deploy require --wait for generate")
	}
	resolvedProjectID, root, err := r.resolveProjectID(*projectID, *dir)
	if err != nil {
		return err
	}
	cfg, err := r.loadConfig()
	if err != nil {
		return err
	}
	var run generationRunInfo
	if err := r.apiJSON(context.Background(), cfg, http.MethodPost, "/v1/projects/"+encodeID(resolvedProjectID)+"/generations", map[string]any{
		"prompt": strings.TrimSpace(*prompt),
	}, &run); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(r.stdout, "Queued generation %s\n", run.ID)
	if *wait {
		run, err = r.waitForGeneration(context.Background(), cfg, resolvedProjectID, run.ID)
		if err != nil {
			return err
		}
	}
	if *pull {
		files, err := r.fetchProjectFiles(context.Background(), cfg, resolvedProjectID, "latest")
		if err != nil {
			return err
		}
		written, skipped, err := writeProjectFiles(root, files, true)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(r.stdout, "Pulled %d generated files\n", written)
		if skipped > 0 {
			_, _ = fmt.Fprintf(r.stdout, "Skipped %d ignored files\n", skipped)
		}
	}
	if *deploy != "none" {
		if _, err := r.queueDeployment(context.Background(), cfg, resolvedProjectID, *deploy); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(r.stdout, "Queued %s deploy\n", *deploy)
		if *deploy == "preview" {
			if _, err := r.waitForLatestJob(context.Background(), cfg, resolvedProjectID); err != nil {
				return err
			}
		}
		if _, err := r.waitForDeployment(context.Background(), cfg, resolvedProjectID, *deploy); err != nil {
			return err
		}
	}
	_ = run
	return nil
}

func (r runner) runStatus(args []string) error {
	fs := newFlagSet("shipable status", r.stderr)
	projectID := fs.String("project", "", "Shipable project ID")
	dir := fs.String("dir", ".", "local project directory")
	jsonOutput := fs.Bool("json", false, "print status as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolvedProjectID, _, err := r.resolveProjectID(*projectID, *dir)
	if err != nil {
		return err
	}
	cfg, err := r.loadConfig()
	if err != nil {
		return err
	}
	status, err := r.fetchStatus(context.Background(), cfg, resolvedProjectID)
	if err != nil {
		return err
	}
	if *jsonOutput {
		content, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(r.stdout, string(content))
		return nil
	}
	_, _ = fmt.Fprintf(r.stdout, "Project: %s (%s)\n", firstNonEmpty(status.Project.Name, status.Project.ID), status.Project.Status)
	if status.Project.LatestVersionID != "" {
		_, _ = fmt.Fprintf(r.stdout, "Latest version: %s\n", status.Project.LatestVersionID)
	}
	if status.Readiness.Status != "" {
		_, _ = fmt.Fprintf(r.stdout, "Readiness: %s", status.Readiness.Status)
		if status.Readiness.Phase != "" {
			_, _ = fmt.Fprintf(r.stdout, " (%s)", status.Readiness.Phase)
		}
		_, _ = fmt.Fprintln(r.stdout)
		for _, blocker := range status.Readiness.Blockers {
			_, _ = fmt.Fprintf(r.stdout, "- %s: %s", blocker.Code, blocker.Message)
			if blocker.Action != "" {
				_, _ = fmt.Fprintf(r.stdout, " Action: %s", blocker.Action)
			}
			_, _ = fmt.Fprintln(r.stdout)
		}
	}
	if status.Preview.URL != "" {
		r.printDeploymentEndpoints("Preview", status.Preview)
	} else if deploymentHasServiceEndpoints(status.Preview) {
		r.printDeploymentEndpoints("Preview", status.Preview)
	}
	if status.Production.URL != "" {
		r.printDeploymentEndpoints("Production", status.Production)
	} else if deploymentHasServiceEndpoints(status.Production) {
		r.printDeploymentEndpoints("Production", status.Production)
	}
	return nil
}

func (r runner) runServiceLogs(args []string) error {
	fs := newFlagSet("shipable logs", r.stderr)
	projectID := fs.String("project", "", "Shipable project ID")
	dir := fs.String("dir", ".", "local project directory")
	target := fs.String("target", "preview", "deployment target to inspect: preview or production")
	deploymentID := fs.String("deployment", "", "deployment id; defaults to latest target deployment")
	componentID := fs.String("component", "", "service component id")
	componentAlias := fs.String("component-id", "", "service component id")
	follow := fs.Bool("follow", false, "follow service logs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *target != "preview" && *target != "production" {
		return errors.New("--target must be preview or production")
	}
	resolvedProjectID, _, err := r.resolveProjectID(*projectID, *dir)
	if err != nil {
		return err
	}
	selectedComponent := strings.TrimSpace(*componentID)
	if selectedComponent == "" {
		selectedComponent = strings.TrimSpace(*componentAlias)
	}
	cfg, err := r.loadConfig()
	if err != nil {
		return err
	}
	resolvedDeploymentID := strings.TrimSpace(*deploymentID)
	if resolvedDeploymentID == "" {
		deployment, err := r.fetchDeployment(context.Background(), cfg, resolvedProjectID, *target)
		if err != nil {
			return err
		}
		resolvedDeploymentID = strings.TrimSpace(deployment.DeploymentID)
		if resolvedDeploymentID == "" {
			return fmt.Errorf("%s deployment response did not include deploymentId", *target)
		}
		selectedComponent, err = selectServiceLogsComponent(deployment, selectedComponent)
		if err != nil {
			return err
		}
	}
	apiPath := "/v1/projects/" + encodeID(resolvedProjectID) +
		"/deployments/" + encodeID(resolvedDeploymentID) +
		"/service-logs"
	query := url.Values{}
	if selectedComponent != "" {
		query.Set("componentId", selectedComponent)
	}
	if *follow {
		query.Set("follow", "1")
	}
	if encoded := query.Encode(); encoded != "" {
		apiPath += "?" + encoded
	}
	ctx := context.Background()
	if *follow {
		// Follow streams are long-lived and use a timeout-free client, so the
		// request context is the only way to stop them. Cancel on Ctrl-C.
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
	}
	return r.apiStream(ctx, cfg, apiPath, r.stdout, *follow)
}

func (r runner) fetchTemplateFiles(ctx context.Context, cfg configFile, id string) (templateFilesResponse, error) {
	var response templateFilesResponse
	if err := r.apiJSON(ctx, cfg, http.MethodGet, "/v1/project-templates/"+encodeID(strings.TrimSpace(id))+"/files", nil, &response); err != nil {
		return templateFilesResponse{}, err
	}
	if len(response.Files) == 0 {
		return templateFilesResponse{}, fmt.Errorf("template %s returned no files", id)
	}
	sort.Slice(response.Files, func(i, j int) bool {
		return response.Files[i].Path < response.Files[j].Path
	})
	return response, nil
}

func (r runner) fetchProjectFiles(ctx context.Context, cfg configFile, projectID string, version string) ([]projectFile, error) {
	apiPath := "/v1/projects/" + encodeID(projectID) + "/files"
	version = strings.TrimSpace(version)
	if version != "" && version != "latest" {
		apiPath += "?versionId=" + url.QueryEscape(version)
	}
	var files []projectFile
	if err := r.apiJSON(ctx, cfg, http.MethodGet, apiPath, nil, &files); err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

// fetchStatus assembles the project status used by both `status` and the TUI.
// It deliberately swallows errors from the preview/production deployment
// lookups (they 404 before a first deploy), returning a partial report — the
// same behavior the inline runStatus assembly had.
func (r runner) fetchStatus(ctx context.Context, cfg configFile, projectID string) (statusReport, error) {
	var status statusReport
	if err := r.apiJSON(ctx, cfg, http.MethodGet, "/v1/projects/"+encodeID(projectID), nil, &status.Project); err != nil {
		return statusReport{}, err
	}
	if err := r.apiJSON(ctx, cfg, http.MethodGet, "/v1/projects/"+encodeID(projectID)+"/readiness", nil, &status.Readiness); err != nil {
		return statusReport{}, err
	}
	_ = r.apiJSON(ctx, cfg, http.MethodGet, "/v1/projects/"+encodeID(projectID)+"/preview", nil, &status.Preview)
	_ = r.apiJSON(ctx, cfg, http.MethodGet, "/v1/projects/"+encodeID(projectID)+"/production", nil, &status.Production)
	return status, nil
}

func (r runner) fetchDeployment(ctx context.Context, cfg configFile, projectID string, target string) (deploymentInfo, error) {
	if target != "preview" && target != "production" {
		return deploymentInfo{}, errors.New("deploy target must be preview or production")
	}
	var deployment deploymentInfo
	err := r.apiJSON(ctx, cfg, http.MethodGet, "/v1/projects/"+encodeID(projectID)+"/"+target, nil, &deployment)
	return deployment, err
}

func (r runner) queueDeployment(ctx context.Context, cfg configFile, projectID string, target string) (deploymentInfo, error) {
	var deployment deploymentInfo
	switch target {
	case "preview":
		err := r.apiJSON(ctx, cfg, http.MethodPost, "/v1/projects/"+encodeID(projectID)+"/preview", nil, &deployment)
		return deployment, err
	case "production":
		err := r.apiJSON(ctx, cfg, http.MethodPost, "/v1/projects/"+encodeID(projectID)+"/production", nil, &deployment)
		return deployment, err
	default:
		return deploymentInfo{}, errors.New("deploy target must be preview or production")
	}
}

func (r runner) printDeploymentEndpoints(label string, deployment deploymentInfo) {
	if strings.TrimSpace(deployment.URL) != "" {
		_, _ = fmt.Fprintf(r.stdout, "%s URL: %s\n", label, strings.TrimSpace(deployment.URL))
	}
	if len(deployment.Services) > 0 {
		serviceLabel := "services"
		if len(deployment.Services) == 1 {
			serviceLabel = "service"
		}
		_, _ = fmt.Fprintf(r.stdout, "%s %s:\n", label, serviceLabel)
		for _, service := range deployment.Services {
			component := strings.TrimSpace(service.ComponentID)
			if component == "" {
				component = "primary"
			}
			status := strings.TrimSpace(service.Status)
			if status == "" {
				status = "unknown"
			}
			if strings.TrimSpace(service.URL) != "" {
				_, _ = fmt.Fprintf(r.stdout, "- %s %s %s\n", component, status, strings.TrimSpace(service.URL))
			} else {
				_, _ = fmt.Fprintf(r.stdout, "- %s %s\n", component, status)
			}
			if strings.TrimSpace(service.LogsURL) != "" {
				_, _ = fmt.Fprintf(r.stdout, "  logs: %s\n", strings.TrimSpace(service.LogsURL))
			}
		}
		return
	}
	if strings.TrimSpace(deployment.ServiceURL) != "" {
		_, _ = fmt.Fprintf(r.stdout, "%s service URL: %s\n", label, strings.TrimSpace(deployment.ServiceURL))
	}
	if strings.TrimSpace(deployment.ServiceLogsURL) != "" {
		_, _ = fmt.Fprintf(r.stdout, "%s service logs: %s\n", label, strings.TrimSpace(deployment.ServiceLogsURL))
	}
}

func deploymentLabel(target string) string {
	if target == "production" {
		return "Production"
	}
	return "Preview"
}

func deploymentHasServiceEndpoints(deployment deploymentInfo) bool {
	if len(deployment.Services) > 0 {
		return true
	}
	return strings.TrimSpace(deployment.ServiceURL) != "" || strings.TrimSpace(deployment.ServiceLogsURL) != ""
}

func selectServiceLogsComponent(deployment deploymentInfo, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		if len(deployment.Services) == 0 {
			return requested, nil
		}
		for _, service := range deployment.Services {
			if strings.TrimSpace(service.ComponentID) == requested {
				return requested, nil
			}
		}
		return "", fmt.Errorf("service component %q was not found in deployment %s", requested, strings.TrimSpace(deployment.DeploymentID))
	}
	services := servicesWithLogsOrURL(deployment.Services)
	switch len(services) {
	case 0:
		return "", nil
	case 1:
		return strings.TrimSpace(services[0].ComponentID), nil
	default:
		ids := make([]string, 0, len(services))
		for _, service := range services {
			if id := strings.TrimSpace(service.ComponentID); id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return "", errors.New("deployment has multiple services; pass --component")
		}
		return "", fmt.Errorf("deployment has multiple services; pass --component (%s)", strings.Join(ids, ", "))
	}
}

func servicesWithLogsOrURL(services []deploymentServiceInfo) []deploymentServiceInfo {
	out := make([]deploymentServiceInfo, 0, len(services))
	for _, service := range services {
		if strings.TrimSpace(service.LogsURL) == "" && strings.TrimSpace(service.URL) == "" {
			continue
		}
		out = append(out, service)
	}
	return out
}

func (r runner) waitForLatestJob(ctx context.Context, cfg configFile, projectID string) (latestJobInfo, error) {
	deadline := time.Now().Add(r.waitTimeout())
	var last latestJobInfo
	for {
		if err := r.apiJSON(ctx, cfg, http.MethodGet, "/v1/projects/"+encodeID(projectID)+"/jobs/latest", nil, &last); err != nil {
			return latestJobInfo{}, err
		}
		job := last
		r.emit(Event{Kind: EvtJobPolled, Command: "build", Status: last.Status, Job: &job})
		if terminalJobStatus(last.Status) {
			if last.Status != "succeeded" {
				return last, fmt.Errorf("job %s %s: %s", last.ID, last.Status, strings.TrimSpace(last.Error))
			}
			r.emit(Event{Kind: EvtJobSucceeded, Command: "build", Status: last.Status, Job: &job})
			_, _ = fmt.Fprintf(r.stdout, "Build job %s succeeded\n", last.ID)
			return last, nil
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("timed out waiting for job %s", last.ID)
		}
		if err := sleepContext(ctx, r.pollInterval()); err != nil {
			return last, err
		}
	}
}

func (r runner) waitForDeployment(ctx context.Context, cfg configFile, projectID string, target string) (deploymentInfo, error) {
	deadline := time.Now().Add(r.waitTimeout())
	var deployment deploymentInfo
	apiPath := "/v1/projects/" + encodeID(projectID) + "/" + target
	for {
		if err := r.apiJSON(ctx, cfg, http.MethodGet, apiPath, nil, &deployment); err != nil {
			return deploymentInfo{}, err
		}
		dep := deployment
		r.emit(Event{Kind: EvtDeploymentPolled, Command: "deploy", Target: target, Status: deployment.Status, Deployment: &dep})
		if deploymentReady(deployment) {
			r.emit(Event{Kind: EvtDeploymentReady, Command: "deploy", Target: target, Status: deployment.Status, Deployment: &dep})
			r.printDeploymentEndpoints(deploymentLabel(target), deployment)
			return deployment, nil
		}
		if deploymentFailed(deployment) {
			return deployment, fmt.Errorf("%s deploy failed: %s", target, firstNonEmpty(deployment.Error, deployment.ServiceStatus, deployment.Status))
		}
		if time.Now().After(deadline) {
			return deployment, fmt.Errorf("timed out waiting for %s deploy", target)
		}
		if err := sleepContext(ctx, r.pollInterval()); err != nil {
			return deployment, err
		}
	}
}

func (r runner) waitForGeneration(ctx context.Context, cfg configFile, projectID string, generationID string) (generationRunInfo, error) {
	deadline := time.Now().Add(r.waitTimeout())
	var run generationRunInfo
	for {
		if err := r.apiJSON(ctx, cfg, http.MethodGet, "/v1/projects/"+encodeID(projectID)+"/generations/latest", nil, &run); err != nil {
			return generationRunInfo{}, err
		}
		if generationID != "" && run.ID != "" && run.ID != generationID {
			return run, fmt.Errorf("latest generation is %s, expected %s", run.ID, generationID)
		}
		gen := run
		r.emit(Event{Kind: EvtGenerationPolled, Command: "generate", Status: run.Status, Generation: &gen})
		if terminalGenerationStatus(run.Status) {
			if run.Status != "succeeded" {
				return run, fmt.Errorf("generation %s %s: %s", run.ID, run.Status, strings.TrimSpace(run.Error))
			}
			r.emit(Event{Kind: EvtGenerationSucceeded, Command: "generate", Status: run.Status, Generation: &gen})
			_, _ = fmt.Fprintf(r.stdout, "Generation %s succeeded\n", run.ID)
			return run, nil
		}
		if time.Now().After(deadline) {
			return run, fmt.Errorf("timed out waiting for generation %s", generationID)
		}
		if err := sleepContext(ctx, r.pollInterval()); err != nil {
			return run, err
		}
	}
}

func (r runner) resolveProjectID(projectID string, dir string) (string, string, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	resolvedProjectID := strings.TrimSpace(projectID)
	if resolvedProjectID == "" {
		link, err := readProjectLink(root)
		if err != nil {
			return "", "", err
		}
		resolvedProjectID = link.ProjectID
	}
	if resolvedProjectID == "" {
		return "", "", errors.New("missing project id; pass --project or run shipable link")
	}
	return resolvedProjectID, root, nil
}

func ensureScaffoldTarget(root string, force bool) error {
	info, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	if force {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s is not empty; pass --force to scaffold into it", root)
	}
	return nil
}

func writeScaffoldFiles(root string, files []templateFile, force bool) error {
	for _, file := range files {
		target, ok := safeProjectPath(root, file.Path)
		if !ok {
			return fmt.Errorf("template file path is unsafe: %s", file.Path)
		}
		if err := ensureSafeProjectWritePath(root, target); err != nil {
			return err
		}
		if !force {
			if _, err := os.Stat(target); err == nil {
				return fmt.Errorf("%s already exists; pass --force to overwrite", target)
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(file.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeProjectFiles(root string, files []projectFile, force bool) (int, int, error) {
	written := 0
	skipped := 0
	for _, file := range files {
		if shouldSkipPulledPath(file.Path) {
			skipped++
			continue
		}
		target, ok := safeProjectPath(root, file.Path)
		if !ok {
			return written, skipped, fmt.Errorf("project file path is unsafe: %s", file.Path)
		}
		if !force {
			if err := ensureSafeProjectWritePath(root, target); err != nil {
				return written, skipped, err
			}
			if existing, err := os.ReadFile(target); err == nil && string(existing) != file.Content {
				return written, skipped, fmt.Errorf("%s has local changes; pass --force to overwrite", target)
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return written, skipped, err
			}
		} else if err := ensureSafeProjectWritePath(root, target); err != nil {
			return written, skipped, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return written, skipped, err
		}
		if err := os.WriteFile(target, []byte(file.Content), 0o644); err != nil {
			return written, skipped, err
		}
		written++
	}
	return written, skipped, nil
}

func ensureSafeProjectWritePath(root string, target string) error {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("project file path is unsafe: %s", target)
	}
	current := root
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("project file path traverses symlink: %s", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("project file parent is not a directory: %s", current)
		}
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to overwrite symlink: %s", target)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func safeProjectPath(root string, rel string) (string, bool) {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || strings.HasPrefix(rel, "/") {
		return "", false
	}
	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || filepath.IsAbs(cleaned) || windowsAbsolutePath(cleaned) {
		return "", false
	}
	return filepath.Join(root, cleaned), true
}

func windowsAbsolutePath(path string) bool {
	return len(path) >= 3 &&
		((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) &&
		path[1] == ':' &&
		(path[2] == '/' || path[2] == '\\')
}

func shouldSkipPulledPath(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 0 {
		return true
	}
	for _, part := range parts[:len(parts)-1] {
		if shouldIgnoreDir(part) {
			return true
		}
	}
	return shouldIgnoreFile(parts[len(parts)-1])
}

func validateDeployTarget(target string) error {
	if target != "none" && target != "preview" && target != "production" {
		return errors.New("--deploy must be none, preview, or production")
	}
	return nil
}

func templateForCreate(templateID string, shape string) (string, error) {
	templateID = strings.TrimSpace(templateID)
	if templateID != "" {
		if _, err := normalizeProjectShape(shape, true); err != nil {
			return "", err
		}
		return templateID, nil
	}
	normalizedShape, err := normalizeProjectShape(shape, true)
	if err != nil {
		return "", err
	}
	switch normalizedShape {
	case "frontend":
		return defaultFrontendTemplateID, nil
	case "service":
		return defaultServiceTemplateID, nil
	case "fullstack":
		return defaultFullstackTemplateID, nil
	default:
		return "", fmt.Errorf("unsupported project shape %q", shape)
	}
}

func normalizeProjectShape(shape string, defaultFullstack bool) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(shape))
	if normalized == "" {
		if defaultFullstack {
			return "fullstack", nil
		}
		return "", nil
	}
	switch normalized {
	case "frontend", "front-end", "static", "static_site", "static-site", "site", "web":
		return "frontend", nil
	case "service", "api", "backend", "back-end", "container", "docker":
		return "service", nil
	case "fullstack", "full-stack", "app", "dashboard":
		return "fullstack", nil
	default:
		return "", fmt.Errorf("--shape must be frontend, service, or fullstack")
	}
}

func filterTemplatesByShape(templates []projectTemplate, shape string) []projectTemplate {
	filtered := make([]projectTemplate, 0, len(templates))
	for _, template := range templates {
		if templateMatchesShape(template, shape) {
			filtered = append(filtered, template)
		}
	}
	return filtered
}

func templateMatchesShape(template projectTemplate, shape string) bool {
	kind := templateShape(template)
	if shape == "service" {
		return kind == "service"
	}
	if shape == "frontend" {
		return kind == "frontend"
	}
	if shape == "fullstack" {
		return kind == "fullstack"
	}
	return true
}

func templateShape(template projectTemplate) string {
	category := strings.ToLower(strings.TrimSpace(template.Category))
	id := strings.ToLower(strings.TrimSpace(template.ID))
	variant := strings.ToLower(strings.TrimSpace(template.VariantLabel))
	switch {
	case category == "backend" ||
		templateHasTag(template, "backend") ||
		templateHasTag(template, "service") ||
		templateHasTag(template, "api") ||
		strings.Contains(id, "-api") ||
		strings.Contains(id, "api-"):
		return "service"
	case category == "frontend" ||
		templateHasTag(template, "frontend") ||
		templateHasTag(template, "static") ||
		strings.Contains(id, "static") ||
		strings.Contains(variant, "static"):
		return "frontend"
	default:
		return "fullstack"
	}
}

func templateHasTag(template projectTemplate, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, tag := range template.Tags {
		if strings.ToLower(strings.TrimSpace(tag)) == want {
			return true
		}
	}
	return false
}

func terminalJobStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "failed", "canceled", "cancelled", "superseded":
		return true
	default:
		return false
	}
}

func terminalGenerationStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "failed", "canceled", "cancelled", "build_failed":
		return true
	default:
		return false
	}
}

func deploymentReady(deployment deploymentInfo) bool {
	if strings.ToLower(strings.TrimSpace(deployment.Status)) != "ready" {
		return false
	}
	if len(deployment.Services) > 0 {
		for _, service := range deployment.Services {
			if !deploymentServiceReady(service) {
				return false
			}
		}
		return true
	}
	if strings.TrimSpace(deployment.URL) == "" && strings.TrimSpace(deployment.ServiceURL) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(deployment.ServiceStatus)) {
	case "", "ready", "not_applicable":
		return true
	default:
		return false
	}
}

func deploymentFailed(deployment deploymentInfo) bool {
	if strings.EqualFold(strings.TrimSpace(deployment.Status), "failed") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(deployment.ServiceStatus), "failed") {
		return true
	}
	for _, service := range deployment.Services {
		if strings.EqualFold(strings.TrimSpace(service.Status), "failed") {
			return true
		}
	}
	return false
}

func deploymentServiceReady(service deploymentServiceInfo) bool {
	status := strings.ToLower(strings.TrimSpace(service.Status))
	if status != "ready" {
		return false
	}
	return strings.TrimSpace(service.URL) != ""
}

func (r runner) pollInterval() time.Duration {
	if value := strings.TrimSpace(r.getenv("SHIPABLE_POLL_INTERVAL_MS")); value != "" {
		if millis, err := strconvAtoi(value); err == nil && millis > 0 {
			return time.Duration(millis) * time.Millisecond
		}
	}
	return defaultPoll
}

func (r runner) waitTimeout() time.Duration {
	if value := strings.TrimSpace(r.getenv("SHIPABLE_WAIT_TIMEOUT_MS")); value != "" {
		if millis, err := strconvAtoi(value); err == nil && millis > 0 {
			return time.Duration(millis) * time.Millisecond
		}
	}
	return defaultWaitTimeout
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func strconvAtoi(value string) (int, error) {
	var n int
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid integer %q", value)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}
