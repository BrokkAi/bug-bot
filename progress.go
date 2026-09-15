package bugbot

import (
	"context"
	"strings"
	"time"
)

// Progress is an owned snapshot of the scan, suitable for a live display.
// Counts cover all saved findings; Findings contains the latest 200, oldest first.
// An empty Phase updates saved results without changing the current activity.
type Progress struct {
	Phase, Task, Commit  string
	Attempt, MaxAttempts int
	WakeAt               time.Time
	Failure              string
	Findings             []FindingProgress
	Counts               FindingCounts
}

type FindingProgress struct {
	ID, Title, Status, URL, Body string
}

type FindingCounts struct {
	Found, Filed, Duplicates, Pending, DryRun, Skipped int
}

type progressKey struct{}

// WithProgress observes progress synchronously. The callback should return promptly.
// It receives no mutable scan state and must not perform actions on the repository.
func WithProgress(ctx context.Context, observe func(Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, observe)
}

func (e engine) report(s *State, phase, task string) {
	if e.observe == nil {
		return
	}
	p := Progress{Phase: phase, Task: task, MaxAttempts: e.config.Attempts, WakeAt: s.NextScan}
	candidates := append([]*Candidate(nil), s.Completed...)
	if s.Scan != nil {
		p.Commit, p.Attempt = s.Scan.Commit, s.Scan.Tries
		p.WakeAt, p.Failure = s.Scan.RetryAt, s.Scan.Failure
		candidates = append(candidates, s.Scan.Candidates...)
	}
	for i, c := range candidates {
		p.Counts.Found++
		switch c.Status {
		case "submitted":
			p.Counts.Filed++
		case "duplicate":
			p.Counts.Duplicates++
		case "pending", "posting":
			p.Counts.Pending++
		case "dry_run":
			p.Counts.DryRun++
		default:
			p.Counts.Skipped++
		}
		if i >= len(candidates)-200 {
			p.Findings = append(p.Findings, FindingProgress{ID: c.RequestID, Title: c.Finding.Title, Status: c.Status, URL: c.URL, Body: findingDetails(c)})
		}
	}
	if phase == "waiting" || phase == "paused" {
		// The daemon checks eligibility on the next poll, even when RetryAt is sooner.
		if poll := e.now().Add(time.Duration(e.config.Poll)); p.WakeAt.Before(poll) {
			p.WakeAt = poll
		}
	}
	e.observe(p)
}

// Completed candidates do not store their original commit. Do not label old
// findings with the current scan's commit or display publication request markers.
func findingDetails(c *Candidate) string {
	var sections []string
	for _, section := range findingDetailSections(c) {
		sections = append(sections, section.title+"\n"+section.body)
	}
	return strings.Join(sections, "\n\n")
}

type findingDetailSection struct {
	title, body string
}

func findingDetailSections(c *Candidate) []findingDetailSection {
	f := c.Finding
	return []findingDetailSection{
		{"Root cause", f.RootCause},
		{"Files", strings.Join(f.Files, "\n")},
		{"Reproduction", f.Reproduction},
		{"Expected", f.Expected},
		{"Actual", f.Actual},
		{"Evidence", strings.Join(f.Evidence, "\n")},
		{"Review", c.Review},
	}
}
