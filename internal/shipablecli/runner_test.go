package shipablecli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersionCommandPrintsBuildInfo(t *testing.T) {
	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"version"},
		Stdout: &stdout,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run version error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "shipable dev") || !strings.Contains(got, "commit unknown") {
		t.Fatalf("version output = %q", got)
	}
}

func TestVersionFlagPrintsBuildInfo(t *testing.T) {
	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"--version"},
		Stdout: &stdout,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run --version error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "shipable dev") {
		t.Fatalf("--version output = %q", got)
	}
}

func TestAuthLoginPersistsTokenAndAPIURL(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.json")

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"auth", "login", "--token-stdin", "--api-url", "https://api.shipable.test"},
		Stdout: &stdout,
		Stderr: io.Discard,
		Stdin:  strings.NewReader("token_test\n"),
		Env: map[string]string{
			"SHIPABLE_CONFIG": configPath,
		},
	})
	if err != nil {
		t.Fatalf("auth login failed: %v", err)
	}

	var cfg configFile
	readJSON(t, configPath, &cfg)
	if cfg.APIURL != "https://api.shipable.test" {
		t.Fatalf("api url = %q", cfg.APIURL)
	}
	if cfg.AccessToken != "token_test" {
		t.Fatalf("access token = %q", cfg.AccessToken)
	}
	if !strings.Contains(stdout.String(), "Authenticated") {
		t.Fatalf("stdout did not confirm auth: %s", stdout.String())
	}
}

func TestAuthLoginUsesWorkOSDeviceFlowWithoutToken(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.json")

	var requests []recordedRequest
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		body := readRequestBody(t, r)
		requests = append(requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.EscapedPath(),
			Body:   string(body),
			Auth:   r.Header.Get("Authorization"),
		})
		switch {
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/user_management/authorize/device":
			if !strings.Contains(string(body), `"client_id":"client_test"`) {
				t.Fatalf("authorize body did not include client id: %s", string(body))
			}
			return http.StatusOK, `{"device_code":"dev_123","user_code":"ABCD-EFGH","verification_uri":"https://auth.workos.test/device","verification_uri_complete":"https://auth.workos.test/device?user_code=ABCD-EFGH","expires_in":600,"interval":0}`
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/user_management/authenticate":
			if !strings.Contains(string(body), "device_code=dev_123") {
				t.Fatalf("authenticate body did not include device code: %s", string(body))
			}
			return http.StatusOK, `{"access_token":"access_device","refresh_token":"refresh_device","organization_id":"org_123","expires_in":3600}`
		default:
			t.Fatalf("unexpected request %s %s body=%s", r.Method, r.URL.EscapedPath(), string(body))
			return http.StatusInternalServerError, ``
		}
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"auth", "login", "--api-url", "https://api.shipable.test"},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_CONFIG":           configPath,
			"SHIPABLE_WORKOS_CLIENT_ID": "client_test",
			"SHIPABLE_WORKOS_API_URL":   "https://auth.workos.test",
		},
	})
	if err != nil {
		t.Fatalf("auth login failed: %v", err)
	}

	assertRequestOrder(t, requests, []string{
		"POST /user_management/authorize/device",
		"POST /user_management/authenticate",
	})
	if !strings.Contains(stdout.String(), "https://auth.workos.test/device?user_code=ABCD-EFGH") ||
		!strings.Contains(stdout.String(), "ABCD-EFGH") {
		t.Fatalf("stdout did not show device login instructions: %s", stdout.String())
	}
	var cfg configFile
	readJSON(t, configPath, &cfg)
	if cfg.AccessToken != "access_device" {
		t.Fatalf("access token = %q", cfg.AccessToken)
	}
	if cfg.RefreshToken != "refresh_device" {
		t.Fatalf("refresh token = %q", cfg.RefreshToken)
	}
	if cfg.OrganizationID != "org_123" {
		t.Fatalf("organization id = %q", cfg.OrganizationID)
	}
	if cfg.WorkOSClientID != "client_test" {
		t.Fatalf("client id = %q", cfg.WorkOSClientID)
	}
}

func TestStartDeviceFlowUsesProdDefaultClientID(t *testing.T) {
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		body := readRequestBody(t, r)
		if r.URL.EscapedPath() == "/user_management/authorize/device" {
			if !strings.Contains(string(body), `"client_id":"client_01KSXAMHC5HC8F6J7D1GZMAA07"`) {
				t.Fatalf("authorize did not use prod default client id: %s", string(body))
			}
			return http.StatusOK, `{"device_code":"dev_1","user_code":"AAAA-BBBB","verification_uri":"https://auth.workos.test/device","expires_in":600,"interval":0}`
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
		return http.StatusInternalServerError, ``
	})
	rr := runner{
		client: client,
		stdout: io.Discard,
		stderr: io.Discard,
		env:    map[string]string{"SHIPABLE_WORKOS_API_URL": "https://auth.workos.test"},
	}
	flow, err := rr.startDeviceFlow(context.Background(), deviceLoginInput{APIURL: "https://api.shipable.test"})
	if err != nil {
		t.Fatalf("startDeviceFlow failed: %v", err)
	}
	if flow.clientID != "client_01KSXAMHC5HC8F6J7D1GZMAA07" {
		t.Fatalf("client id = %q, want prod default", flow.clientID)
	}
}

func TestLinkWritesProjectFile(t *testing.T) {
	tmp := t.TempDir()

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"link", "--project", "proj_123", "--dir", tmp},
		Stdout: &stdout,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("link failed: %v", err)
	}

	var link projectLinkFile
	readJSON(t, filepath.Join(tmp, ".shipable", "project.json"), &link)
	if link.ProjectID != "proj_123" {
		t.Fatalf("project id = %q", link.ProjectID)
	}
	if !strings.Contains(stdout.String(), "proj_123") {
		t.Fatalf("stdout did not mention project: %s", stdout.String())
	}
}

func TestTemplatesListsAvailableTemplates(t *testing.T) {
	var requests []recordedRequest
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.EscapedPath(), Auth: r.Header.Get("Authorization")})
		if r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/project-templates" {
			return http.StatusOK, `[{"id":"dashboard-go-workos-postgres-redis","name":"Go WorkOS Dashboard","description":"Go, WorkOS, Postgres, and Dragonfly-compatible Redis.","category":"Dashboard","tags":["react","go","postgres","redis","workos"],"variantLabel":"Go + WorkOS","sortOrder":10,"status":"active"}]`
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
		return http.StatusInternalServerError, ``
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"templates"},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL": "https://api.shipable.test",
			"SHIPABLE_TOKEN":   "token_test",
		},
	})
	if err != nil {
		t.Fatalf("templates failed: %v", err)
	}

	assertRequestOrder(t, requests, []string{"GET /v1/project-templates"})
	if !strings.Contains(stdout.String(), "dashboard-go-workos-postgres-redis") ||
		!strings.Contains(stdout.String(), "Go WorkOS Dashboard") {
		t.Fatalf("stdout did not list template: %s", stdout.String())
	}
}

