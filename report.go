package bugbot

import (
	"fmt"
	"io"
	"strings"
)

// Report writes all matching saved findings as Markdown without changing state.
// An empty status includes every saved outcome.
func Report(cfg Config, status string, output io.Writer) error {
	if status != "" && !validFindingStatus(status) {
		return fmt.Errorf("unsupported report status %q; use pending, posting, submitted, duplicate, uncertain, invalid, dry_run, or stale", status)
	}
	s, err := ReadState(cfg)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Saved findings\n\nRepository: %s/%s\n\nBranch: %s\n\n", cfg.GitHub.Host, cfg.GitHubRepo(), cfg.Branch)
	if status != "" {
		fmt.Fprintf(&b, "Status filter: %s\n\n", status)
	}
	count := 0
	appendFinding := func(c *Candidate, source, commit string) {
		if status != "" && c.Status != status {
			return
		}
		count++
		fmt.Fprintf(&b, "## %d. %s\n\nStatus: %s\n\nSource: %s\n\nCommit: %s\n\n", count, c.Finding.Title, c.Status, source, commit)
		if c.URL != "" {
			fmt.Fprintf(&b, "Issue: %s\n\n", c.URL)
		}
		for _, section := range findingDetailSections(c) {
			fmt.Fprintf(&b, "### %s\n\n%s\n\n", section.title, section.body)
		}
	}
	if s != nil {
		for _, c := range s.Completed {
			appendFinding(c, "completed scan", "unavailable (not retained for completed findings)")
		}
		if s.Scan != nil {
			for _, c := range s.Scan.Candidates {
				appendFinding(c, "active scan", s.Scan.Commit)
			}
		}
	}
	if count == 0 {
		b.WriteString("No saved findings match this selection.\n")
	}
	_, err = io.WriteString(output, b.String())
	return err
}
