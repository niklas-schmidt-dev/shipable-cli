# Shipable CLI

Shipable CLI is the command line interface for Shipable app generation, sync,
preview, and deployment workflows.

## Install

```bash
npm install -g @shipable/cli
shipable version
```

## Two ways to use it

Shipable CLI is designed to be driven two ways from a single codebase:

- **Headless / scriptable (default).** Every action is a flag-driven subcommand
  with machine-friendly output (`--json` on `templates`/`status`), so agents and
  CI can call it without ever hitting an interactive prompt. This is what the
  npm package ships, and it has zero runtime dependencies.
- **Interactive TUI (opt-in).** A full-screen terminal UI built on
  [Bubble Tea](https://github.com/charmbracelet/bubbletea), launched explicitly
  with `shipable ui`. It reuses the exact same engine as the headless commands
  (no duplicated API/auth/poll logic) and only renders to a real terminal.

### Interactive UI (`shipable ui`)

The TUI gives you a status dashboard with one-key actions — deploy preview/
production, sync, generate, follow logs, and create from a template — plus
browser (device-flow) login.

It can also switch backends live with `e`: it defaults to the **official**
production API (`https://api.shipable.de`) and toggles to a **local** backend
(`http://localhost:8080`) for development. Each backend keeps its own stored
token (`config.json` vs `config-local.json`), so switching never logs you out
of the other. Override the URLs with `SHIPABLE_OFFICIAL_API_URL` /
`SHIPABLE_LOCAL_API_URL`; exporting `SHIPABLE_API_URL=http://localhost:8080`
starts the TUI on the local backend.

It is compiled behind the `tui` build tag so the headless/npm binaries stay
small and dependency-free:

```bash
go build -tags tui -o shipable ./cmd/shipable
./shipable ui
```

`shipable ui` only starts when stdin, stdout, and stderr are all TTYs, and
refuses under `CI`, `TERM=dumb`, or `SHIPABLE_NO_TUI=1`. It renders to stderr,
so the rest of the CLI keeps stdout reserved for machine output. The default
(npm-distributed) binary is built **without** the tag and `shipable ui` there
prints how to get the TUI build.

### Authentication

Two ways to authenticate, neither of which needs a WorkOS client id:

The CLI talks to the production API (`https://api.shipable.de`) by default;
override with `--api-url`, `SHIPABLE_API_URL`, or the TUI backend switcher.

- **Access token** — set `SHIPABLE_TOKEN` (and `SHIPABLE_API_URL` to target a
  non-default backend) before launching, run `shipable auth login --token-stdin`,
  or press `t` in the TUI to paste a token.
- **Browser (device flow)** — `shipable auth login`, or press `l` in the TUI.

Browser login uses the WorkOS device-authorization grant, which needs a
**public** WorkOS client id (no secret). It is resolved in this order:

1. `--client-id`
2. `SHIPABLE_WORKOS_CLIENT_ID`
3. `WORKOS_CLIENT_ID`
4. `apps/api/.env` (`WORKOS_CLIENT_ID`), searched upward from the working dir
5. the prod Shipable CLI client id baked into the source

The source default is the Production WorkOS CLI application
`client_01KSXAMHC5HC8F6J7D1GZMAA07`, so npm, Homebrew, and source builds can
start browser login without extra configuration. Override it only for local or
staging auth testing.

Release builds may still override it with `SHIPABLE_WORKOS_CLIENT_ID` and
optional `SHIPABLE_WORKOS_API_URL` via `-ldflags`, but the normal npm and
Homebrew builds should rely on the source default. For local development against
another WorkOS app, export `SHIPABLE_WORKOS_CLIENT_ID` before launching, e.g.:

```bash
export SHIPABLE_WORKOS_CLIENT_ID=client_xxx
go run -tags tui ./cmd/shipable ui
```

## Development

```bash
go test ./...
go test -tags tui ./...   # also exercises the TUI model
cd npm
npm ci --ignore-scripts
npm test
npm run build
npm run pack:check
```

The npm package bundles prebuilt Go binaries for macOS, Linux, and Windows on
x64 and arm64. It has no npm dependencies, no install lifecycle scripts, and no
runtime binary downloads.

The TUI dependencies (Bubble Tea et al.) are only used under the `tui` build
tag, so the default `go build` and the npm binaries never link them. Because
`go mod tidy` ignores tagged files unless the tag is visible, always tidy with:

```bash
GOFLAGS=-tags=tui go mod tidy
```

A plain `go mod tidy` would prune the TUI requires from `go.mod`.

## License

Apache-2.0. See [LICENSE](LICENSE).