func TestTemplatesFiltersServiceShape(t *testing.T) {
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		if r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/project-templates" {
			return http.StatusOK, `[
				{"id":"starter-static","name":"Static Frontend","description":"Static site","category":"Frontend","tags":["static"],"status":"active"},
				{"id":"starter-node-api","name":"Node API","description":"Service API","category":"Backend","tags":["api","service"],"status":"active"},
				{"id":"dashboard-node-workos-postgres","name":"Dashboard","description":"Fullstack app","category":"Dashboard","tags":["react","node","postgres"],"status":"active"}
			]`
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
		return http.StatusInternalServerError, ``
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"templates", "--shape", "service"},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL": "https://api.shipable.test",
			"SHIPABLE_TOKEN":   "token_test",
		},
	})
	if err != nil {
		t.Fatalf("templates --shape service failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "starter-node-api") ||
		strings.Contains(stdout.String(), "starter-static") ||
		strings.Contains(stdout.String(), "dashboard-node-workos-postgres") {
		t.Fatalf("stdout did not filter service templates: %s", stdout.String())
	}
}

func TestTemplatesPrintsJSON(t *testing.T) {
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		if r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/project-templates" {
			return http.StatusOK, `[{"id":"starter-static","name":"Static","description":"Static app","category":"Starter","tags":["react"],"variantLabel":"Static","sortOrder":1,"status":"active"}]`
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
		return http.StatusInternalServerError, ``
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"templates", "--json"},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL": "https://api.shipable.test",
			"SHIPABLE_TOKEN":   "token_test",
		},
	})
	if err != nil {
		t.Fatalf("templates --json failed: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &decoded); err != nil {
		t.Fatalf("stdout is not json: %v; body=%s", err, stdout.String())
	}
	if len(decoded) != 1 || decoded[0]["id"] != "starter-static" {
		t.Fatalf("decoded templates = %#v", decoded)
	}
}

func TestCreateScaffoldsTemplateLinksAndDeploysPreview(t *testing.T) {
	tmp := t.TempDir()
	var requests []recordedRequest
	jobPolls := 0
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		body := readRequestBody(t, r)
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.EscapedPath(), Body: string(body), Auth: r.Header.Get("Authorization")})
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/project-templates/dashboard-go-workos-postgres-redis/files":
			return http.StatusOK, `{"template":{"id":"dashboard-go-workos-postgres-redis","name":"Go WorkOS Dashboard","description":"Go stack","category":"Dashboard","tags":["react","go","postgres","redis","workos"],"variantLabel":"Go + WorkOS","sortOrder":10,"status":"active"},"files":[{"path":"package.json","contentHash":"sha256:pkg","content":"{\"scripts\":{\"build\":\"vite build\"}}"},{"path":"src/App.tsx","contentHash":"sha256:app","content":"export default function App() { return <h1>Go</h1> }"},{"path":"shipable.app.json","contentHash":"sha256:manifest","content":"{\"schemaVersion\":2,\"name\":\"go\",\"components\":[{\"id\":\"web\",\"type\":\"frontend\",\"build\":{\"command\":\"npm run build\",\"output\":\"dist\"}},{\"id\":\"api\",\"type\":\"container\",\"runtime\":\"docker\",\"build\":{\"strategy\":\"dockerfile\",\"dockerfile\":\"Dockerfile\"},\"run\":{\"port\":8080,\"healthCheck\":\"/api/health\"},\"dependsOn\":[\"db\",\"redis\"]},{\"id\":\"db\",\"type\":\"database\",\"engine\":\"postgres\",\"provider\":\"managed\"},{\"id\":\"redis\",\"type\":\"cache\",\"engine\":\"redis\",\"provider\":\"managed\"}],\"routes\":[{\"component\":\"web\",\"environment\":\"preview\"}]}"}]}`
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects":
			if !strings.Contains(string(body), `"name":"ops-dashboard"`) ||
				!strings.Contains(string(body), `"template":"dashboard-go-workos-postgres-redis"`) ||
				!strings.Contains(string(body), `"creationMode":"template"`) ||
				strings.Contains(string(body), "manifestJson") {
				t.Fatalf("create body = %s", string(body))
			}
			return http.StatusCreated, `{"id":"proj_123","name":"ops-dashboard","templateType":"dashboard-go-workos-postgres-redis","latestVersionId":"ver_1"}`
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects/proj_123/preview":
			return http.StatusAccepted, `{"id":"proj_123"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/jobs/latest":
			jobPolls++
			if jobPolls == 1 {
				return http.StatusOK, `{"id":"job_1","projectId":"proj_123","versionId":"ver_1","type":"build","status":"running"}`
			}
			return http.StatusOK, `{"id":"job_1","projectId":"proj_123","versionId":"ver_1","type":"build","status":"succeeded"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/preview":
			return http.StatusOK, `{"deploymentId":"dep_1","projectId":"proj_123","versionId":"ver_1","status":"ready","serviceStatus":"ready","url":"https://preview.shipable.test/preview/dep_1/token/","serviceUrl":"https://preview.shipable.test/_shipable/service/preview/dep_1/token"}`
		default:
			t.Fatalf("unexpected request %s %s body=%s", r.Method, r.URL.EscapedPath(), string(body))
			return http.StatusInternalServerError, ``
		}
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"create", "--name", "ops-dashboard", "--template", "dashboard-go-workos-postgres-redis", "--dir", tmp, "--deploy", "preview", "--wait"},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL":          "https://api.shipable.test",
			"SHIPABLE_TOKEN":            "token_test",
			"SHIPABLE_POLL_INTERVAL_MS": "1",
		},
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	assertRequestOrder(t, requests, []string{
		"GET /v1/project-templates/dashboard-go-workos-postgres-redis/files",
		"POST /v1/projects",
		"POST /v1/projects/proj_123/preview",
		"GET /v1/projects/proj_123/jobs/latest",
		"GET /v1/projects/proj_123/jobs/latest",
		"GET /v1/projects/proj_123/preview",
	})
	if got := readFileString(t, filepath.Join(tmp, "src", "App.tsx")); !strings.Contains(got, "<h1>Go</h1>") {
		t.Fatalf("scaffolded App.tsx = %s", got)
	}
	var link projectLinkFile
	readJSON(t, filepath.Join(tmp, ".shipable", "project.json"), &link)
	if link.ProjectID != "proj_123" {
		t.Fatalf("project id = %q", link.ProjectID)
	}
	if !strings.Contains(stdout.String(), "https://preview.shipable.test/preview/dep_1/token/") {
		t.Fatalf("stdout did not include preview url: %s", stdout.String())
	}
}

