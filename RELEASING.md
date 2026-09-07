# Releasing bug-bot

Run `make check build`, inspect `git diff`, and commit the complete change.
The shared ACP dependency must be a published version, with no local replace
or workspace override needed to build. Keep `go.mod` and `go.sum` committed.

Push master and a new semver tag such as `v0.1.1`. The Release workflow runs
Linux/macOS checks, builds all four platform archives, verifies checksums and
smoke-tests the Linux binary. It uploads a draft release and publishes it only
after uploads succeed. Monitor the workflow and verify the release assets.

The installer expects `brokk-bug-bot-VERSION-OS-ARCH.tar.gz`, containing `bbb`,
and `checksums.txt`. Supported targets are Linux/macOS, amd64/arm64.
`python3 scripts/package_release.py v0.1.1` builds these archives locally.
The tag is the Go module release. After it succeeds, run the Publish packages
workflow from that tag with the same tag as input. Its default `publish=false`
builds and tests npm tarballs; `publish=true` uploads the four native platform
packages and the `@brokkai/bug-bot` launcher. Configure npm trusted publishing for
all five packages with repository `BrokkAi/bug-bot`, workflow
`publish-packages.yml`, and environment `packages-publish`. `NPM_TOKEN` can
bootstrap publication. No Python distribution is included.

Before a release, run the offline installer check from a clean commit:
`python3 scripts/smoke_installers.py`. This builds all four archives and installs
and runs the local platform's npm package without publishing anything.

If publication fails after draft creation, inspect the draft and uploaded assets.
Complete or replace that draft explicitly; do not move published version tags.
