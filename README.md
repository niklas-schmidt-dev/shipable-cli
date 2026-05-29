# Shipable CLI

Shipable CLI is the command line interface for Shipable app generation, sync,
preview, and deployment workflows.

## Install

```bash
npm install -g @shipable/cli
shipable version
```

## Development

```bash
go test ./...
cd npm
npm ci --ignore-scripts
npm test
npm run build
npm run pack:check
```

The npm package bundles prebuilt Go binaries for macOS, Linux, and Windows on
x64 and arm64. It has no npm dependencies, no install lifecycle scripts, and no
runtime binary downloads.

## License

Apache-2.0. See [LICENSE](LICENSE).
