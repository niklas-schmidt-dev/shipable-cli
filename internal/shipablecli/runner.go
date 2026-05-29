package shipablecli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// Defaults to HTTPS so auth tokens are not sent in clear text. Local
	// development can override this with --api-url or SHIPABLE_API_URL.
	defaultAPIURL          = "https://localhost:8080"
	defaultPoll            = 2 * time.Second
	maxSyncFileSize        = 10 << 20
	maxErrorResponseLength = 200
	workOSAPIURL           = "https://api.workos.com"
)

type RunOptions struct {
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Env    map[string]string
	Client *http.Client
	Exec   func(name string, args []string, options execOptions) error
}

type execOptions struct {
	Dir    string
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Env    []string
}

type configFile struct {
	APIURL               string `json:"apiUrl"`
	AccessToken          string `json:"accessToken"`
	RefreshToken         string `json:"refreshToken,omitempty"`
	OrganizationID       string `json:"organizationId,omitempty"`
	WorkOSClientID       string `json:"workosClientId,omitempty"`
	WorkOSAPIURL         string `json:"workosApiUrl,omitempty"`
	AccessTokenExpiresAt string `json:"accessTokenExpiresAt,omitempty"`
}

type projectLinkFile struct {
	ProjectID string `json:"projectId"`
}

type runner struct {
	args   []string
	stdout io.Writer
	stderr io.Writer
	stdin  io.Reader
	env    map[string]string
	client *http.Client
	exec   func(name string, args []string, options execOptions) error
}

func Run(options RunOptions) error {
	r := runner{
		args:   options.Args,
		stdout: writerOrDiscard(options.Stdout),
		stderr: writerOrDiscard(options.Stderr),
		stdin:  options.Stdin,
		env:    options.Env,
		client: options.Client,
		exec:   options.Exec,
	}
	if r.stdin == nil {
		r.stdin = os.Stdin
	}
	if r.client == nil {
		r.client = &http.Client{
			Timeout: 30 * time.Second,
		}
	}
	if r.exec == nil {
		r.exec = defaultExec
	}
	return r.run()
}

func (r runner) run() error {
	if len(r.args) == 0 {
		return r.usage()
	}
	switch r.args[0] {
	case "version", "--version":
		return r.runVersion()
	case "auth":
		return r.runAuth(r.args[1:])
	case "templates":
		return r.runTemplates(r.args[1:])
	case "create":
		return r.runCreate(r.args[1:])
	case "link":
		return r.runLink(r.args[1:])
	case "sync":
		return r.runSync(r.args[1:])
	case "deploy":
		return r.runDeploy(r.args[1:])
	case "pull":
		return r.runPull(r.args[1:])
	case "generate":
		return r.runGenerate(r.args[1:])
	case "status":
		return r.runStatus(r.args[1:])
	case "logs", "service-logs":
		return r.runServiceLogs(r.args[1:])
	case "dev":
		return r.runDev(r.args[1:])
	case "help", "-h", "--help":
		return r.usage()
	default:
		return fmt.Errorf("unknown command %q", r.args[0])
	}
}

func (r runner) runVersion() error {
	_, _ = fmt.Fprintln(r.stdout, currentVersion().String())
	return nil
}

