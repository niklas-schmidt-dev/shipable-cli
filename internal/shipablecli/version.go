package shipablecli

import "fmt"

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
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
