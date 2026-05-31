# Release

The npm package is published as `@shipable/cli` through npm Trusted Publishing.
Homebrew builds are published through the official tap
`niklas-schmidt-dev/homebrew-shipable` from the same tagged source release.

The Production WorkOS CLI browser-login client id is committed in source as
`client_01KSXAMHC5HC8F6J7D1GZMAA07`. Do not rely on release-only ldflags for
that default; source-built installs such as Homebrew must behave the same as npm.

1. Land changes on `main` and wait for the `ci` workflow to pass.
2. Create an annotated signed tag whose version matches `npm/package.json`.
3. Push the tag.
4. The release workflow verifies the signed tag, runs tests, builds all bundled
   binaries, verifies the exact npm packlist, and stages the package with OIDC.
5. Inspect the staged package with `npm stage view` and `npm stage download`.
6. Approve the staged package manually with npm 2FA.
7. Update the Homebrew tap formula to the same tagged source release.

Required npm setup:

- Trusted Publisher: `niklas-schmidt-dev/shipable-cli`
- Workflow filename: `publish-cli-npm.yml`
- Permission: `npm stage publish`
- Package publishing access: require 2FA and disallow tokens
- Repository variable: `SHIPABLE_RELEASE_GPG_PUBLIC_KEY_B64`

No `NPM_TOKEN` is used.

Homebrew tap release checklist:

1. Tag the source release, for example `v0.1.4`, after `npm/package.json`
   matches that version.
2. Update `niklas-schmidt-dev/homebrew-shipable` formula URL and `sha256` to
   the GitHub source tarball for the tag.
3. Verify the formula builds from source and that `brew test
   niklas-schmidt-dev/shipable/shipable` exercises local, credential-free
   behavior.
4. Push the tap update only after validation passes.

Future Homebrew/core checklist:

1. Do not submit to Homebrew/core until Shipable satisfies Homebrew's current
   notability and third-party-use requirements for new formulae.
2. Use the unmodified Homebrew PR template.
3. Disclose any AI/LLM assistance in the initial PR body, including tool/model
   and manual verification steps.
4. Do not open a Homebrew/core PR without explicit approval.