func (r runner) usage() error {
	_, _ = fmt.Fprintln(r.stdout, "Usage: shipable <auth|templates|create|link|sync|deploy|pull|generate|status|logs|dev|version> [options]")
	_, _ = fmt.Fprintln(r.stdout, "")
	_, _ = fmt.Fprintln(r.stdout, "Commands:")
	_, _ = fmt.Fprintln(r.stdout, "  version")
	_, _ = fmt.Fprintln(r.stdout, "  auth login [--client-id <workos-client-id>] [--api-url <url>]")
	_, _ = fmt.Fprintln(r.stdout, "  auth login --token-stdin [--api-url <url>]  # dev fallback")
	_, _ = fmt.Fprintln(r.stdout, "  auth status")
	_, _ = fmt.Fprintln(r.stdout, "  templates [--shape frontend|service|fullstack] [--json]")
	_, _ = fmt.Fprintln(r.stdout, "  create --name <name> [--template <template-id>] [--shape frontend|service|fullstack] --dir <path> [--deploy none|preview|production] [--wait] [--force]")
	_, _ = fmt.Fprintln(r.stdout, "  link --project <project-id> [--dir <path>]")
	_, _ = fmt.Fprintln(r.stdout, "  sync [--project <project-id>] [--dir <path>] [--build] [--deploy none|preview|production] [--delete] [--watch] [--wait]")
	_, _ = fmt.Fprintln(r.stdout, "  deploy [--project <project-id>] [--dir <path>] --target preview|production [--wait]")
	_, _ = fmt.Fprintln(r.stdout, "  pull [--project <project-id>] [--dir <path>] [--version latest] [--force]")
	_, _ = fmt.Fprintln(r.stdout, "  generate [--project <project-id>] [--dir <path>] --prompt <text> [--wait] [--pull] [--deploy none|preview|production]")
	_, _ = fmt.Fprintln(r.stdout, "  status [--project <project-id>] [--dir <path>] [--json]")
	_, _ = fmt.Fprintln(r.stdout, "  logs [--project <project-id>] [--dir <path>] [--target preview|production] [--deployment <deployment-id>] [--component <component-id>] [--follow]")
	_, _ = fmt.Fprintln(r.stdout, "  dev up")
	return nil
}

