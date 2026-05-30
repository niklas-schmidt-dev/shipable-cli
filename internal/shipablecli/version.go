package shipablecli

import "fmt"

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"

	// defaultWorkOSClientID is the public WorkOS client id for the published
	// Shipable CLI app. It is a public OAuth identifier (the device-authorization
	// grant uses no client secret), so it is safe to ship in source-built
	// binaries. It is overridden by --client-id / SHIPABLE_WORKOS_CLIENT_ID /
	// WORKOS_CLIENT_ID / apps/api/.env or by release-specific -ldflags.
	defaultWorkOSClientID = "client_01KSXAMHC5HC8F6J7D1GZMAA07"

	// defaultWorkOSAPIURL optionally overrides the WorkOS API base at build
	// time; empty falls back to the public WorkOS API.
	defaultWorkOSAPIURL = ""

	// officialAPIURL is the production Shipable API base, injected at build time
	// via -ldflags -X. When set, the TUI offers an "official" backend the
	// developer can switch to (alongside the local one) with one key. Empty in
	// dev builds; can also be supplied at runtime via SHIPABLE_OFFICIAL_API_URL.
	officialAPIURL = ""
)

type versionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
}

func currentVersion() versionInfo {
	return versionInfo{
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
	}
}

func (info versionInfo) String() string {
	return fmt.Sprintf("shipable %s (commit %s, built %s)", info.Version, info.Commit, info.BuildDate)
}