func TestCreateServiceShapeUsesBackendTemplate(t *testing.T) {
	tmp := t.TempDir()
	var requests []recordedRequest
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		body := readRequestBody(t, r)
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.EscapedPath(), Body: string(body)})
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/project-templates/starter-node-api/files":
			return http.StatusOK, `{"template":{"id":"starter-node-api","name":"Node API","description":"Backend API","category":"Backend","tags":["api","service"],"status":"active"},"files":[{"path":"package.json","content":"{\"scripts\":{\"start\":\"node api/server.js\"}}"},{"path":"shipable.app.json","content":"{\"schemaVersion\":2,\"name\":\"node-api\",\"components\":[{\"id\":\"api\",\"type\":\"service\",\"runtime\":\"node\",\"run\":{\"command\":\"npm start\",\"port\":8787}}],\"routes\":[{\"component\":\"api\",\"environment\":\"preview\"}]}"}]}`
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects":
			if !strings.Contains(string(body), `"template":"starter-node-api"`) {
				t.Fatalf("create body = %s", string(body))
			}
			return http.StatusCreated, `{"id":"proj_api","name":"mobile-api","templateType":"starter-node-api","latestVersionId":"ver_1"}`
		default:
			t.Fatalf("unexpected request %s %s body=%s", r.Method, r.URL.EscapedPath(), string(body))
			return http.StatusInternalServerError, ``
		}
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"create", "--name", "mobile-api", "--shape", "service", "--dir", tmp},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL": "https://api.shipable.test",
			"SHIPABLE_TOKEN":   "token_test",
		},
	})
	if err != nil {
		t.Fatalf("create --shape service failed: %v", err)
	}
	assertRequestOrder(t, requests, []string{
		"GET /v1/project-templates/starter-node-api/files",
		"POST /v1/projects",
	})
	if got := readFileString(t, filepath.Join(tmp, "shipable.app.json")); !strings.Contains(got, `"type":"service"`) {
		t.Fatalf("service manifest was not scaffolded: %s", got)
	}
	if !strings.Contains(stdout.String(), "Created project mobile-api") {
		t.Fatalf("stdout did not include create confirmation: %s", stdout.String())
	}
}

func TestCreateRejectsNonEmptyDirectoryWithoutForce(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "README.md"), "local work")
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		t.Fatalf("create should not call API for non-empty dir without --force: %s %s", r.Method, r.URL.EscapedPath())
		return http.StatusInternalServerError, ``
	})

	err := Run(RunOptions{
		Args:   []string{"create", "--name", "ops-dashboard", "--template", "dashboard-go-workos-postgres-redis", "--dir", tmp},
		Stdout: io.Discard,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL": "https://api.shipable.test",
			"SHIPABLE_TOKEN":   "token_test",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("create error = %v, want non-empty directory rejection", err)
	}
}

func TestSyncUploadsBuildsAndPublishesPreview(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "package.json"), `{"scripts":{"build":"vite build"}}`)
	writeFile(t, filepath.Join(tmp, "src", "App.tsx"), `export function App() { return <h1>Hello</h1> }`)
	writeFile(t, filepath.Join(tmp, "node_modules", "ignored.js"), `ignored`)
	writeFile(t, filepath.Join(tmp, "tsconfig.tsbuildinfo"), `{"program":{"fileNames":[]}}`)

	var requests []recordedRequest
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		body := readRequestBody(t, r)
		requests = append(requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.EscapedPath(),
			Body:   string(body),
			Auth:   r.Header.Get("Authorization"),
		})
		switch {
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.EscapedPath(), "/v1/projects/proj_123/files/"):
			return http.StatusOK, `{"id":"proj_123"}`
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects/proj_123/versions":
			return http.StatusCreated, `{"id":"ver_123","versionNumber":7}`
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects/proj_123/versions/ver_123/build":
			return http.StatusAccepted, `{"id":"proj_123"}`
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects/proj_123/preview":
			return http.StatusAccepted, `{"id":"proj_123"}`
		default:
			t.Fatalf("unexpected request %s %s body=%s", r.Method, r.URL.EscapedPath(), string(body))
			return http.StatusInternalServerError, ``
		}
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"sync", "--project", "proj_123", "--dir", tmp, "--build", "--deploy", "preview", "--message", "local agent sync"},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL": "https://api.shipable.test",
			"SHIPABLE_TOKEN":   "token_test",
		},
	})
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	assertRequestOrder(t, requests, []string{
		"PUT /v1/projects/proj_123/files/package.json",
		"PUT /v1/projects/proj_123/files/src/App.tsx",
		"POST /v1/projects/proj_123/versions",
		"POST /v1/projects/proj_123/versions/ver_123/build",
		"POST /v1/projects/proj_123/preview",
	})
	for _, request := range requests {
		if request.Auth != "Bearer token_test" {
			t.Fatalf("auth header for %s %s = %q", request.Method, request.Path, request.Auth)
		}
	}
	if strings.Contains(joinRequestPaths(requests), "node_modules") {
		t.Fatalf("node_modules should be ignored: %v", requests)
	}
	if strings.Contains(joinRequestPaths(requests), "tsconfig.tsbuildinfo") {
		t.Fatalf("TypeScript build info should be ignored: %v", requests)
	}
	if !strings.Contains(stdout.String(), "version ver_123") {
		t.Fatalf("stdout did not mention version: %s", stdout.String())
	}
}