func (r runner) runAuth(args []string) error {
	if len(args) == 0 {
		return errors.New("auth requires login or status")
	}
	switch args[0] {
	case "login":
		fs := newFlagSet("shipable auth login", r.stderr)
		tokenStdin := fs.Bool("token-stdin", false, "Read WorkOS access token from stdin")
		apiURL := fs.String("api-url", defaultAPIURL, "Shipable API URL")
		clientID := fs.String("client-id", "", "WorkOS client ID")
		workOSBase := fs.String("workos-api-url", "", "WorkOS API URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		trimmedToken := ""
		if *tokenStdin {
			content, err := io.ReadAll(io.LimitReader(r.stdin, 16*1024))
			if err != nil {
				return err
			}
			trimmedToken = strings.TrimSpace(string(content))
		}
		if trimmedToken == "" {
			trimmedToken = strings.TrimSpace(r.getenv("SHIPABLE_TOKEN"))
		}
		if trimmedToken == "" {
			return r.runDeviceLogin(context.Background(), deviceLoginInput{
				APIURL:       *apiURL,
				ClientID:     *clientID,
				WorkOSAPIURL: *workOSBase,
			})
		}
		cfg := configFile{APIURL: strings.TrimRight(strings.TrimSpace(*apiURL), "/"), AccessToken: trimmedToken}
		if cfg.APIURL == "" {
			cfg.APIURL = defaultAPIURL
		}
		if err := writeJSONFile(r.configPath(), cfg, 0o600); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(r.stdout, "Authenticated for %s\n", cfg.APIURL)
		return nil
	case "status":
		cfg, err := r.loadConfig()
		if err != nil {
			return err
		}
		var payload map[string]any
		if err := r.apiJSON(context.Background(), cfg, http.MethodGet, "/v1/session", nil, &payload); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(r.stdout, "Authenticated")
		return nil
	default:
		return fmt.Errorf("unknown auth command %q", args[0])
	}
}

type deviceLoginInput struct {
	APIURL       string
	ClientID     string
	WorkOSAPIURL string
}

type deviceAuthorizeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	OrganizationID   string `json:"organization_id"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (r runner) runDeviceLogin(ctx context.Context, input deviceLoginInput) error {
	apiURL := strings.TrimRight(strings.TrimSpace(input.APIURL), "/")
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	clientID := strings.TrimSpace(input.ClientID)
	if clientID == "" {
		clientID = strings.TrimSpace(r.getenv("SHIPABLE_WORKOS_CLIENT_ID"))
	}
	if clientID == "" {
		clientID = strings.TrimSpace(r.getenv("WORKOS_CLIENT_ID"))
	}
	if clientID == "" {
		clientID = discoverWorkOSClientID(".")
	}
	if clientID == "" {
		return errors.New("missing WorkOS client id; pass --client-id, set SHIPABLE_WORKOS_CLIENT_ID, or configure apps/api/.env")
	}
	workOSBase := strings.TrimRight(strings.TrimSpace(input.WorkOSAPIURL), "/")
	if workOSBase == "" {
		workOSBase = strings.TrimRight(strings.TrimSpace(r.getenv("SHIPABLE_WORKOS_API_URL")), "/")
	}
	if workOSBase == "" {
		workOSBase = workOSAPIURL
	}

	var device deviceAuthorizeResponse
	if err := r.workOSJSON(ctx, workOSBase, "/user_management/authorize/device", map[string]string{
		"client_id": clientID,
	}, &device); err != nil {
		return err
	}
	if device.DeviceCode == "" {
		return errors.New("WorkOS device auth response did not include device_code")
	}
	verificationURL := device.VerificationURIComplete
	if verificationURL == "" {
		verificationURL = device.VerificationURI
	}
	_, _ = fmt.Fprintf(r.stdout, "Open: %s\nCode: %s\nWaiting for browser approval...\n", verificationURL, device.UserCode)

	token, err := r.pollDeviceToken(ctx, workOSBase, clientID, device)
	if err != nil {
		return err
	}
	expiresAt := accessTokenExpiresAt(token.AccessToken, token.ExpiresIn)
	cfg := configFile{
		APIURL:               apiURL,
		AccessToken:          token.AccessToken,
		RefreshToken:         token.RefreshToken,
		OrganizationID:       token.OrganizationID,
		WorkOSClientID:       clientID,
		WorkOSAPIURL:         workOSBase,
		AccessTokenExpiresAt: expiresAt,
	}
	if err := writeJSONFile(r.configPath(), cfg, 0o600); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(r.stdout, "Authenticated for %s\n", cfg.APIURL)
	return nil
}

func (r runner) pollDeviceToken(ctx context.Context, workOSBase string, clientID string, device deviceAuthorizeResponse) (tokenResponse, error) {
	interval := time.Duration(device.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)
	if device.ExpiresIn <= 0 {
		deadline = time.Now().Add(10 * time.Minute)
	}
	for {
		token, err := r.authenticateDevice(ctx, workOSBase, clientID, device.DeviceCode)
		if err == nil {
			return token, nil
		}
		var pending deviceAuthPendingError
		if !errors.As(err, &pending) {
			return tokenResponse{}, err
		}
		if pending.code == "access_denied" {
			return tokenResponse{}, errors.New("device login denied")
		}
		if pending.code == "expired_token" || time.Now().After(deadline) {
			return tokenResponse{}, errors.New("device login expired")
		}
		if pending.code == "slow_down" {
			interval += time.Second
		}
		select {
		case <-ctx.Done():
			return tokenResponse{}, ctx.Err()
		case <-time.After(interval):
		}
	}
}

type deviceAuthPendingError struct {
	code        string
	description string
}

func (e deviceAuthPendingError) Error() string {
	if e.description != "" {
		return e.description
	}
	return e.code
}

func (r runner) authenticateDevice(ctx context.Context, workOSBase string, clientID string, deviceCode string) (tokenResponse, error) {
	values := url.Values{}
	values.Set("client_id", clientID)
	values.Set("device_code", deviceCode)
	values.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	var token tokenResponse
	err := r.workOSForm(ctx, workOSBase, "/user_management/authenticate", values, &token)
	if err != nil {
		return tokenResponse{}, err
	}
	if token.Error != "" {
		return tokenResponse{}, deviceAuthPendingError{code: token.Error, description: token.ErrorDescription}
	}
	if token.AccessToken == "" {
		return tokenResponse{}, errors.New("WorkOS authenticate response did not include access_token")
	}
	return token, nil
}

func (r runner) runLink(args []string) error {
	fs := newFlagSet("shipable link", r.stderr)
	projectID := fs.String("project", "", "Shipable project ID")
	dir := fs.String("dir", ".", "local project directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*projectID) == "" {
		return errors.New("missing --project")
	}
	root, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	link := projectLinkFile{ProjectID: strings.TrimSpace(*projectID)}
	path := filepath.Join(root, ".shipable", "project.json")
	if err := writeJSONFile(path, link, 0o644); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(r.stdout, "Linked %s to %s\n", root, link.ProjectID)
	return nil
}

func (r runner) runDev(args []string) error {
	if len(args) == 0 || args[0] != "up" {
		return errors.New("dev requires up")
	}
	return r.exec("make", []string{"local"}, execOptions{
		Stdout: r.stdout,
		Stderr: r.stderr,
		Stdin:  r.stdin,
	})
}

func (r runner) runSync(args []string) error {
	fs := newFlagSet("shipable sync", r.stderr)
	projectID := fs.String("project", "", "Shipable project ID")
	dir := fs.String("dir", ".", "local project directory")
	message := fs.String("message", "local cli sync", "version message")
	build := fs.Bool("build", false, "queue a build for the created version")
	deploy := fs.String("deploy", "none", "deploy target: none, preview, or production")
	deleteMissing := fs.Bool("delete", false, "delete remote files missing locally")
	watch := fs.Bool("watch", false, "watch local files and sync on change")
	wait := fs.Bool("wait", false, "wait for queued build/deploy work to finish")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *deploy != "none" && *deploy != "preview" && *deploy != "production" {
		return errors.New("--deploy must be none, preview, or production")
	}
	root, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	resolvedProjectID := strings.TrimSpace(*projectID)
	if resolvedProjectID == "" {
		link, err := readProjectLink(root)
		if err != nil {
			return err
		}
		resolvedProjectID = link.ProjectID
	}
	if resolvedProjectID == "" {
		return errors.New("missing project id; pass --project or run shipable link")
	}
	cfg, err := r.loadConfig()
	if err != nil {
		return err
	}
	runOnce := func() error {
		result, err := r.syncOnce(context.Background(), syncInput{
			Config:        cfg,
			ProjectID:     resolvedProjectID,
			Root:          root,
			Message:       *message,
			Build:         *build,
			Deploy:        *deploy,
			DeleteMissing: *deleteMissing,
		})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(r.stdout, "Synced %d files to %s as version %s\n", result.Uploaded, resolvedProjectID, result.VersionID)
		if result.Deleted > 0 {
			_, _ = fmt.Fprintf(r.stdout, "Deleted %d remote files\n", result.Deleted)
		}
		if result.Built {
			_, _ = fmt.Fprintf(r.stdout, "Queued build for version %s\n", result.VersionID)
		}
		if result.Deploy != "none" {
			_, _ = fmt.Fprintf(r.stdout, "Queued %s deploy\n", result.Deploy)
		}
		if *wait {
			if result.Built || result.Deploy == "preview" {
				if _, err := r.waitForLatestJob(context.Background(), cfg, resolvedProjectID); err != nil {
					return err
				}
			}
			if result.Deploy != "none" {
				if _, err := r.waitForDeployment(context.Background(), cfg, resolvedProjectID, result.Deploy); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if !*watch {
		return runOnce()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return r.watch(ctx, root, runOnce)
}

type syncInput struct {
	Config        configFile
	ProjectID     string
	Root          string
	Message       string
	Build         bool
	Deploy        string
	DeleteMissing bool
}

type syncResult struct {
	Uploaded  int
	Deleted   int
	VersionID string
	Built     bool
	Deploy    string
}

func (r runner) syncOnce(ctx context.Context, input syncInput) (syncResult, error) {
	files, skippedLocalPaths, err := scanFilesWithSkipped(input.Root)
	if err != nil {
		return syncResult{}, err
	}
	remotePaths := map[string]struct{}{}
	if input.DeleteMissing {
		var remote []projectFile
		if err := r.apiJSON(ctx, input.Config, http.MethodGet, "/v1/projects/"+encodeID(input.ProjectID)+"/files", nil, &remote); err != nil {
			return syncResult{}, err
		}
		for _, file := range remote {
			if shouldSkipPulledPath(file.Path) {
				continue
			}
			remotePaths[file.Path] = struct{}{}
		}
	}
	for _, file := range files {
		body := map[string]string{"content": file.Content}
		if err := r.apiJSON(ctx, input.Config, http.MethodPut, "/v1/projects/"+encodeID(input.ProjectID)+"/files/"+encodePath(file.Path), body, nil); err != nil {
			return syncResult{}, err
		}
		delete(remotePaths, file.Path)
	}
	for path := range skippedLocalPaths {
		delete(remotePaths, path)
	}
	deleted := 0
	if input.DeleteMissing {
		missing := make([]string, 0, len(remotePaths))
		for path := range remotePaths {
			missing = append(missing, path)
		}
		sort.Strings(missing)
		for _, path := range missing {
			if err := r.apiJSON(ctx, input.Config, http.MethodDelete, "/v1/projects/"+encodeID(input.ProjectID)+"/files/"+encodePath(path), nil, nil); err != nil {
				return syncResult{}, err
			}
			deleted++
		}
	}
	var version projectVersion
	body := map[string]any{
		"message":      strings.TrimSpace(input.Message),
		"kind":         "manual",
		"source":       "user",
		"confirmPrune": false,
	}
	if body["message"] == "" {
		body["message"] = "local cli sync"
	}
	if err := r.apiJSON(ctx, input.Config, http.MethodPost, "/v1/projects/"+encodeID(input.ProjectID)+"/versions", body, &version); err != nil {
		return syncResult{}, err
	}
	result := syncResult{Uploaded: len(files), Deleted: deleted, VersionID: version.ID, Deploy: "none"}
	if input.Build {
		if err := r.apiJSON(ctx, input.Config, http.MethodPost, "/v1/projects/"+encodeID(input.ProjectID)+"/versions/"+encodeID(version.ID)+"/build", nil, nil); err != nil {
			return syncResult{}, err
		}
		result.Built = true
	}
	switch input.Deploy {
	case "preview":
		if err := r.apiJSON(ctx, input.Config, http.MethodPost, "/v1/projects/"+encodeID(input.ProjectID)+"/preview", nil, nil); err != nil {
			return syncResult{}, err
		}
		result.Deploy = "preview"
	case "production":
		if err := r.apiJSON(ctx, input.Config, http.MethodPost, "/v1/projects/"+encodeID(input.ProjectID)+"/production", nil, nil); err != nil {
			return syncResult{}, err
		}
		result.Deploy = "production"
	}
	return result, nil
}

func (r runner) watch(ctx context.Context, root string, runOnce func() error) error {
	var last string
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		hash, err := treeFingerprint(root)
		if err != nil {
			return err
		}
		if hash != last {
			if err := runOnce(); err != nil {
				_, _ = fmt.Fprintf(r.stderr, "sync failed: %v\n", err)
			} else {
				last = hash
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(defaultPoll):
		}
	}
}

type localFile struct {
	Path    string
	Content string
}

type projectFile struct {
	Path        string `json:"path"`
	ContentHash string `json:"contentHash"`
	Content     string `json:"content"`
	SizeBytes   int64  `json:"sizeBytes"`
	ContentType string `json:"contentType"`
}

type projectVersion struct {
	ID string `json:"id"`
}

func scanFiles(root string) ([]localFile, error) {
	files, _, err := scanFilesWithSkipped(root)
	return files, err
}

func scanFilesWithSkipped(root string) ([]localFile, map[string]struct{}, error) {
	var files []localFile
	skipped := map[string]struct{}{}
	skipPath := func(path string) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel != "." {
			skipped[rel] = struct{}{}
		}
		return nil
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if entry.IsDir() {
			if shouldIgnoreDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if shouldIgnoreFile(name) {
			return skipPath(path)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return skipPath(path)
		}
		if !entry.Type().IsRegular() {
			return skipPath(path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxSyncFileSize {
			return fmt.Errorf("%s exceeds maximum sync file size of %d bytes", path, maxSyncFileSize)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !isText(content) {
			return skipPath(path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, localFile{
			Path:    filepath.ToSlash(rel),
			Content: string(content),
		})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, skipped, nil
}

func shouldIgnoreDir(name string) bool {
	switch name {
	case ".git", ".shipable", "node_modules", "dist", "build", ".next", ".turbo", ".cache", "coverage", "vendor":
		return true
	default:
		return false
	}
}

func shouldIgnoreFile(name string) bool {
	if strings.HasPrefix(name, ".env") {
		return true
	}
	if strings.HasSuffix(name, ".tsbuildinfo") {
		return true
	}
	switch name {
	case ".DS_Store":
		return true
	default:
		return false
	}
}

func isText(content []byte) bool {
	if bytes.IndexByte(content, 0) >= 0 {
		return false
	}
	return utf8.Valid(content)
}

func safeErrorResponse(content []byte) string {
	message := strings.TrimSpace(string(content))
	runes := []rune(message)
	if len(runes) <= maxErrorResponseLength {
		return message
	}
	return string(runes[:maxErrorResponseLength]) + "...(truncated)"
}

func treeFingerprint(root string) (string, error) {
	files, err := scanFiles(root)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	for _, file := range files {
		_, _ = io.WriteString(hasher, file.Path)
		_, _ = hasher.Write([]byte{0})
		_, _ = io.WriteString(hasher, file.Content)
		_, _ = hasher.Write([]byte{0})
	}
	sum := hasher.Sum(nil)
	return hex.EncodeToString(sum), nil
}

func (r runner) apiJSON(ctx context.Context, cfg configFile, method string, apiPath string, body any, target any) error {
	var requestBody io.Reader
	if body != nil {
		content, err := json.Marshal(body)
		if err != nil {
			return err
		}
		requestBody = bytes.NewReader(content)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(cfg.APIURL, "/")+apiPath, requestBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if target != nil {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	content, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s failed with HTTP %d: %s", method, apiPath, resp.StatusCode, safeErrorResponse(content))
	}
	if target != nil && len(strings.TrimSpace(string(content))) > 0 {
		if err := json.Unmarshal(content, target); err != nil {
			return err
		}
	}
	return nil
}

func (r runner) apiStream(ctx context.Context, cfg configFile, apiPath string, target io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.APIURL, "/")+apiPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	req.Header.Set("Accept", "text/plain")
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		content, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return readErr
		}
		return fmt.Errorf("GET %s failed with HTTP %d: %s", apiPath, resp.StatusCode, safeErrorResponse(content))
	}
	_, err = io.Copy(writerOrDiscard(target), resp.Body)
	return err
}

func (r runner) workOSJSON(ctx context.Context, baseURL string, apiPath string, body any, target any) error {
	content, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+apiPath, bytes.NewReader(content))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return r.doJSON(req, target)
}

func (r runner) workOSForm(ctx context.Context, baseURL string, apiPath string, values url.Values, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+apiPath, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return r.doJSON(req, target)
}

func (r runner) doJSON(req *http.Request, target any) error {
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	content, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s failed with HTTP %d: %s", req.Method, req.URL.Path, resp.StatusCode, safeErrorResponse(content))
	}
	if target != nil && len(strings.TrimSpace(string(content))) > 0 {
		if err := json.Unmarshal(content, target); err != nil {
			return err
		}
	}
	return nil
}

func (r runner) loadConfig() (configFile, error) {
	envAccessToken := strings.TrimSpace(r.getenv("SHIPABLE_TOKEN"))
	cfg := configFile{
		APIURL:      strings.TrimRight(strings.TrimSpace(r.getenv("SHIPABLE_API_URL")), "/"),
		AccessToken: envAccessToken,
	}
	if cfg.APIURL == "" || cfg.AccessToken == "" {
		fileCfg, err := readConfigFile(r.configPath())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return configFile{}, err
		}
		if cfg.APIURL == "" {
			cfg.APIURL = fileCfg.APIURL
		}
		if cfg.AccessToken == "" {
			cfg.AccessToken = fileCfg.AccessToken
			cfg.RefreshToken = fileCfg.RefreshToken
			cfg.OrganizationID = fileCfg.OrganizationID
			cfg.WorkOSClientID = fileCfg.WorkOSClientID
			cfg.WorkOSAPIURL = fileCfg.WorkOSAPIURL
			cfg.AccessTokenExpiresAt = fileCfg.AccessTokenExpiresAt
		}
	}
	if cfg.APIURL == "" {
		cfg.APIURL = defaultAPIURL
	}
	if cfg.AccessToken == "" {
		return configFile{}, errors.New("not authenticated; run shipable auth login --token-stdin or set SHIPABLE_TOKEN")
	}
	if cfg.RefreshToken != "" && cfg.WorkOSClientID != "" && accessTokenNeedsRefresh(cfg.AccessTokenExpiresAt) {
		refreshed, err := r.refreshConfigToken(context.Background(), cfg)
		if err != nil {
			return configFile{}, err
		}
		cfg = refreshed
		if err := writeJSONFile(r.configPath(), cfg, 0o600); err != nil {
			return configFile{}, err
		}
	}
	return cfg, nil
}

func (r runner) refreshConfigToken(ctx context.Context, cfg configFile) (configFile, error) {
	workOSBase := strings.TrimRight(strings.TrimSpace(cfg.WorkOSAPIURL), "/")
	if workOSBase == "" {
		workOSBase = workOSAPIURL
	}
	values := url.Values{}
	values.Set("client_id", cfg.WorkOSClientID)
	values.Set("refresh_token", cfg.RefreshToken)
	values.Set("grant_type", "refresh_token")
	var token tokenResponse
	if err := r.workOSForm(ctx, workOSBase, "/user_management/authenticate", values, &token); err != nil {
		return configFile{}, err
	}
	if token.Error != "" {
		return configFile{}, errors.New(token.Error)
	}
	if token.AccessToken == "" {
		return configFile{}, errors.New("WorkOS refresh response did not include access_token")
	}
	cfg.AccessToken = token.AccessToken
	if token.RefreshToken != "" {
		cfg.RefreshToken = token.RefreshToken
	}
	if token.OrganizationID != "" {
		cfg.OrganizationID = token.OrganizationID
	}
	cfg.WorkOSAPIURL = workOSBase
	cfg.AccessTokenExpiresAt = accessTokenExpiresAt(token.AccessToken, token.ExpiresIn)
	return cfg, nil
}

func readConfigFile(path string) (configFile, error) {
	var cfg configFile
	content, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(content, &cfg); err != nil {
		return cfg, err
	}
	cfg.APIURL = strings.TrimRight(strings.TrimSpace(cfg.APIURL), "/")
	cfg.AccessToken = strings.TrimSpace(cfg.AccessToken)
	cfg.RefreshToken = strings.TrimSpace(cfg.RefreshToken)
	cfg.OrganizationID = strings.TrimSpace(cfg.OrganizationID)
	cfg.WorkOSClientID = strings.TrimSpace(cfg.WorkOSClientID)
	cfg.WorkOSAPIURL = strings.TrimRight(strings.TrimSpace(cfg.WorkOSAPIURL), "/")
	return cfg, nil
}

func discoverWorkOSClientID(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		value := readDotenvValue(filepath.Join(dir, "apps", "api", ".env"), "WORKOS_CLIENT_ID")
		if value != "" {
			return value
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func readDotenvValue(path string, key string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || !strings.HasPrefix(line, prefix) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		value = strings.Trim(value, `"'`)
		switch {
		case value == "":
			return ""
		// Reject common placeholder values without rejecting real IDs that only contain these words.
		case isPlaceholderValue(value):
			return ""
		default:
			return value
		}
	}
	return ""
}

