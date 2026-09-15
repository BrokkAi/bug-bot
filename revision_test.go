package bugbot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExhaustedScanChecksRevision(t *testing.T) {
	for _, force := range []bool{false, true} {
		for _, changed := range []bool{false, true} {
			name := map[bool]string{false: "poll", true: "once"}[force] + "/" + map[bool]string{false: "unchanged", true: "advanced"}[changed]
			t.Run(name, func(t *testing.T) {
				e, s, f, a, source := fixture(t)
				e.config.Attempts = 1
				f.createErr = &rejectedCreateError{errors.New("confirmed rejection")}
				if err := e.step(context.Background(), s, true); err == nil {
					t.Fatal("expected rejection")
				}
				old := s.Scan
				candidate := *old.Candidates[0]
				evidence := filepath.Join(old.Directory, "reproduction.txt")
				writeTestFile(t, evidence, "saved reproduction")
				if changed {
					localGit(t, source, "commit", "--allow-empty", "-m", "advance")
					localGit(t, source, "push", "origin", "HEAD:main")
				}
				saved, err := ReadState(e.config)
				if err != nil {
					t.Fatal(err)
				}
				f.createErr = nil
				// Keep the retry delay in the future, including for daemon polls.
				e.now = func() time.Time { return old.RetryAt.Add(-time.Second) }
				a.onScan = func() {
					if saved.Scan.Commit == old.Commit || saved.Scan.Directory == old.Directory || saved.Scan.Tries != 1 {
						t.Fatalf("new revision did not get a separate scan and budget: %+v", saved.Scan)
					}
				}
				err = e.step(context.Background(), saved, force)
				if !changed {
					if err == nil || !strings.Contains(err.Error(), "scan attempt budget exhausted") {
						t.Fatalf("expected exhausted budget, got %v", err)
					}
					if jsonContextCompact(saved.Scan) != jsonContextCompact(old) || a.scans != 1 || f.creates != 1 {
						t.Fatal("unchanged exhausted scan was modified or retried")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if saved.Scan != nil || a.scans != 2 || f.creates != 2 || len(saved.Completed) != 2 {
					t.Fatalf("new revision was not scanned: %+v", saved)
				}
				candidate.Status = "stale"
				if !reflect.DeepEqual(*saved.Completed[0], candidate) || saved.Completed[1].Status != "submitted" {
					t.Fatal("old finding/evidence or new publication was lost")
				}
				if len(saved.History) != 2 || !strings.HasPrefix(saved.History[0], old.Commit+":") {
					t.Fatal("old scan history was lost")
				}
				if data, err := os.ReadFile(evidence); err != nil || string(data) != "saved reproduction" {
					t.Fatal("old workspace evidence was lost")
				}
			})
		}
	}
}

func TestRevisionWaitsForAllUnknownPublications(t *testing.T) {
	e, s, f, a, source := fixture(t)
	e.config.Attempts = 1
	a.findings = []Finding{finding(), finding(), finding()}
	f.lost = true
	if err := e.step(context.Background(), s, true); err == nil {
		t.Fatal("expected lost response")
	}
	old := s.Scan
	// Simulate another outstanding write in saved state, with a pending finding
	// after it. Every uncertain write must reconcile before invalidation.
	second := old.Candidates[1]
	second.Status = "posting"
	if err := e.save(s); err != nil {
		t.Fatal(err)
	}
	localGit(t, source, "commit", "--allow-empty", "-m", "advance")
	localGit(t, source, "push", "origin", "HEAD:main")
	saved, err := ReadState(e.config)
	if err != nil {
		t.Fatal(err)
	}
	err = e.step(context.Background(), saved, true)
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("expected unknown publication, got %v", err)
	}
	if saved.Scan.Commit != old.Commit || len(saved.Completed) != 0 || saved.Scan.Candidates[0].Status != "submitted" || saved.Scan.Candidates[1].Status != "posting" || saved.Scan.Candidates[2].Status != "pending" || a.scans != 1 || f.creates != 1 {
		t.Fatal("changed scan ownership before all writes reconciled")
	}
	head, err := (checkout{e.config}).head(context.Background())
	if err != nil || head != old.Commit {
		t.Fatal("fetched new revision before reconciliation")
	}
	f.items = append(f.items, Issue{Number: 102, URL: "https://github.com/o/r/issues/102", Body: issueBody(second, old.Commit)})
	f.lost = false
	if err := e.step(context.Background(), saved, true); err != nil {
		t.Fatal(err)
	}
	if saved.Scan != nil || a.scans != 2 || f.creates != 1 || len(saved.Completed) != 6 {
		t.Fatal("recovery did not scan new revision without reposting")
	}
	for i, want := range []string{"submitted", "submitted", "stale"} {
		if saved.Completed[i].Status != want {
			t.Fatalf("old finding %d: got %s, want %s", i, saved.Completed[i].Status, want)
		}
	}
}