func TestSyncWaitsForPreviewAndPrintsURL(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "index.html"), `<div>ok</div>`)
	jobPolls := 0
	var requests []recordedRequest
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		body := readRequestBody(t, r)
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.EscapedPath(), Body: string(body)})
		switch {
		case r.Method == http.MethodPut && r.URL.EscapedPath() == "/v1/projects/proj_123/files/index.html":
			return http.StatusOK, `{"id":"proj_123"}`
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects/proj_123/versions":
			return http.StatusCreated, `{"id":"ver_123"}`
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects/proj_123/versions/ver_123/build":
			return http.StatusAccepted, `{"id":"proj_123"}`
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects/proj_123/preview":
			return http.StatusAccepted, `{"id":"proj_123"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/jobs/latest":
			jobPolls++
			if jobPolls == 1 {
				return http.StatusOK, `{"id":"job_1","projectId":"proj_123","versionId":"ver_123","type":"build","status":"running"}`
			}
			return http.StatusOK, `{"id":"job_1","projectId":"proj_123","versionId":"ver_123","type":"build","status":"succeeded"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/preview":
			return http.StatusOK, `{"deploymentId":"dep_1","projectId":"proj_123","versionId":"ver_123","status":"ready","serviceStatus":"ready","url":"https://preview.shipable.test/app"}`
		default:
			t.Fatalf("unexpected request %s %s body=%s", r.Method, r.URL.EscapedPath(), string(body))
			return http.StatusInternalServerError, ``
		}
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"sync", "--project", "proj_123", "--dir", tmp, "--build", "--deploy", "preview", "--wait"},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL":          "https://api.shipable.test",
			"SHIPABLE_TOKEN":            "token_test",
			"SHIPABLE_POLL_INTERVAL_MS": "1",
		},
	})
	if err != nil {
		t.Fatalf("sync --wait failed: %v", err)
	}

	assertRequestOrder(t, requests, []string{
		"PUT /v1/projects/proj_123/files/index.html",
		"POST /v1/projects/proj_123/versions",
		"POST /v1/projects/proj_123/versions/ver_123/build",
		"POST /v1/projects/proj_123/preview",
		"GET /v1/projects/proj_123/jobs/latest",
		"GET /v1/projects/proj_123/jobs/latest",
		"GET /v1/projects/proj_123/preview",
	})
	if !strings.Contains(stdout.String(), "Build job job_1 succeeded") ||
		!strings.Contains(stdout.String(), "https://preview.shipable.test/app") {
		t.Fatalf("stdout did not include job success and url: %s", stdout.String())
	}
}

func TestDeployProductionWaitsAndPrintsURL(t *testing.T) {
	tmp := t.TempDir()
	writeTestJSONFile(t, filepath.Join(tmp, ".shipable", "project.json"), projectLinkFile{ProjectID: "proj_123"})
	var requests []recordedRequest
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.EscapedPath()})
		switch {
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects/proj_123/production":
			return http.StatusCreated, `{"deploymentId":"dep_1","projectId":"proj_123","versionId":"ver_123","status":"ready","serviceStatus":"ready","url":"https://prod.shipable.test/production/proj_123/"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/production":
			return http.StatusOK, `{"deploymentId":"dep_1","projectId":"proj_123","versionId":"ver_123","status":"ready","serviceStatus":"ready","url":"https://prod.shipable.test/production/proj_123/"}`
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
			return http.StatusInternalServerError, ``
		}
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"deploy", "--dir", tmp, "--target", "production", "--wait"},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL":          "https://api.shipable.test",
			"SHIPABLE_TOKEN":            "token_test",
			"SHIPABLE_POLL_INTERVAL_MS": "1",
		},
	})
	if err != nil {
		t.Fatalf("deploy production failed: %v", err)
	}
	assertRequestOrder(t, requests, []string{
		"POST /v1/projects/proj_123/production",
		"GET /v1/projects/proj_123/production",
	})
	if !strings.Contains(stdout.String(), "https://prod.shipable.test/production/proj_123/") {
		t.Fatalf("stdout did not include production url: %s", stdout.String())
	}
}

func TestDeployWaitsForAllServicesAndPrintsComponentEndpoints(t *testing.T) {
	tmp := t.TempDir()
	writeTestJSONFile(t, filepath.Join(tmp, ".shipable", "project.json"), projectLinkFile{ProjectID: "proj_123"})
	polls := 0
	var requests []recordedRequest
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.EscapedPath()})
		switch {
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects/proj_123/production":
			return http.StatusAccepted, `{"deploymentId":"dep_prod","projectId":"proj_123","status":"deploying"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/production":
			polls++
			if polls == 1 {
				return http.StatusOK, `{"deploymentId":"dep_prod","projectId":"proj_123","status":"ready","url":"https://prod.shipable.test/app","services":[{"componentId":"api","status":"ready","url":"https://prod.shipable.test/_shipable/service/production/proj_123/components/api"},{"componentId":"worker","status":"deploying","url":""}]}`
			}
			return http.StatusOK, `{"deploymentId":"dep_prod","projectId":"proj_123","status":"ready","url":"https://prod.shipable.test/app","services":[{"componentId":"api","status":"ready","url":"https://prod.shipable.test/_shipable/service/production/proj_123/components/api","logsUrl":"/projects/proj_123/deployments/dep_prod/service-logs?componentId=api"},{"componentId":"worker","status":"ready","url":"https://prod.shipable.test/_shipable/service/production/proj_123/components/worker","logsUrl":"/projects/proj_123/deployments/dep_prod/service-logs?componentId=worker"}]}`
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
			return http.StatusInternalServerError, ``
		}
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"deploy", "--dir", tmp, "--target", "production", "--wait"},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL":          "https://api.shipable.test",
			"SHIPABLE_TOKEN":            "token_test",
			"SHIPABLE_POLL_INTERVAL_MS": "1",
		},
	})
	if err != nil {
		t.Fatalf("deploy --wait failed: %v", err)
	}
	assertRequestOrder(t, requests, []string{
		"POST /v1/projects/proj_123/production",
		"GET /v1/projects/proj_123/production",
		"GET /v1/projects/proj_123/production",
	})
	if !strings.Contains(stdout.String(), "api ready https://prod.shipable.test/_shipable/service/production/proj_123/components/api") ||
		!strings.Contains(stdout.String(), "worker ready https://prod.shipable.test/_shipable/service/production/proj_123/components/worker") ||
		!strings.Contains(stdout.String(), "componentId=worker") {
		t.Fatalf("stdout did not include service endpoints and logs: %s", stdout.String())
	}
}

