# Release

The npm package is published as `@shipable/cli` through npm Trusted Publishing.
Homebrew builds are submitted to `Homebrew/homebrew-core` from the same tagged
source release.

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

Required npm setup:

- Trusted Publisher: `niklas-schmidt-dev/shipable-cli`
- Workflow filename: `publish-cli-npm.yml`
- Permission: `npm stage publish`
- Package publishing access: require 2FA and disallow tokens
- Repository variable: `SHIPABLE_RELEASE_GPG_PUBLIC_KEY_B64`

No `NPM_TOKEN` is used.

Homebrew/core release checklist:

1. Tag the source release, for example `v0.1.4`, after `npm/package.json`
   matches that version.
2. Update the Homebrew/core formula URL and `sha256` to the GitHub source
   tarball for the tag.
3. Verify the formula builds from source and that `brew test shipable` exercises
   local, credential-free behavior.
4. Disclose any AI assistance in the Homebrew PR description and note that the
   submitted content was reviewed before opening the PR.
