# Releasing

Releases are cut by pushing a tag. GoReleaser builds four platform archives
(linux/darwin × amd64/arm64), publishes a GitHub Release, and updates
the Homebrew cask in `upstacked/homebrew-tools`.

```sh
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

Do **not** also run `goreleaser release` locally for the same tag: both the
workflow and the local run will try to upload the same assets, and the second
one fails with `already_exists`.

To rehearse without publishing anything:

```sh
goreleaser release --snapshot --clean --skip=publish
```

A rehearsal cannot catch everything. GoReleaser validates the cask's `token`
field only during a real publish, so neither `goreleaser check` nor a snapshot
will tell you it is malformed. Releasing from a throwaway tag is the only way
to exercise that path.

Running a real release locally without a tap token needs `--skip=homebrew`;
otherwise the cask step fails on the missing environment variable.

## Homebrew tap credentials

The workflow's `GITHUB_TOKEN` is scoped to `upstacked/upagent-anywhere`, so it cannot push
the formula to `upstacked/homebrew-tools`. A GitHub App supplies a short-lived
token scoped to the tap, with no personal credential stored in a secret.

Without those secrets the release still publishes binaries; only the cask
update is skipped, and the run logs a warning.

### One-time setup

The `upstacked-tap-publisher` App already exists for `upstacked/cli` and is
installed on `upstacked/homebrew-tools`, which is all the token is scoped to.
This repository only needs the same two secrets:

```sh
gh secret set TAP_APP_ID --repo upstacked/upagent-anywhere --body "<app id>"
gh secret set TAP_APP_PRIVATE_KEY --repo upstacked/upagent-anywhere < path/to/key.pem
```

Verify with a patch tag; the run should show the cask being pushed.

## Verifying an install

```sh
brew update
brew install upstacked/tools/upagent-anywhere   # or: brew upgrade --cask upstacked/tools/upagent-anywhere
upagent-anywhere version
```