func TestGenerateWaitPullAndDeployPreview(t *testing.T) {
	tmp := t.TempDir()
	writeTestJSONFile(t, filepath.Join(tmp, ".shipable", "project.json"), projectLinkFile{ProjectID: "proj_123"})
	writeFile(t, filepath.Join(tmp, "src", "App.tsx"), "old app")
	generationPolls := 0
	var requests []recordedRequest
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		body := readRequestBody(t, r)
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.EscapedPath(), Body: string(body)})
		switch {
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects/proj_123/generations":
			if !strings.Contains(string(body), "Make it faster") {
				t.Fatalf("generation body = %s", string(body))
			}
			return http.StatusAccepted, `{"id":"gen_1","projectId":"proj_123","status":"queued","prompt":"Make it faster"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/generations/latest":
			generationPolls++
			if generationPolls == 1 {
				return http.StatusOK, `{"id":"gen_1","projectId":"proj_123","status":"generating_files","prompt":"Make it faster"}`
			}
			return http.StatusOK, `{"id":"gen_1","projectId":"proj_123","status":"succeeded","prompt":"Make it faster","targetVersionId":"ver_gen"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/files":
			return http.StatusOK, `[{"path":"src/App.tsx","content":"new app","contentHash":"sha256:new"},{"path":"shipable.app.json","content":"{}","contentHash":"sha256:manifest"}]`
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects/proj_123/preview":
			return http.StatusAccepted, `{"id":"proj_123"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/jobs/latest":
			return http.StatusOK, `{"id":"job_1","projectId":"proj_123","versionId":"ver_gen","type":"build","status":"succeeded"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/preview":
			return http.StatusOK, `{"deploymentId":"dep_1","projectId":"proj_123","versionId":"ver_gen","status":"ready","serviceStatus":"ready","url":"https://preview.shipable.test/generated"}`
		default:
			t.Fatalf("unexpected request %s %s body=%s", r.Method, r.URL.EscapedPath(), string(body))
			return http.StatusInternalServerError, ``
		}
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"generate", "--dir", tmp, "--prompt", "Make it faster", "--wait", "--pull", "--deploy", "preview"},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL":          "https://api.shipable.test",
			"SHIPABLE_TOKEN":            "token_test",
			"SHIPABLE_POLL_INTERVAL_MS": "1",
		},
	})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if got := readFileString(t, filepath.Join(tmp, "src", "App.tsx")); got != "new app" {
		t.Fatalf("pulled App.tsx = %q", got)
	}
	if !strings.Contains(stdout.String(), "Generation gen_1 succeeded") ||
		!strings.Contains(stdout.String(), "https://preview.shipable.test/generated") {
		t.Fatalf("stdout did not include generation and preview: %s", stdout.String())
	}
}

func TestStatusPrintsProjectAndDeploymentURLs(t *testing.T) {
	tmp := t.TempDir()
	writeTestJSONFile(t, filepath.Join(tmp, ".shipable", "project.json"), projectLinkFile{ProjectID: "proj_123"})
	var requests []recordedRequest
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.EscapedPath()})
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123":
			return http.StatusOK, `{"id":"proj_123","name":"Ops Dashboard","status":"ready","latestVersionId":"ver_123","activeVersionId":"ver_123"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/readiness":
			return http.StatusOK, `{"status":"production_ready","phase":"ready","blockers":[],"warnings":[],"checks":{"manifest":{"status":"ok"}}}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/preview":
			return http.StatusOK, `{"deploymentId":"dep_prev","projectId":"proj_123","versionId":"ver_123","status":"ready","serviceStatus":"ready","url":"https://preview.shipable.test/app"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/production":
			return http.StatusOK, `{"deploymentId":"dep_prod","projectId":"proj_123","versionId":"ver_123","status":"ready","serviceStatus":"ready","url":"https://prod.shipable.test/app"}`
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
			return http.StatusInternalServerError, ``
		}
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"status", "--dir", tmp},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL": "https://api.shipable.test",
			"SHIPABLE_TOKEN":   "token_test",
		},
	})
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	assertRequestOrder(t, requests, []string{
		"GET /v1/projects/proj_123",
		"GET /v1/projects/proj_123/readiness",
		"GET /v1/projects/proj_123/preview",
		"GET /v1/projects/proj_123/production",
	})
	if !strings.Contains(stdout.String(), "Ops Dashboard") ||
		!strings.Contains(stdout.String(), "Readiness: production_ready (ready)") ||
		!strings.Contains(stdout.String(), "https://preview.shipable.test/app") ||
		!strings.Contains(stdout.String(), "https://prod.shipable.test/app") {
		t.Fatalf("stdout did not include status fields: %s", stdout.String())
	}
}

func TestStatusPrintsMultiServiceDeployments(t *testing.T) {
	tmp := t.TempDir()
	writeTestJSONFile(t, filepath.Join(tmp, ".shipable", "project.json"), projectLinkFile{ProjectID: "proj_123"})
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123":
			return http.StatusOK, `{"id":"proj_123","name":"Backend Stack","status":"ready","latestVersionId":"ver_123"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/readiness":
			return http.StatusOK, `{"status":"production_ready","phase":"ready","blockers":[],"warnings":[],"checks":{"manifest":{"status":"ok"}}}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/preview":
			return http.StatusOK, `{"deploymentId":"dep_prev","projectId":"proj_123","versionId":"ver_123","status":"ready","url":"https://preview.shipable.test/preview/dep/token/","services":[{"componentId":"api","runtimeKind":"node_service","status":"ready","url":"https://preview.shipable.test/_shipable/service/preview/dep/token/components/api","logsUrl":"/projects/proj_123/deployments/dep_prev/service-logs?componentId=api"},{"componentId":"jobs","runtimeKind":"docker_service","status":"ready","url":"https://preview.shipable.test/_shipable/service/preview/dep/token/components/jobs","logsUrl":"/projects/proj_123/deployments/dep_prev/service-logs?componentId=jobs"}]}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/production":
			return http.StatusNotFound, `{}`
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
			return http.StatusInternalServerError, ``
		}
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"status", "--dir", tmp},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL": "https://api.shipable.test",
			"SHIPABLE_TOKEN":   "token_test",
		},
	})
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "Preview services:") ||
		!strings.Contains(stdout.String(), "api ready https://preview.shipable.test/_shipable/service/preview/dep/token/components/api") ||
		!strings.Contains(stdout.String(), "jobs ready https://preview.shipable.test/_shipable/service/preview/dep/token/components/jobs") ||
		!strings.Contains(stdout.String(), "service-logs?componentId=jobs") {
		t.Fatalf("stdout did not include multi-service status: %s", stdout.String())
	}
}

