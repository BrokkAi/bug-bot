package bugbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BrokkAi/acp-go/runner"
	"github.com/BrokkAi/bug-bot/internal/osrun"
)

func canonicalTestDir(t *testing.T) string {
	t.Helper()
	p, err := canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func writeTestFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
}
func localGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	env := map[string]string{"GIT_AUTHOR_NAME": "Fixture", "GIT_AUTHOR_EMAIL": "fixture@example.com", "GIT_COMMITTER_NAME": "Fixture", "GIT_COMMITTER_EMAIL": "fixture@example.com", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null"}
	out, err := osrun.Run(context.Background(), dir, env, append([]string{"git"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func finding() Finding {
	return Finding{Title: "Empty input panics in parser", RootCause: "Parser indexes input[0] before checking length", Files: []string{"README.md"}, Reproduction: "Call parse with an empty string", Expected: "Return empty result", Actual: "Index out of range panic", Evidence: []string{"parse(\"\") reproduced an index out of range panic"}}
}

type fakeSource struct {
	cfg            Config
	items          []Issue
	reads, creates int
	onRead         func(*fakeSource) error
	createErr      error
	lost           bool
}

func (f *fakeSource) issues(context.Context) ([]Issue, error) {
	f.reads++
	if f.onRead != nil {
		if err := f.onRead(f); err != nil {
			return nil, err
		}
	}
	return append([]Issue(nil), f.items...), nil
}
func (f *fakeSource) create(_ context.Context, c *Candidate, commit string) (*Issue, error) {
	f.creates++
	if f.createErr != nil {
		return nil, f.createErr
	}
	i := Issue{Number: 100 + f.creates, Title: c.Finding.Title, Body: issueBody(c, commit), State: "open"}
	i.URL = fmt.Sprintf("https://github.com/o/r/issues/%d", i.Number)
	f.items = append(f.items, i)
	if f.lost {
		return nil, errors.New("connection lost after POST")
	}
	return &i, nil
}

type fakeAgent struct {
	scans, reviews int
	findings       []Finding
	onReview       func([]Issue) Review
	onScan         func()
	onExecute      func() error
	err            error
}

func (a *fakeAgent) Execute(_ context.Context, prompt string) (string, error) {
	if a.err != nil {
		return "", a.err
	}
	if a.onExecute != nil {
		if err := a.onExecute(); err != nil {
			return "", err
		}
	}
	if strings.Contains(prompt, "Scan context (data):") {
		a.scans++
		if a.onScan != nil {
			a.onScan()
		}
		fs := a.findings
		if fs == nil {
			fs = []Finding{finding()}
		}
		return "BUG_RESULT " + jsonContextCompact(ScanResult{Summary: "Inspected parser empty-input handling", Findings: fs}), nil
	}
	a.reviews++
	var data struct{ Issues []Issue }
	if err := json.Unmarshal([]byte(strings.Split(prompt, "Review context (data):\n")[1]), &data); err != nil {
		return "", err
	}
	r := Review{Verdict: "new", Reason: "Independently reproduced the empty-input panic; distinct root cause", Checked: []int{}}
	for _, i := range data.Issues {
		r.Checked = append(r.Checked, i.Number)
		// Simulated LLM verdict for this fixture's one known root cause.
		if strings.Contains(i.Body, finding().RootCause) {
			r.Verdict = "duplicate"
			r.Duplicate = i.Number
			r.Reason = "Same parser defect"
		}
	}
	if a.onReview != nil {
		custom := a.onReview(data.Issues)
		custom.Checked = r.Checked
		r = custom
	}
	return "BUG_REVIEW " + jsonContextCompact(r), nil
}
func jsonContextCompact(v any) string { b, _ := json.Marshal(v); return string(b) }
func fixture(t *testing.T) (engine, *State, *fakeSource, *fakeAgent, string) {
	t.Helper()
	source, remote := discoveryRepo(t)
	dir := canonicalTestDir(t)
	cfg := DefaultConfig()
	cfg.Remote = remote
	cfg.Branch = "main"
	cfg.GitHub.Repo = "o/r"
	cfg.Directory = filepath.Join(dir, "checkout")
	cfg.StateDirectory = filepath.Join(dir, "state")
	if err := os.MkdirAll(cfg.StateDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	f := &fakeSource{cfg: cfg}
	a := &fakeAgent{}
	e := engine{config: cfg, source: f, log: slog.New(slog.NewTextHandler(io.Discard, nil)), agent: func(Config) Agent { return a }, now: time.Now, sleep: func(context.Context, time.Duration) error { return nil }}
	return e, newState(cfg), f, a, source
}

func TestRunSymlinkDirectory(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			e, _, _, _, _ := fixture(t)
			t.Setenv("XDG_STATE_HOME", canonicalTestDir(t))
			if existing {
				if err := (checkout{e.config}).open(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			alias := filepath.Join(canonicalTestDir(t), "alias")
			if err := os.Symlink(filepath.Dir(e.config.Directory), alias); err != nil {
				t.Fatal(err)
			}
			cfg := e.config
			cfg.Directory = filepath.Join(alias, "checkout")
			cfg.StateDirectory = filepath.Join(alias, "state")
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reached := false
			ctx = WithProgress(ctx, func(p Progress) {
				if p.Phase == "attempt" {
					reached = true
					cancel() // Stop before accessing GitHub or starting an agent.
				}
			})
			err := Run(ctx, cfg, e.log, true)
			if !reached || !errors.Is(err, context.Canceled) {
				t.Fatalf("expected cancellation after workspace preparation, reached=%t: %v", reached, err)
			}
			saved, err := ReadState(e.config)
			if err != nil || saved == nil || saved.Scan == nil {
				t.Fatalf("missing saved scan: %+v, %v", saved, err)
			}
			if filepath.Dir(saved.Scan.Directory) != e.config.Directory+"-scans" {
				t.Fatalf("scan path is not canonical: %s", saved.Scan.Directory)
			}
			w := checkout{e.config}
			w.config.Directory = saved.Scan.Directory
			if err := w.verify(context.Background(), saved.Scan); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunSymlinkOverlapRejected(t *testing.T) {
	e, _, _, _, _ := fixture(t)
	alias := filepath.Join(canonicalTestDir(t), "alias")
	if err := os.Symlink(filepath.Dir(e.config.Directory), alias); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"checkout", "checkout-scans"} {
		t.Run(name, func(t *testing.T) {
			cfg := e.config
			cfg.StateDirectory = filepath.Join(alias, name, "state")
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			if err := Run(context.Background(), cfg, e.log, true); err == nil || !strings.Contains(err.Error(), "must not overlap") {
				t.Fatalf("expected resolved path overlap rejection, got %v", err)
			}
		})
	}
}

func TestScanPublishAndRestartDoesNotDuplicate(t *testing.T) {
	e, s, f, a, source := fixture(t)
	writeTestFile(t, filepath.Join(source, "unfinished.txt"), "user work")
	if err := e.step(context.Background(), s, true); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 || a.reviews != 1 || len(s.Completed) != 1 || s.Completed[0].Status != "submitted" {
		t.Fatalf("unexpected completion %+v, creates %d, reviews %d", s, f.creates, a.reviews)
	}
	saved, err := ReadState(e.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.step(context.Background(), saved, true); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 {
		t.Fatal("duplicate created after restart")
	}
	if got, _ := os.ReadFile(filepath.Join(source, "unfinished.txt")); string(got) != "user work" {
		t.Fatal("source checkout changed")
	}
}
func TestSemanticClosedDuplicate(t *testing.T) {
	e, s, f, a, _ := fixture(t)
	f.items = []Issue{{Number: 5, Title: "Crash with zero bytes", Body: "Different wording", State: "closed", Comments: []string{"The parser reads index zero on empty input"}}}
	a.onReview = func(issues []Issue) Review {
		if len(issues) != 1 || len(issues[0].Comments) != 0 || !strings.Contains(issues[0].Body, "index zero") {
			t.Fatalf("discussion missing from review: %+v", issues)
		}
		return Review{Verdict: "duplicate", Reason: "Same underlying empty-input indexing bug", Duplicate: 5}
	}
	if err := e.step(context.Background(), s, true); err != nil {
		t.Fatal(err)
	}
	if f.creates != 0 || s.Completed[0].Status != "duplicate" {
		t.Fatal("closed semantic duplicate was filed")
	}
}

func TestLLMDecidesEvenWhenTitlesMatch(t *testing.T) {
	e, s, f, a, _ := fixture(t)
	f.items = []Issue{{Number: 12, Title: finding().Title, Body: "An unrelated defect with the same generic title", State: "open"}}
	a.onReview = func(issues []Issue) Review {
		if len(issues) != 1 || issues[0].Number != 12 {
			t.Fatalf("LLM did not receive existing issue: %+v", issues)
		}
		return Review{Verdict: "new", Reason: "Same title, but a different trigger and root cause"}
	}
	if err := e.step(context.Background(), s, true); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 || a.reviews != 1 {
		t.Fatal("title matching overrode the LLM's decision")
	}
}
func TestNewIssueDuringReviewIsCompared(t *testing.T) {
	e, s, f, a, _ := fixture(t)
	a.onReview = func(issues []Issue) Review {
		if len(issues) == 0 {
			f.items = []Issue{{Number: 9, Title: "Parser regression with empty text", State: "open"}}
			return Review{Verdict: "new", Reason: "Reproduced"}
		}
		return Review{Verdict: "duplicate", Reason: "New human report has the same cause", Duplicate: 9}
	}
	if err := e.step(context.Background(), s, true); err != nil {
		t.Fatal(err)
	}
	if f.creates != 0 || a.reviews != 2 {
		t.Fatalf("missed concurrent issue: creates %d, reviews %d", f.creates, a.reviews)
	}
}
func TestIssueAppearsImmediatelyBeforePost(t *testing.T) {
	e, s, f, _, _ := fixture(t)
	f.onRead = func(f *fakeSource) error {
		if f.reads == 4 {
			f.items = []Issue{{Number: 8, Title: finding().Title, Body: finding().RootCause, State: "open"}}
		}
		return nil
	}
	if err := e.step(context.Background(), s, true); err != nil {
		t.Fatal(err)
	}
	if f.creates != 0 {
		t.Fatal("failed to refresh before POST")
	}
}
func TestLostCreateResponseReconcilesAtAttemptLimit(t *testing.T) {
	e, s, f, a, _ := fixture(t)
	e.config.Attempts = 1
	f.lost = true
	if err := e.step(context.Background(), s, true); err == nil {
		t.Fatal("expected transport error")
	}
	if s.Scan.Candidates[0].Status != "posting" {
		t.Fatal("POST intent was not retained")
	}
	saved, err := ReadState(e.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.step(context.Background(), saved, true); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 || a.scans != 1 || saved.Scan != nil || saved.Completed[0].Status != "submitted" {
		t.Fatal("lost response was not reconciled without a second POST")
	}
}
func TestUnknownCreateNeverBlindlyRetries(t *testing.T) {
	e, s, f, _, _ := fixture(t)
	f.createErr = errors.New("unknown outcome")
	if err := e.step(context.Background(), s, true); err == nil {
		t.Fatal("expected error")
	}
	for range 3 {
		if err := e.step(context.Background(), s, true); err == nil {
			t.Fatal("unknown outcome accepted")
		}
	}
	if f.creates != 1 {
		t.Fatal("POST was retried")
	}
	t.Setenv("XDG_STATE_HOME", canonicalTestDir(t))
	if Retry(e.config) == nil {
		t.Fatal("retry allowed ambiguous POST")
	}
}
func TestDryRunThenRealRun(t *testing.T) {
	e, s, f, _, _ := fixture(t)
	e.config.DryRun = true
	if err := e.step(context.Background(), s, true); err != nil {
		t.Fatal(err)
	}
	if f.creates != 0 || s.Completed[0].Status != "dry_run" {
		t.Fatal("dry run published")
	}
	e.config.DryRun = false
	if err := e.step(context.Background(), s, true); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 {
		t.Fatal("dry run suppressed later publication")
	}
}
func TestIncompleteHistoryAndUncertainReviewFailClosed(t *testing.T) {
	for _, mode := range []string{"history", "uncertain", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			e, s, f, a, _ := fixture(t)
			if mode == "history" {
				f.onRead = func(*fakeSource) error { return errors.New("GitHub unavailable") }
			} else {
				a.onReview = func([]Issue) Review {
					return Review{Verdict: mode, Reason: "Cannot establish a distinct reproducible defect"}
				}
			}
			err := e.step(context.Background(), s, true)
			if mode == "history" && err == nil {
				t.Fatal("history error hidden")
			}
			if mode != "history" && err != nil {
				t.Fatal(err)
			}
			if f.creates != 0 {
				t.Fatal("published without complete evidence")
			}
		})
	}
}
func TestScanPublishesPinnedCommitWhenBranchAdvances(t *testing.T) {
	e, s, f, a, source := fixture(t)
	scanned := localGit(t, source, "rev-parse", "HEAD")
	a.onScan = func() {
		localGit(t, source, "switch", "main")
		writeTestFile(t, filepath.Join(source, "next.txt"), "new commit")
		localGit(t, source, "add", ".")
		localGit(t, source, "commit", "-m", "advance")
		localGit(t, source, "push", "origin", "main")
	}
	if err := e.step(context.Background(), s, true); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 || len(f.items) != 1 {
		t.Fatalf("finding was not published: creates %d, issues %d", f.creates, len(f.items))
	}
	if advanced := localGit(t, source, "rev-parse", "HEAD"); advanced == scanned {
		t.Fatal("fixture branch did not advance")
	}
	if !strings.Contains(f.items[0].Body, scanned) {
		t.Fatal("published finding does not identify the immutable scanned commit")
	}
}
func TestChangedSourceAndMissingSourceRefused(t *testing.T) {
	for _, mode := range []string{"edit", "missing"} {
		t.Run(mode, func(t *testing.T) {
			e, s, f, a, _ := fixture(t)
			if mode == "missing" {
				bad := finding()
				bad.Files = []string{"does-not-exist.go"}
				a.findings = []Finding{bad}
			} else {
				e.agent = func(cfg Config) Agent {
					a.onScan = func() { writeTestFile(t, filepath.Join(cfg.Directory, "README.md"), "edited source") }
					return a
				}
			}
			if err := e.step(context.Background(), s, true); err == nil {
				t.Fatal("invalid source accepted")
			}
			if f.creates != 0 {
				t.Fatal("published invalid source evidence")
			}
		})
	}
}
func TestSetupFailureDoesNotConsumeAttempt(t *testing.T) {
	e, s, _, a, _ := fixture(t)
	a.err = &runner.SetupError{Err: errors.New("missing model")}
	var slept []time.Duration
	e.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	if err := e.step(context.Background(), s, true); err == nil {
		t.Fatal("setup error hidden")
	}
	if s.Scan.Tries != 0 {
		t.Fatal("setup failure consumed attempt")
	}
	if len(slept) != len(startupRetryDelays) {
		t.Fatalf("startup failure retried %d times, want %d", len(slept), len(startupRetryDelays))
	}
}
func TestTransientStartupFailureRecovers(t *testing.T) {
	e, s, f, a, _ := fixture(t)
	failures := 2
	a.onExecute = func() error {
		if failures > 0 {
			failures--
			return &runner.SetupError{Err: errors.New("Codex process has exited with code 1: failed to initialize sqlite state runtime")}
		}
		return nil
	}
	var slept []time.Duration
	e.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	if err := e.step(context.Background(), s, true); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 || len(slept) != 2 || slept[0] != startupRetryDelays[0] || slept[1] != startupRetryDelays[1] {
		t.Fatalf("creates %d, slept %v", f.creates, slept)
	}
}
func TestStartupRetrySkipsPermanentAndPromptFailures(t *testing.T) {
	for _, err := range []error{
		&runner.SetupError{Err: fmt.Errorf("launch ACP agent: %w", &exec.Error{Name: "codex-acp", Err: exec.ErrNotFound})},
		errors.New("prompt failed after start"),
	} {
		e, s, _, a, _ := fixture(t)
		a.err = err
		slept := 0
		e.sleep = func(context.Context, time.Duration) error { slept++; return nil }
		if e.step(context.Background(), s, true) == nil {
			t.Fatal("error hidden")
		}
		if slept != 0 {
			t.Fatalf("retried %v", err)
		}
	}
}
func TestStartupRetryStopsWhenCancelled(t *testing.T) {
	e, s, _, a, _ := fixture(t)
	a.err = &runner.SetupError{Err: errors.New("exited")}
	ctx, cancel := context.WithCancel(context.Background())
	e.sleep = func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }
	if err := e.step(ctx, s, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if s.Scan.Tries != 0 {
		t.Fatalf("cancelled startup retry consumed attempt: %+v", s.Scan)
	}
}
func TestBatchDuplicatesAndZeroFindings(t *testing.T) {
	for _, empty := range []bool{true, false} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			e, s, f, a, _ := fixture(t)
			if empty {
				a.findings = []Finding{}
			} else {
				a.findings = []Finding{finding(), finding()}
			}
			if err := e.step(context.Background(), s, true); err != nil {
				t.Fatal(err)
			}
			want := 1
			if empty {
				want = 0
			}
			if f.creates != want {
				t.Fatalf("created %d, want %d", f.creates, want)
			}
		})
	}
}
func TestOperatorVerifierFailureStopsPublication(t *testing.T) {
	e, s, f, _, _ := fixture(t)
	e.config.Verify = []string{"sh", "-c", "test -n \"$BUG_COMMIT\" && test -n \"$BUG_FINDING\" && exit 7"}
	if err := e.step(context.Background(), s, true); err == nil || !strings.Contains(err.Error(), "operator verification") {
		t.Fatalf("wrong error %v", err)
	}
	if f.creates != 0 {
		t.Fatal("failed verification still published")
	}
}
