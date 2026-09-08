package bugbot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Reproduces the real ACP message sequence that previously lost valid receipts.
func TestFinalReceiptAfterCommentary(t *testing.T) {
	e, s, source, _, _ := fixture(t)
	script := filepath.Join(canonicalTestDir(t), "agent.py")
	writeTestFile(t, script, `import json, os, sys
def send(value):
    print(json.dumps(value), flush=True)
def chunk(message, phase, text, kind='agent_message_chunk'):
    send(dict(jsonrpc='2.0', method='session/update', params=dict(
        sessionId='fixture', update=dict(sessionUpdate=kind, messageId=message,
        content=dict(type='text', text=text), _meta=dict(codex=dict(phase=phase))))))
for line in sys.stdin:
    request = json.loads(line)
    method = request.get('method')
    if method == 'initialize':
        result = dict(protocolVersion=1, agentCapabilities={}, authMethods=[])
    elif method == 'session/new':
        result = dict(sessionId='fixture')
    elif method == 'session/prompt':
        prompt = request['params']['prompt'][0]['text']
        if 'Scan context (data):' in prompt:
            chunk('progress', 'commentary', 'The investigation is complete.')
            chunk('scan', 'final_answer', os.environ['BUG_DIG_SCAN'])
        else:
            chunk('progress', 'commentary', 'The reproduction passed.')
            chunk('thought', 'analysis', 'Finalizing the JSON object', 'agent_thought_chunk')
            chunk('review', 'final_answer', 'BUG_')
            chunk('review', 'final_answer', 'REVIEW ' + os.environ['BUG_DIG_REVIEW'])
        result = dict(stopReason='end_turn')
    else:
        continue
    send(dict(jsonrpc='2.0', id=request['id'], result=result))
`)
	e.agent = func(cfg Config) Agent {
		cfg.Agent.Command = []string{"python3", script}
		cfg.Agent.Environment = map[string]string{
			"BUG_DIG_SCAN":   "BUG_RESULT " + jsonContextCompact(ScanResult{Summary: "Reproduced parser failure", Findings: []Finding{finding()}}),
			"BUG_DIG_REVIEW": jsonContextCompact(Review{Verdict: "new", Reason: "Independently reproduced", Checked: []int{}}),
		}
		return agentProcess{config: cfg, log: e.log}
	}
	err := e.step(context.Background(), s, true)
	if err != nil || source.creates != 1 {
		t.Fatalf("valid final ACP receipt must publish: err=%v, creates=%d", err, source.creates)
	}
}

func TestCanonicalRepositoryCaseReconciles(t *testing.T) {
	e, s, source, _, _ := fixture(t)
	// The fake GitHub source returns canonical o/r URLs. This accepted config
	// names the same repository with different capitalization.
	e.config.GitHub.Repo = "O/R"
	e.config.GitHub.Host = "GitHub.COM"
	s.Repo = e.config.GitHubRepo()
	s.Host = e.config.GitHub.Host
	if err := e.config.Validate(); err != nil {
		t.Fatal(err)
	}
	source.lost = true
	first := e.step(context.Background(), s, true)
	if first == nil || !strings.Contains(first.Error(), "connection lost") {
		t.Fatalf("expected lost response, got %v", first)
	}
	if source.creates != 1 || len(source.items) != 1 {
		t.Fatal("fixture did not create the issue")
	}
	saved, err := ReadState(e.config)
	if err != nil {
		t.Fatal(err)
	}
	second := e.step(context.Background(), saved, true)
	if second != nil || saved.Scan != nil || len(saved.Completed) != 1 || saved.Completed[0].Status != "submitted" || source.creates != 1 {
		t.Fatalf("same-repository issue must reconcile: first=%v; restart=%v; creates=%d", first, second, source.creates)
	}
}

func TestDefinitePostRejectionCanRetry(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "explicit"}[explicit], func(t *testing.T) {
			testDefinitePostRejectionCanRetry(t, explicit)
		})
	}
}

func testDefinitePostRejectionCanRetry(t *testing.T, explicit bool) {
	e, s, _, _, _ := fixture(t)
	if explicit {
		e.config.Attempts = 1
	}
	dir := canonicalTestDir(t)
	gh := filepath.Join(dir, "gh")
	// A definite rejected create, as opposed to an ambiguous lost response.
	writeTestFile(t, gh, `#!/usr/bin/env python3
import json, os, sys
if '--method' in sys.argv:
    assert '--include' in sys.argv
    if os.environ.get('BUG_DIG_ACCEPT_POST') != '1':
        print('HTTP/2.0 422 Unprocessable Entity\r\nContent-Type: application/json\r\n\r\n{"message":"Validation Failed"}')
        print('gh: Validation Failed (HTTP 422)', file=sys.stderr)
        sys.exit(1)
    body = next(arg[5:] for arg in sys.argv if arg.startswith('body='))
    print('HTTP/2.0 201 Created\r\nContent-Type: application/json\r\n\r\n', end='')
    print(json.dumps(dict(number=1, html_url='https://github.com/o/r/issues/1', body=body)))
    sys.exit(0)
print('[]')
`)
	if err := os.Chmod(gh, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", canonicalTestDir(t))
	e.source = githubClient{config: e.config}
	first := e.step(context.Background(), s, true)
	if first == nil || !strings.Contains(first.Error(), "HTTP 422") {
		t.Fatalf("expected definite API rejection, got %v", first)
	}
	// A confirmed rejection must survive restart as pending, so correcting the
	// request allows either automatic recovery or retry after budget exhaustion.
	t.Setenv("BUG_DIG_ACCEPT_POST", "1")
	saved, err := ReadState(e.config)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Scan.Candidates[0].Status != "pending" {
		t.Fatalf("rejected create left status %s", saved.Scan.Candidates[0].Status)
	}
	if explicit {
		if err := Retry(e.config); err != nil {
			t.Fatal(err)
		}
		saved, err = ReadState(e.config)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := e.step(context.Background(), saved, true); err != nil {
		t.Fatal(err)
	}
	if saved.Scan != nil || len(saved.Completed) != 1 || saved.Completed[0].Status != "submitted" {
		t.Fatal("corrected create did not complete")
	}
}
