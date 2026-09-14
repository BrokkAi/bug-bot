# Brokk Bug Bot

Find new bugs in a repository and file useful GitHub issues. `bbb` follows the
same Go CLI, no-config discovery, managed workspace, and shared
[ACP runner](https://github.com/BrokkAi/acp-go) pattern as
[issue-bot](https://github.com/BrokkAi/issue-bot) (`bib`) and
[release-bot](https://github.com/BrokkAi/release-bot) (`brb`).

**The LLM decides whether a finding duplicates an existing issue.** It compares
root causes, triggering inputs, behavior, and discussion across open and closed
issues. There are no title similarity thresholds or bug fingerprint rules.

## Install and run

Install with npm (Node.js 18+; no Go toolchain required):

```sh
npm install -g @brokkai/bug-bot
bbb /path/to/your-repo
```

For a single invocation, use `npx --yes @brokkai/bug-bot`. Keep npm optional
dependencies enabled: they supply the native Linux/macOS x64 or arm64 binary.

Or install a native binary with its SHA-256 checksum verified:

```sh
curl -fsSL https://raw.githubusercontent.com/BrokkAi/bug-bot/master/install.sh | sh
```

The installer puts `bbb` in `~/.local/bin`; add that directory to `PATH`.
Set `INSTALL_DIR` to change the destination. To pin a version, download the
script and run `sh install.sh v0.1.0`.

With Go 1.27.1 or newer:

```sh
go install github.com/BrokkAi/bug-bot/cmd/bbb@latest
```

Go installs `bbb` in `GOBIN`, or `$(go env GOPATH)/bin` by default.
From a source checkout:

```sh
make build
./bin/bbb /path/to/your-repo
./bin/bbb /path/to/your-repo --plain
./bin/bbb once /path/to/your-repo --dry-run
./bin/bbb once /path/to/your-repo --focus "parser and input validation"
./bin/bbb /path/to/your-repo --max-issues 2 --label bug
./bin/bbb /path/to/your-repo --model YOUR_MODEL_ID --effort low
./bin/bbb status /path/to/your-repo
./bin/bbb version
./bin/bbb retry /path/to/your-repo --once
```

Source builds require Go 1.27.1. To install the local source as `bbb`, run
`go install ./cmd/bbb` and put your Go bin directory on `PATH`.
Running `bbb` from inside any target repository discovers its remote and default
branch. A Git URL also works. Flags can precede or follow the repository argument.

`bbb version` prints the embedded release tag. Local builds report `dev`; binaries installed with `go install ...@version` report the module version.

Runtime requirements: Git, authenticated `gh` with repository/issue read and
issue creation access, and an authenticated ACP agent. By default it uses an
installed `codex-acp`, falling back to `npx --yes @agentclientprotocol/codex-acp`.
Explicit `--agent` commands are used as supplied; repeat `--agent-arg` for arguments.

Starting `bbb` authorizes unattended local investigation, test execution, and
creation of issues for the selected repository. `--dry-run` performs discovery
and review, prints the proposed issue bodies, and saves them without filing.

## Terminal dashboard

Interactive runs show a live dashboard by default. It fits the current terminal
or tmux pane, adjusts when the pane is resized, and keeps the repository and
current task visible. Each pane still runs one repository.

```sh
bbb /path/to/repo           # live dashboard in an interactive terminal
bbb /path/to/repo --plain   # scrolling console output and agent transcript
bbb /path/to/repo --json    # structured logs for tools and log collectors
```

The overview shows the repository, branch and commit, scan stage, active tool,
uptime, attempt budget, and next check or retry countdown. Larger panes also
show the selected model, reasoning effort, and investigation focus.

**Saved** counts cover findings in the configured repository/branch state,
including the current scan: found, filed, duplicate, pending, dry run, and skipped
(invalid, uncertain, or stale). **Run** counters start at zero each time the
process starts: completed scans, attempts, agent starts, tools, and error log
events. A discovered finding is counted as filed only after publication is
confirmed. Findings restored after restarting are included in saved totals.

- `1`, `2`, `3` or `Tab`: switch between overview, findings, and activity.
- `↑` / `↓` or `k` / `j`: browse findings or scroll activity.
- `Enter`: inspect the selected finding, issue URL, reproduction, and review.
- `Esc`: return from finding details. `Page Up` / `Page Down` scroll details.
- `g` / `G`: jump to the start/end; `G` resumes following live activity.
- `q` or `Ctrl+C`: stop the bot and its active agent, then restore the terminal.

The finding browser shows the latest 200 findings, while totals include all saved
findings. The activity view keeps recent output; full agent transcripts remain
under the state directory. On exit, a short summary and new finding URLs stay in
the terminal. `once` exits after its scan, and dry runs also print proposed finding
details on exit.

Piped input, redirected stderr, and `TERM=dumb` use scrolling output automatically.
`--plain` and `--json` disable the dashboard and are mutually exclusive.
`NO_COLOR` disables dashboard colors. `status`, `version`, and help keep their
existing output and never open the dashboard.

## How it works

1. Fetch the target branch into a managed clone and make an isolated detached
   worktree for the scan. The original checkout and uncommitted work are preserved.
2. Download all open and closed issues and their comments with pagination. Give
   the investigator the complete snapshot and recent scan summaries so it can
   avoid known bugs and explore new areas on subsequent scans.
3. Have the agent inspect source and run relevant checks. Each candidate must
   include a root cause, source paths, concrete reproduction, expected/actual
   behavior, and observed evidence or a precise code proof. Zero findings is valid.
4. Start separate LLM review sessions to verify the evidence and compare each
   candidate against the issue history. Large histories are supplied in batches;
   every issue and comment is included, and each response must identify all issue
   numbers it reviewed. A duplicate verdict links the existing report in local
   state. Uncertain and invalid findings are saved without filing.
5. Refresh issues before publication. New or edited reports go back to the LLM
   for comparison. Recheck the source commit and tracked files. An optional
   operator verifier can provide an additional gate.
6. Create issues sequentially, including reproduction, evidence, and the review
   explanation. Each newly created issue is available to the next candidate's
   LLM review, including candidates from the same scan.

Closed issues count as known reports, including fixed, duplicate, or rejected
bugs. The reviewer is instructed to treat a regression of an existing report as
belonging to that report. The bot does not reopen or comment on it.

The default is at most **three issues per scan**, a **two-hour attempt budget**,
and another scan **30 minutes after completion**, even if the commit is unchanged.
Recent summaries guide exploration; this is not a claim of exhaustive coverage.
`once` runs or resumes one scan and exits. Failed scans retain their candidates,
workspace, and diagnostics; retries wait at least 15 minutes and run on the next
poll, with three attempts before requiring `retry`. Agent setup errors stop the
daemon without consuming an attempt. An advanced branch invalidates pending
findings so the next eligible attempt scans the new commit.

## Duplicate handling and interrupted requests

Semantic duplicate detection is an LLM judgment, so it is not a guarantee.
Incomplete history, malformed review responses, and uncertain comparisons stop
publication. Same-title reports still reach the LLM: identical wording can hide
different bugs, and different wording can describe the same bug.

Every planned issue gets a random request ID, saved **before** sending its create
request and embedded as a hidden comment in the issue body. This ID identifies
one publication attempt; it is not derived from bug content or used to classify
duplicates. After a crash or lost response, `bbb` looks for that ID and records
the existing issue. If its outcome is unknown and the ID is not visible, it
refuses to send another create request. Run `once` to try reconciliation again.
If it never appears, inspect GitHub and the saved state before repairing the
pending entry; `retry` intentionally cannot blindly resend an ambiguous request.

Confirmed HTTP rejections, such as validation or permission failures, keep the
candidate pending. Correct the request or access problem, then run `once` to
resume, or `retry --once` if the attempt budget is exhausted. Timeouts, server
errors, and incomplete responses still require marker reconciliation. Older
versions saved every create error as ambiguous; those existing `posting` entries
still require inspection when no marker appears.

A per-repository local lock coordinates instances across branches and config
paths using the same state home. Different machines/accounts and simultaneous
human reports cannot be locked atomically with GitHub issue creation. Run one
active `bbb` per repository to avoid that race.

## Brokk Town worker service

`bbb worker --socket PATH` serves one-shot bug scan operations to Brokk
Town over a private Unix-domain socket. The socket is mode `0600`; the endpoint is
private to the local service, and the process exits after Town requests shutdown.

Worker protocol v1 uses standard-library HTTP with JSON messages:

- `GET /v1/initialize` returns the protocol range, bot identity, release version,
  and capabilities. Town requires `bug-scan` as well as common `run` and
  `progress` capabilities.
- `POST /v1/runs` accepts one strict JSON task and responds with contiguous
  newline-delimited JSON events: `progress`, optional typed `result`,
  and `error`, `canceled`, or `complete`.
- `POST /v1/shutdown` asks the service to stop after the current stream.

Version and capability negotiation happen before work starts. Town does not read
this bot's private state files; issue and review outcomes are explicit protocol
results when applicable, while GitHub remains the durable source for receipts.
The schemas are independent of the Unix HTTP transport, allowing an authenticated
TLS transport to be added later without changing worker semantics.

## Optional configuration

`bbb --config bug-bot.json` loads a strict JSON object. No file is loaded or
generated implicitly. Paths are resolved relative to the configuration file.
See [bug-bot.example.json](bug-bot.example.json).

```json
{
  "remote": "https://github.com/OWNER/REPO.git",
  "branch": "main",
  "directory": "var/checkout",
  "state_directory": "var/state",
  "poll": "30m",
  "timeout": "2h",
  "retry_delay": "15m",
  "attempts": 3,
  "max_issues": 3,
  "focus": "",
  "labels": [],
  "dry_run": false,
  "agent": {"command": ["codex-acp"]}
}
```

Labels are optional and added only to new issues; they never filter the history.
Use labels that already exist in the target repository. `github.host` supports
Enterprise, and `github.repo` (`OWNER/REPO`) identifies a local mirror's GitHub
repository. `agent` also supports `environment`, `auth_method`, `mode`, `model`,
and `effort`, with selection handled by the shared ACP runner.

`verify` accepts an argument array, such as `["/opt/checks/verify-bug"]`, executed
in the scan worktree with `BUG_COMMIT` and JSON `BUG_FINDING` in its environment.
Keep operator verifiers outside the writable worktree. A nonzero exit blocks filing.

## State and execution

State defaults to `$XDG_STATE_HOME/bug-bot` or `~/.local/state/bug-bot`, keyed by
remote and branch. JSON state is replaced atomically with fsync; private session
transcripts live under the state directory. `status` prints saved JSON without
starting an agent. `--json` selects structured progress logs.

Scan worktrees and reproduction files are retained for inspection. Manage their
retention along with transcripts externally. Agent instructions prohibit fixes,
commits, pushes, and direct GitHub writes; tracked source changes or a changed
HEAD invalidate the scan. Evidence is independently reviewed by the LLM, not
proof that tests are correct. As in the sibling bots, ACP permission requests
are automatically approved and agent commands run with the account's OS rights.
This is not a sandbox; use an appropriate account/container for the repository.

## Development and packaging

```sh
make check build
./bin/bbb --help
python3 -m unittest discover -s scripts -p '*_test.py'
node --test --test-isolation=none npm/bbb.test.cjs
```

Tests use local Git fixtures and simulated ACP/GitHub outcomes; they do not run
a paid model or create real issues. They cover semantic closed duplicates,
same-title distinct bugs, concurrent reports, partial histories, source changes,
dry runs, retries, ambiguous POSTs, receipt coverage, configuration, locks, progress snapshots, dashboard resizing, and terminal cleanup.

The inherited release workflow packages Linux/macOS amd64/arm64 archives with
checksums. The npm launcher and packaging workflow target `@brokkai/bug-bot`
and install `bbb`. See [RELEASING.md](RELEASING.md) for publication and verification.

API references: [GitHub issues](https://docs.github.com/en/rest/issues/issues),
[issue comments](https://docs.github.com/en/rest/issues/comments).
## Automatic releases

Push a new `v*` version tag to run the complete **Publish packages** pipeline:
Linux/macOS checks, native GitHub assets, then all five npm packages from the
same tag and commit. No manual package dispatch is needed. The package job runs
only after native publication succeeds and validates package contents and local
installs before uploading. It does not wait for npm's public index to update.

For recovery, rerun failed jobs or manually dispatch `publish-packages.yml` from
the exact existing tag with `publish=true`. The default manual `publish=false`
validates without uploading. Existing published bytes must match on retry.
See [RELEASING.md](RELEASING.md) for details.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) and our
[Code of Conduct](CODE_OF_CONDUCT.md). Report vulnerabilities privately using
[SECURITY.md](SECURITY.md).

## License

Licensed under [Apache-2.0](LICENSE). See [NOTICE](NOTICE) for project
attribution and [licenses/README.md](licenses/README.md) for dependency terms,
third-party notices, and the license review process.
