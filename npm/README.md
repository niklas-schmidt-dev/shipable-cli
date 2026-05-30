# Shipable CLI

Install the Shipable CLI with npm:

```bash
npm install -g @shipable/cli
shipable version
```

This package contains prebuilt Go binaries for macOS, Linux, and Windows on x64
and arm64. The npm wrapper only selects and executes the bundled binary. It has
no dependencies, no install lifecycle scripts, and does not download code during
installation.

Browser login uses the Production WorkOS CLI application client id baked into
the Shipable CLI source: `client_01KSXAMHC5HC8F6J7D1GZMAA07`.

## License

Apache-2.0. See [LICENSE](LICENSE).
