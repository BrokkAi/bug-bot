package bugbot

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reportFixture(t *testing.T) (Config, *State) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Remote = "https://github.com/o/r.git"
	cfg.Directory = filepath.Join(t.TempDir(), "private-checkout")
	cfg.StateDirectory = t.TempDir()
	return cfg, newState(cfg)
}

func TestReportAllSavedFindings(t *testing.T) {
	cfg, s := reportFixture(t)
	statuses := []string{"submitted", "duplicate", "pending", "posting", "uncertain", "invalid", "dry_run", "stale"}
	for i := 0; i < 250; i++ {
		f := finding()
		f.Title = fmt.Sprintf("Saved finding %03d", i)
		s.Completed = append(s.Completed, &Candidate{RequestID: fmt.Sprintf("%032x", i), Finding: f, Status: statuses[i%len(statuses)], Review: "Review evidence\nwith another line", URL: "https://github.com/o/r/issues/1"})
	}
	s.Scan = &Scan{Commit: strings.Repeat("a", 40), Directory: filepath.Join(cfg.Directory+"-scans", "scan-private"), Candidates: []*Candidate{{RequestID: strings.Repeat("b", 32), Finding: finding(), Status: "dry_run", Review: "Active review"}}}
	if err := writeState(cfg, s); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.StateDirectory, "state.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Report(cfg, "", &out); err != nil {
		t.Fatal(err)
	}
	report := out.String()
	if strings.Count(report, "\n## ") != 251 || strings.Count(report, "Commit: unavailable") != 250 || strings.Count(report, s.Scan.Commit) != 1 {
		t.Fatal("incorrect finding count or commit provenance")
	}
	for _, c := range s.Completed {
		if strings.Count(report, c.Finding.Title) != 1 {
			t.Fatalf("missing or repeated %s", c.Finding.Title)
		}
	}
	for _, want := range append([]string{"Repository: github.com/o/r", "Branch: master", "Active review", s.Completed[0].URL, s.Completed[0].Review, finding().Reproduction, finding().Expected, finding().Actual}, finding().Evidence...) {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, status := range statuses {
		if !strings.Contains(report, "Status: "+status+"\n") {
			t.Errorf("missing status %s", status)
		}
	}
	for _, private := range []string{cfg.Directory, cfg.StateDirectory, s.Scan.Directory, s.Completed[0].RequestID, "<!--"} {
		if strings.Contains(report, private) {
			t.Errorf("private metadata leaked: %q", private)
		}
	}
	out.Reset()
	if err := Report(cfg, "dry_run", &out); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "\n## ") != 32 || strings.Contains(out.String(), "Status: submitted") {
		t.Fatal("incorrect filtered selection")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("report changed saved state")
	}
}

func TestReportEmptyAndValidation(t *testing.T) {
	cfg, s := reportFixture(t)
	var out strings.Builder
	if err := Report(cfg, "", &out); err != nil || !strings.Contains(out.String(), "No saved findings") {
		t.Fatalf("absent state: %v %s", err, &out)
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDirectory, "state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("report created state")
	}
	if err := writeState(cfg, s); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Report(cfg, "dry_run", &out); err != nil || !strings.Contains(out.String(), "No saved findings") {
		t.Fatalf("empty selection: %v", err)
	}
	s.Completed = []*Candidate{{RequestID: strings.Repeat("c", 32), Finding: finding(), Status: "submitted"}}
	if err := writeState(cfg, s); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Report(cfg, "dry_run", &out); err != nil || !strings.Contains(out.String(), "No saved findings") || strings.Contains(out.String(), "## 1.") {
		t.Fatalf("nonmatching selection: %v %s", err, &out)
	}
	if err := Report(cfg, "unknown", &out); err == nil || !strings.Contains(err.Error(), "unsupported report status") {
		t.Fatalf("invalid filter: %v", err)
	}
	for _, raw := range []string{"{", `{"format":2}`} {
		if err := os.WriteFile(filepath.Join(cfg.StateDirectory, "state.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		_, want := ReadState(cfg)
		out.Reset()
		err := Report(cfg, "", &out)
		if err == nil || err.Error() != want.Error() || out.Len() != 0 {
			t.Fatalf("validation error: %v, want %v", err, want)
		}
	}
}

type failedReportWriter struct{}

func (failedReportWriter) Write([]byte) (int, error) { return 0, errors.New("output failed") }
func TestReportOutputError(t *testing.T) {
	cfg, _ := reportFixture(t)
	if err := Report(cfg, "", failedReportWriter{}); err == nil || err.Error() != "output failed" {
		t.Fatalf("output error lost: %v", err)
	}
}
