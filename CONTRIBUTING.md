# Contributing

Contributions are welcome through pull requests.

By submitting a contribution, you agree that your contribution is licensed under
the Apache License, Version 2.0.

Before opening a pull request, run:

```bash
go test ./...
go test -tags tui ./...   # compiles and tests the interactive TUI
cd npm
npm ci --ignore-scripts
npm test
npm run pack:check
```

The interactive UI lives behind the `tui` build tag (`internal/shipablecli/tui*.go`)
so the default and npm-distributed binaries stay dependency-free. Verify it with
`go build -tags tui ./...`. When dependencies change, tidy with the tag visible —
`GOFLAGS=-tags=tui go mod tidy` — since a plain `go mod tidy` cannot see the
tagged files and would drop the TUI dependencies from `go.mod`.
