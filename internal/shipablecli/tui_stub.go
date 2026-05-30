//go:build !tui

package shipablecli

import "errors"

// errTUINotBuilt is returned when the interactive UI is invoked on a binary
// compiled without the `tui` build tag. The default (and npm-distributed)
// builds omit the Bubble Tea dependency tree so they stay small and
// dependency-free for headless/automation use; the TUI ships as a separate
// `-tags tui` build.
var errTUINotBuilt = errors.New("this build was compiled without the interactive UI; install the TUI build or rebuild with: go build -tags tui ./cmd/shipable")

func (r runner) runUI(_ []string) error {
	return errTUINotBuilt
}