func TestStatusJSONIncludesReadiness(t *testing.T) {
	tmp := t.TempDir()
	writeTestJSONFile(t, filepath.Join(tmp, ".shipable", "project.json"), projectLinkFile{ProjectID: "proj_123"})
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123":
			return http.StatusOK, `{"id":"proj_123","name":"Ops Dashboard","status":"ready"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/readiness":
			return http.StatusOK, `{"status":"blocked","phase":"secrets","blockers":[{"code":"secret.missing","severity":"error","message":"missing secret","action":"Add the missing project secrets."}],"warnings":[],"checks":{"secrets":{"status":"blocked"}}}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/preview":
			return http.StatusNotFound, `{}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/production":
			return http.StatusNotFound, `{}`
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
			return http.StatusInternalServerError, ``
		}
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"status", "--dir", tmp, "--json"},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL": "https://api.shipable.test",
			"SHIPABLE_TOKEN":   "token_test",
		},
	})
	if err != nil {
		t.Fatalf("status --json failed: %v", err)
	}
	if !strings.Contains(stdout.String(), `"readiness"`) ||
		!strings.Contains(stdout.String(), `"status": "blocked"`) ||
		!strings.Contains(stdout.String(), `"code": "secret.missing"`) {
		t.Fatalf("stdout did not include readiness JSON: %s", stdout.String())
	}
}

func TestStatusJSONIncludesServices(t *testing.T) {
	tmp := t.TempDir()
	writeTestJSONFile(t, filepath.Join(tmp, ".shipable", "project.json"), projectLinkFile{ProjectID: "proj_123"})
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123":
			return http.StatusOK, `{"id":"proj_123","name":"Backend Stack","status":"ready"}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/readiness":
			return http.StatusOK, `{"status":"production_ready","phase":"ready","blockers":[],"warnings":[],"checks":{}}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/preview":
			return http.StatusOK, `{"deploymentId":"dep_prev","projectId":"proj_123","status":"ready","services":[{"componentId":"api","status":"ready","url":"https://preview.example/api"}]}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/production":
			return http.StatusNotFound, `{}`
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
			return http.StatusInternalServerError, ``
		}
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"status", "--dir", tmp, "--json"},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL": "https://api.shipable.test",
			"SHIPABLE_TOKEN":   "token_test",
		},
	})
	if err != nil {
		t.Fatalf("status --json failed: %v", err)
	}
	if !strings.Contains(stdout.String(), `"services"`) ||
		!strings.Contains(stdout.String(), `"componentId": "api"`) ||
		!strings.Contains(stdout.String(), `"url": "https://preview.example/api"`) {
		t.Fatalf("stdout did not include services JSON: %s", stdout.String())
	}
}

func TestServiceLogsStreamsSelectedComponent(t *testing.T) {
	tmp := t.TempDir()
	writeTestJSONFile(t, filepath.Join(tmp, ".shipable", "project.json"), projectLinkFile{ProjectID: "proj_123"})
	var requests []recordedRequest
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.EscapedPath(), Auth: r.Header.Get("Authorization")})
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/preview":
			return http.StatusOK, `{"deploymentId":"dep_prev","projectId":"proj_123","status":"ready","services":[{"componentId":"api","status":"ready","url":"https://preview.example/api","logsUrl":"/projects/proj_123/deployments/dep_prev/service-logs?componentId=api"},{"componentId":"worker","status":"ready","url":"https://preview.example/worker","logsUrl":"/projects/proj_123/deployments/dep_prev/service-logs?componentId=worker"}]}`
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/deployments/dep_prev/service-logs":
			if r.URL.Query().Get("componentId") != "worker" || r.URL.Query().Get("follow") != "1" {
				t.Fatalf("logs query = %s", r.URL.RawQuery)
			}
			if r.Header.Get("Accept") != "text/plain" {
				t.Fatalf("logs accept header = %q", r.Header.Get("Accept"))
			}
			return http.StatusOK, "worker ready\n"
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
			return http.StatusInternalServerError, ``
		}
	})

	var stdout strings.Builder
	err := Run(RunOptions{
		Args:   []string{"logs", "--dir", tmp, "--component", "worker", "--follow"},
		Stdout: &stdout,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL": "https://api.shipable.test",
			"SHIPABLE_TOKEN":   "token_test",
		},
	})
	if err != nil {
		t.Fatalf("logs failed: %v", err)
	}
	assertRequestOrder(t, requests, []string{
		"GET /v1/projects/proj_123/preview",
		"GET /v1/projects/proj_123/deployments/dep_prev/service-logs",
	})
	if stdout.String() != "worker ready\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestServiceLogsRequiresComponentForMultiServiceDeployment(t *testing.T) {
	tmp := t.TempDir()
	writeTestJSONFile(t, filepath.Join(tmp, ".shipable", "project.json"), projectLinkFile{ProjectID: "proj_123"})
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		if r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/preview" {
			return http.StatusOK, `{"deploymentId":"dep_prev","projectId":"proj_123","status":"ready","services":[{"componentId":"api","status":"ready","url":"https://preview.example/api","logsUrl":"/projects/proj_123/deployments/dep_prev/service-logs?componentId=api"},{"componentId":"worker","status":"ready","url":"https://preview.example/worker","logsUrl":"/projects/proj_123/deployments/dep_prev/service-logs?componentId=worker"}]}`
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
		return http.StatusInternalServerError, ``
	})

	err := Run(RunOptions{
		Args:   []string{"logs", "--dir", tmp},
		Stdout: io.Discard,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL": "https://api.shipable.test",
			"SHIPABLE_TOKEN":   "token_test",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "pass --component") {
		t.Fatalf("logs error = %v, want component prompt", err)
	}
}

func TestSyncRefreshesExpiredWorkOSAccessToken(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, filepath.Join(tmp, "index.html"), `<div>ok</div>`)
	writeTestJSONFile(t, configPath, configFile{
		APIURL:               "https://api.shipable.test",
		AccessToken:          "old_access",
		RefreshToken:         "refresh_device",
		WorkOSClientID:       "client_test",
		WorkOSAPIURL:         "https://auth.workos.test",
		AccessTokenExpiresAt: "2000-01-01T00:00:00Z",
	})

	var requests []recordedRequest
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		body := readRequestBody(t, r)
		requests = append(requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.EscapedPath(),
			Body:   string(body),
			Auth:   r.Header.Get("Authorization"),
		})
		switch {
		case r.Method == http.MethodPost && r.URL.String() == "https://auth.workos.test/user_management/authenticate":
			if !strings.Contains(string(body), "refresh_token=refresh_device") {
				t.Fatalf("refresh body did not include refresh token: %s", string(body))
			}
			return http.StatusOK, `{"access_token":"new_access","refresh_token":"new_refresh","organization_id":"org_123","expires_in":3600}`
		case r.Method == http.MethodPut && r.URL.EscapedPath() == "/v1/projects/proj_123/files/index.html":
			if r.Header.Get("Authorization") != "Bearer new_access" {
				t.Fatalf("sync used stale auth header: %q", r.Header.Get("Authorization"))
			}
			return http.StatusOK, `{"id":"proj_123"}`
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects/proj_123/versions":
			return http.StatusCreated, `{"id":"ver_123"}`
		default:
			t.Fatalf("unexpected request %s %s body=%s", r.Method, r.URL.String(), string(body))
			return http.StatusInternalServerError, ``
		}
	})

	err := Run(RunOptions{
		Args:   []string{"sync", "--project", "proj_123", "--dir", tmp},
		Stdout: io.Discard,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_CONFIG": configPath,
		},
	})
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	assertRequestOrder(t, requests, []string{
		"POST /user_management/authenticate",
		"PUT /v1/projects/proj_123/files/index.html",
		"POST /v1/projects/proj_123/versions",
	})
	var cfg configFile
	readJSON(t, configPath, &cfg)
	if cfg.AccessToken != "new_access" || cfg.RefreshToken != "new_refresh" {
		t.Fatalf("config was not refreshed: %+v", cfg)
	}
}

