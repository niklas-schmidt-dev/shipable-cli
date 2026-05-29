# Release

The npm package is published as `@shipable/cli` through npm Trusted Publishing.

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
