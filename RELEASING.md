# Releasing bug-bot

Release preparation goes through a topic-branch PR into `master`. Fetch and merge
concurrent `master` changes, pass CI, merge the PR, and use its actual merged
commit. Never move an existing release tag. The next version after v0.2.1 is
v0.2.2; versions come from the proposed semver tag, not a source manifest.

## Destinations and order

1. GitHub `BrokkAi/bug-bot`: four `brokk-bug-bot-VERSION-OS-ARCH.tar.gz`
   archives for Linux/macOS amd64/arm64, plus `checksums.txt` and `release.json`.
   Each archive includes the executable, exact commit/version/platform metadata,
   README, license, notice and dependency report.
2. Go module `github.com/BrokkAi/bug-bot`: the same immutable version tag exposes
   the source module. There is no separate upload or registry credential.
3. npm `@brokkai/bug-bot-linux-x64`, `@brokkai/bug-bot-linux-arm64`,
   `@brokkai/bug-bot-darwin-x64`, `@brokkai/bug-bot-darwin-arm64`: independent
   native packages, submitted before the launcher.
4. npm `@brokkai/bug-bot`: launcher with exact-version optional dependencies on
   all four platform packages.

No Python, Maven, crates.io, container, documentation deployment, update feed,
external signing or notarization publication is configured. The shell installer
consumes GitHub assets; it has no separate update feed.

## Non-publishing preflight

`ci.yml` (CI) validates pushes, PRs, manual runs and reusable calls on Linux and
macOS. `release.yml` (Release) is reusable build-only infrastructure, invoked by
`publish-packages.yml` (Publish packages). Branch pushes never publish.

Run `make check build`, Python script tests and Node launcher tests for changed
inputs. CI also builds all real platform archives, npm packages, and performs
local installer smoke tests. License validation runs before native packaging.
See `licenses/README.md` when changing dependencies or Go versions.

Dispatch the existing workflow at the exact preparation branch or merged SHA,
with a proposed version and **publish=false** (the default):

```sh
gh workflow run publish-packages.yml --repo github.com/BrokkAi/bug-bot --ref PREPARED_REF -f tag=v0.2.2 -F publish=false
```

This runs complete CI, builds native archives and all npm packages, saves Actions
artifacts, checks every version, and probes the actual publisher without final
asset uploads, tags or public releases. A disposable empty private GitHub draft
is created, updated and deleted solely to test the job's release-write access.
The draft is not readiness state and is never needed by later verification.

The `packages` job uses environment `packages-publish`, `contents:write`,
`actions:read`, and `id-token:write`. Its GITHUB_TOKEN validates GitHub access.
For every npm package, it exchanges its exact-commit GitHub OIDC token with npm
and checks expiry. No developer login or stored token is substituted. npm trust
configuration requires a maintainer session with write access and 2FA; the
short-lived OIDC exchange token cannot inspect that setting. Exchange success
establishes package-scoped identity, not direct-publish permission. The registry
enforces the direct-publish grant on the first upload. If it rejects that upload,
the workflow stops with the GitHub release still private, preserving the staged
assets and allowing an exact-tag retry after the npm owner fixes the trust grant.

Inspect the exact-SHA latest run and jobs using `gh`. Once the preflight run is
successful, independent daemon checks use these commands with `RELEASE_COMMIT`,
`RELEASE_TAG`, and optional `RELEASE_TARGET` set:

```sh
python3 scripts/release_preflight.py build
python3 scripts/release_preflight.py authorization
python3 scripts/release_preflight.py version
```

The build command requires successful exact-commit jobs and unexpired native/npm
artifact evidence. Authorization also requires evidence less than one hour old;
redispatch preflight when it expires. Version checks always query GitHub and npm.
The same commands cover every destination; npm checks validate all five packages
and their dependency ordering together. There is no unconditional-success check.

## Publication and recovery (separate phase)

After preflight is approved by the release daemon, create/push the exact version
tag, which triggers `publish-packages.yml`. A tag made using GITHUB_TOKEN does
not automatically trigger another workflow: use an explicit dispatch from the
existing exact tag with `publish=true` in that case. Never dispatch publication
from a branch. Workflow dispatch with `publish=false` needs no existing tag.

The publishing job rebuilds and validates everything, rechecks every version and
its own credentials before uploads, recreates a missing draft, uploads missing
native assets without overwriting conflicts, submits platform npm packages before
the launcher, verifies all npm versions, then makes the GitHub release public.
Registry visibility delays fail safely with the draft private; rerun once npm
indexes are available. Existing identical npm payloads are retained. A public
GitHub release enters read-only verification; it is never converted to a draft.

Tags with a prerelease suffix (such as `v0.4.0-rc.1`) publish as GitHub
prereleases without replacing the latest stable release used by the unpinned
shell installer. npm publishes these versions under `next`. Stable tags promote
the GitHub release to latest and use npm's `latest` tag.

For partial staging recovery, rerun the same exact tag. Existing draft assets
must match the original staged bytes. Preserve conflicting drafts and investigate
instead of overwriting. During upload, native downloads must match staged bytes.
Independent later verification validates downloaded archives against their own
checksums/manifests and compares unpacked file bytes and permissions against the
exact-commit build, so compressor differences do not hide binary differences.

After publication run the separate destination verifier:

```sh
python3 scripts/release_preflight.py published
```

Set `RELEASE_DESTINATION` to the individual npm package name, to
`github.com/BrokkAi/bug-bot` for the Go module, or `github-release` for assets.
It rejects missing/partial publication and wrong metadata, contents or modes.
Go verification resolves the immutable module version and checks its origin SHA.