func TestSyncDeletesMissingRemoteDraftFiles(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "index.html"), `<div>ok</div>`)

	var requests []recordedRequest
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		body := readRequestBody(t, r)
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.EscapedPath(), Body: string(body)})
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/files":
			return http.StatusOK, `[{"path":"index.html"},{"path":"old.txt"}]`
		case r.Method == http.MethodPut && r.URL.EscapedPath() == "/v1/projects/proj_123/files/index.html":
			return http.StatusOK, `{"id":"proj_123"}`
		case r.Method == http.MethodDelete && r.URL.EscapedPath() == "/v1/projects/proj_123/files/old.txt":
			return http.StatusOK, `{"id":"proj_123"}`
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects/proj_123/versions":
			return http.StatusCreated, `{"id":"ver_123"}`
		default:
			t.Fatalf("unexpected request %s %s body=%s", r.Method, r.URL.EscapedPath(), string(body))
			return http.StatusInternalServerError, ``
		}
	})

	err := Run(RunOptions{
		Args:   []string{"sync", "--project", "proj_123", "--dir", tmp, "--delete"},
		Stdout: io.Discard,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL": "https://api.shipable.test",
			"SHIPABLE_TOKEN":   "token_test",
		},
	})
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	assertRequestOrder(t, requests, []string{
		"GET /v1/projects/proj_123/files",
		"PUT /v1/projects/proj_123/files/index.html",
		"DELETE /v1/projects/proj_123/files/old.txt",
		"POST /v1/projects/proj_123/versions",
	})
}

func TestSyncDeletePreservesSkippedRemoteFiles(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "index.html"), `<div>ok</div>`)
	writeFile(t, filepath.Join(tmp, "asset.bin"), string([]byte{0, 1, 2}))

	var requests []recordedRequest
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		body := readRequestBody(t, r)
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.EscapedPath(), Body: string(body)})
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/v1/projects/proj_123/files":
			return http.StatusOK, `[{"path":"index.html"},{"path":"asset.bin"},{"path":"node_modules/pkg/index.js"},{"path":"old.txt"}]`
		case r.Method == http.MethodPut && r.URL.EscapedPath() == "/v1/projects/proj_123/files/index.html":
			return http.StatusOK, `{"id":"proj_123"}`
		case r.Method == http.MethodDelete && r.URL.EscapedPath() == "/v1/projects/proj_123/files/old.txt":
			return http.StatusOK, `{"id":"proj_123"}`
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/v1/projects/proj_123/versions":
			return http.StatusCreated, `{"id":"ver_123"}`
		default:
			t.Fatalf("unexpected request %s %s body=%s", r.Method, r.URL.EscapedPath(), string(body))
			return http.StatusInternalServerError, ``
		}
	})

	err := Run(RunOptions{
		Args:   []string{"sync", "--project", "proj_123", "--dir", tmp, "--delete"},
		Stdout: io.Discard,
		Stderr: io.Discard,
		Client: client,
		Env: map[string]string{
			"SHIPABLE_API_URL": "https://api.shipable.test",
			"SHIPABLE_TOKEN":   "token_test",
		},
	})
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	assertRequestOrder(t, requests, []string{
		"GET /v1/projects/proj_123/files",
		"PUT /v1/projects/proj_123/files/index.html",
		"DELETE /v1/projects/proj_123/files/old.txt",
		"POST /v1/projects/proj_123/versions",
	})
}

func TestSafeProjectPathRejectsWindowsAbsolutePaths(t *testing.T) {
	t.Parallel()

	if target, ok := safeProjectPath(t.TempDir(), `C:/Users/alice/.ssh/id_rsa`); ok {
		t.Fatalf("safeProjectPath accepted Windows absolute path: %s", target)
	}
}

func TestWriteProjectFilesRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	_, _, err := writeProjectFiles(root, []projectFile{{Path: "linked/owned.txt", Content: "owned"}}, true)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("writeProjectFiles error = %v, want symlink rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "owned.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside file was written or stat failed: %v", statErr)
	}
}

func TestDevUpDelegatesToMakeLocal(t *testing.T) {
	var commands []executedCommand
	err := Run(RunOptions{
		Args:   []string{"dev", "up"},
		Stdout: io.Discard,
		Stderr: io.Discard,
		Exec: func(name string, args []string, options execOptions) error {
			commands = append(commands, executedCommand{Name: name, Args: args, Dir: options.Dir})
			return nil
		},
	})
	if err != nil {
		t.Fatalf("dev up failed: %v", err)
	}

	if len(commands) != 1 {
		t.Fatalf("commands = %+v", commands)
	}
	if commands[0].Name != "make" || strings.Join(commands[0].Args, " ") != "local" {
		t.Fatalf("command = %+v", commands[0])
	}
}

func TestWatchReturnsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := (runner{stderr: io.Discard}).watch(ctx, t.TempDir(), func() error {
		t.Fatal("runOnce should not be called after cancellation")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("watch error = %v, want context.Canceled", err)
	}
}

func TestScanFilesRejectsLargeFiles(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "large.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create large file: %v", err)
	}
	if err := file.Truncate(maxSyncFileSize + 1); err != nil {
		_ = file.Close()
		t.Fatalf("truncate large file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close large file: %v", err)
	}

	_, err = scanFiles(tmp)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum sync file size") {
		t.Fatalf("scanFiles error = %v, want large-file error", err)
	}
}

func TestReadDotenvValueOnlyRejectsPlaceholderValues(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")

	writeFile(t, path, "WORKOS_CLIENT_ID=client_example_live\n")
	if got := readDotenvValue(path, "WORKOS_CLIENT_ID"); got != "client_example_live" {
		t.Fatalf("client id containing example = %q", got)
	}

	writeFile(t, path, "WORKOS_CLIENT_ID=placeholder\n")
	if got := readDotenvValue(path, "WORKOS_CLIENT_ID"); got != "" {
		t.Fatalf("placeholder value = %q, want empty", got)
	}

	writeFile(t, path, "WORKOS_CLIENT_ID=example-client\n")
	if got := readDotenvValue(path, "WORKOS_CLIENT_ID"); got != "" {
		t.Fatalf("example-prefixed value = %q, want empty", got)
	}
}

func TestRequireSecureURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "https remote", url: "https://api.shipable.test", wantErr: false},
		{name: "https default", url: defaultAPIURL, wantErr: false},
		{name: "http localhost", url: "http://localhost:8080", wantErr: false},
		{name: "http loopback ipv4", url: "http://127.0.0.1:3000", wantErr: false},
		{name: "http loopback ipv6", url: "http://[::1]:3000", wantErr: false},
		{name: "http remote rejected", url: "http://api.shipable.test", wantErr: true},
		{name: "bad scheme rejected", url: "ftp://api.shipable.test", wantErr: true},
		{name: "empty rejected", url: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireSecureURL(tc.url)
			if tc.wantErr && err == nil {
				t.Fatalf("requireSecureURL(%q) = nil, want error", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("requireSecureURL(%q) = %v, want nil", tc.url, err)
			}
		})
	}
}