func isPlaceholderValue(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "example", "placeholder", "changeme":
		return true
	}
	for _, prefix := range []string{"example-", "example_", "example.", "placeholder-", "placeholder_", "placeholder."} {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return strings.HasPrefix(normalized, "changeme")
}

func accessTokenExpiresAt(accessToken string, expiresIn int) string {
	if exp := jwtExpiresAt(accessToken); !exp.IsZero() {
		return exp.Format(time.RFC3339)
	}
	if expiresIn > 0 {
		return time.Now().Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339)
	}
	return ""
}

func accessTokenNeedsRefresh(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return false
	}
	return time.Now().Add(time.Minute).After(expiresAt)
}

func jwtExpiresAt(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp any `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}
	}
	switch exp := claims.Exp.(type) {
	case float64:
		return time.Unix(int64(exp), 0)
	case string:
		seconds, err := strconv.ParseInt(exp, 10, 64)
		if err != nil {
			return time.Time{}
		}
		return time.Unix(seconds, 0)
	default:
		return time.Time{}
	}
}

func readProjectLink(root string) (projectLinkFile, error) {
	var link projectLinkFile
	content, err := os.ReadFile(filepath.Join(root, ".shipable", "project.json"))
	if err != nil {
		return link, fmt.Errorf("missing project id; pass --project or run shipable link: %w", err)
	}
	if err := json.Unmarshal(content, &link); err != nil {
		return link, err
	}
	link.ProjectID = strings.TrimSpace(link.ProjectID)
	return link, nil
}

func (r runner) configPath() string {
	if value := strings.TrimSpace(r.getenv("SHIPABLE_CONFIG")); value != "" {
		return value
	}
	if value := strings.TrimSpace(r.getenv("XDG_CONFIG_HOME")); value != "" {
		return filepath.Join(value, "shipable", "config.json")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "shipable", "config.json")
	}
	return filepath.Join(".shipable", "config.json")
}

func (r runner) getenv(key string) string {
	if r.env != nil {
		return r.env[key]
	}
	return os.Getenv(key)
}

func writeJSONFile(path string, payload any, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	temp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	cleanup = false
	return os.Chmod(path, mode)
}

func encodeID(id string) string {
	return url.PathEscape(id)
}

func encodePath(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func writerOrDiscard(writer io.Writer) io.Writer {
	if writer == nil {
		return io.Discard
	}
	return writer
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func defaultExec(name string, args []string, options execOptions) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = options.Dir
	cmd.Stdout = options.Stdout
	cmd.Stderr = options.Stderr
	cmd.Stdin = options.Stdin
	if len(options.Env) > 0 {
		cmd.Env = mergeEnv(os.Environ(), options.Env)
	}
	return cmd.Run()
}

func mergeEnv(parent []string, overrides []string) []string {
	values := make(map[string]string, len(parent)+len(overrides))
	order := make([]string, 0, len(parent)+len(overrides))
	invalid := make([]string, 0)

	for _, entry := range parent {
		key, ok := envKey(entry)
		if !ok {
			invalid = append(invalid, entry)
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = entry
	}
	for _, entry := range overrides {
		key, ok := envKey(entry)
		if !ok {
			invalid = append(invalid, entry)
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = entry
	}

	merged := make([]string, 0, len(order)+len(invalid))
	for _, key := range order {
		merged = append(merged, values[key])
	}
	return append(merged, invalid...)
}

func envKey(entry string) (string, bool) {
	index := strings.IndexByte(entry, '=')
	if index <= 0 {
		return "", false
	}
	return entry[:index], true
}