func TestSecureRedirectPolicy(t *testing.T) {
	mustURL := func(raw string) *url.URL {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		return parsed
	}
	newHop := func(target string, withAuth bool) *http.Request {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatalf("new request %q: %v", target, err)
		}
		if withAuth {
			req.Header.Set("Authorization", "Bearer token_secret")
		}
		return req
	}

	t.Run("rejects https to http downgrade on same host", func(t *testing.T) {
		req := newHop("http://api.shipable.test/login", true)
		via := []*http.Request{{URL: mustURL("https://api.shipable.test/v1/session")}}
		if err := secureRedirectPolicy(req, via); err == nil {
			t.Fatal("secureRedirectPolicy allowed https->http downgrade, want error")
		}
	})

	t.Run("rejects unsupported scheme", func(t *testing.T) {
		req := newHop("ftp://api.shipable.test/login", true)
		via := []*http.Request{{URL: mustURL("https://api.shipable.test/v1/session")}}
		if err := secureRedirectPolicy(req, via); err == nil {
			t.Fatal("secureRedirectPolicy allowed ftp scheme, want error")
		}
	})

	t.Run("strips auth on host change", func(t *testing.T) {
		req := newHop("https://evil.example.test/login", true)
		via := []*http.Request{{URL: mustURL("https://api.shipable.test/v1/session")}}
		if err := secureRedirectPolicy(req, via); err != nil {
			t.Fatalf("secureRedirectPolicy(host change) = %v, want nil", err)
		}
		if got := req.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization not stripped on host change: %q", got)
		}
	})

	t.Run("preserves auth on same origin", func(t *testing.T) {
		req := newHop("https://api.shipable.test/v1/session/", true)
		via := []*http.Request{{URL: mustURL("https://api.shipable.test/v1/session")}}
		if err := secureRedirectPolicy(req, via); err != nil {
			t.Fatalf("secureRedirectPolicy(same origin) = %v, want nil", err)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer token_secret" {
			t.Fatalf("Authorization changed on same origin: %q", got)
		}
	})

	t.Run("stops after too many redirects", func(t *testing.T) {
		req := newHop("https://api.shipable.test/v1/session", true)
		via := make([]*http.Request, 10)
		if err := secureRedirectPolicy(req, via); err == nil {
			t.Fatal("secureRedirectPolicy allowed 11th redirect, want error")
		}
	})
}

func TestAPIRequestDoesNotLeakTokenOnSchemeDowngradeRedirect(t *testing.T) {
	// End-to-end: a same-host https->http redirect must not carry the bearer
	// token. httptest binds both servers to loopback, where plaintext http is
	// permitted by requireSecureURL, so the load-bearing defense here is the
	// Authorization strip on the scheme change. The default client wires
	// secureRedirectPolicy, so even when the downgraded hop is followed the
	// token is removed before the request hits the plaintext channel.
	var sawAuthOverHTTP bool
	insecure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "" {
			sawAuthOverHTTP = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer insecure.Close()

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer token_secret" {
			t.Errorf("initial https hop missing auth header: %q", req.Header.Get("Authorization"))
		}
		http.Redirect(w, req, insecure.URL+req.URL.Path, http.StatusFound)
	}))
	defer secure.Close()

	transport := secure.Client().Transport
	client := &http.Client{
		Timeout:       30 * time.Second,
		Transport:     transport,
		CheckRedirect: secureRedirectPolicy,
	}

	if err := (runner{client: client}).apiJSON(context.Background(), configFile{
		APIURL:      secure.URL,
		AccessToken: "token_secret",
	}, http.MethodGet, "/v1/session", nil, nil); err != nil {
		t.Fatalf("apiJSON over loopback downgrade = %v, want nil", err)
	}
	if sawAuthOverHTTP {
		t.Fatal("bearer token leaked to plaintext http endpoint via redirect")
	}
}

func TestAPIJSONTruncatesErrorResponseBody(t *testing.T) {
	client := fakeHTTPClient(func(r *http.Request) (int, string) {
		return http.StatusBadRequest, strings.Repeat("a", maxErrorResponseLength+1) + "secret_tail"
	})

	err := (runner{client: client}).apiJSON(context.Background(), configFile{
		APIURL:      "https://api.shipable.test",
		AccessToken: "token_test",
	}, http.MethodGet, "/v1/projects/proj_123", nil, nil)
	if err == nil {
		t.Fatal("apiJSON error = nil, want HTTP error")
	}
	message := err.Error()
	if strings.Contains(message, "secret_tail") {
		t.Fatalf("error leaked response tail: %s", message)
	}
	if !strings.Contains(message, "...(truncated)") {
		t.Fatalf("error was not marked truncated: %s", message)
	}
}

func TestMergeEnvDeduplicatesAndOverrides(t *testing.T) {
	got := mergeEnv([]string{"A=1", "B=2", "A=old"}, []string{"B=override", "C=3"})
	want := []string{"A=old", "B=override", "C=3"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("mergeEnv = %#v, want %#v", got, want)
	}
}

type recordedRequest struct {
	Method string
	Path   string
	Body   string
	Auth   string
}

type executedCommand struct {
	Name string
	Args []string
	Dir  string
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func fakeHTTPClient(handler func(*http.Request) (int, string)) *http.Client {
	return &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			status, body := handler(request)
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    request,
			}, nil
		}),
	}
}

func readRequestBody(t *testing.T, request *http.Request) []byte {
	t.Helper()
	if request.Body == nil {
		return nil
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	return body
}

func readJSON(t *testing.T, path string, target any) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(content, target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func writeTestJSONFile(t *testing.T, path string, payload any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	content, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	if err := os.WriteFile(path, append(content, '\n'), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertRequestOrder(t *testing.T, requests []recordedRequest, want []string) {
	t.Helper()
	if len(requests) != len(want) {
		t.Fatalf("request count = %d want %d: %+v", len(requests), len(want), requests)
	}
	for i, request := range requests {
		got := request.Method + " " + request.Path
		if got != want[i] {
			t.Fatalf("request %d = %s want %s; all=%+v", i, got, want[i], requests)
		}
	}
}

func joinRequestPaths(requests []recordedRequest) string {
	paths := make([]string, 0, len(requests))
	for _, request := range requests {
		paths = append(paths, request.Path)
	}
	return strings.Join(paths, "\n")
}
