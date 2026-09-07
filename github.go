package bugbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BrokkAi/bug-bot/internal/osrun"
)

type Issue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	URL         string          `json:"html_url"`
	State       string          `json:"state"`
	Comments    []string        `json:"discussion,omitempty"`
	PullRequest json.RawMessage `json:"pull_request,omitempty"`
}
type issueSource interface {
	issues(context.Context) ([]Issue, error)
	create(context.Context, *Candidate, string) (*Issue, error)
}
type githubClient struct{ config Config }

func (g githubClient) api(ctx context.Context, endpoint string, result any, fields ...string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	args := []string{"gh", "api", "--hostname", g.config.GitHub.Host, endpoint}
	out, err := osrun.Run(ctx, "", nil, append(args, fields...)...)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(out), result)
}
func (g githubClient) path(suffix string) string { return "repos/" + g.config.GitHubRepo() + suffix }

// Never use GitHub search's result cap, open-only queries, or label filtering for deduplication.
func (g githubClient) issues(ctx context.Context) ([]Issue, error) {
	all, err := pages[Issue](ctx, g, g.path("/issues"), url.Values{"state": {"all"}, "sort": {"created"}, "direction": {"asc"}})
	if err != nil {
		return nil, err
	}
	byNumber := map[int]int{}
	var issues []Issue
	for _, i := range all {
		if len(i.PullRequest) > 0 && string(i.PullRequest) != "null" {
			continue
		}
		if i.Number < 1 || (i.State != "open" && i.State != "closed") {
			return nil, errors.New("incomplete GitHub issue response")
		}
		if _, exists := byNumber[i.Number]; exists {
			return nil, errors.New("unstable issue pagination; retry snapshot")
		}
		byNumber[i.Number] = len(issues)
		issues = append(issues, i)
	}
	// One paginated repository-wide request includes discussion on closed issues too.
	comments, err := pages[struct {
		IssueURL string `json:"issue_url"`
		Body     string `json:"body"`
	}](ctx, g, g.path("/issues/comments"), url.Values{"sort": {"created"}, "direction": {"asc"}})
	if err != nil {
		return nil, err
	}
	for _, c := range comments {
		u, err := url.Parse(c.IssueURL)
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(u.Path[strings.LastIndex(u.Path, "/")+1:])
		if err != nil {
			return nil, errors.New("invalid comment issue URL")
		}
		if at, ok := byNumber[n]; ok {
			issues[at].Comments = append(issues[at].Comments, c.Body)
		}
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Number < issues[j].Number })
	return issues, nil
}
func pages[T any](ctx context.Context, g githubClient, path string, q url.Values) ([]T, error) {
	var all []T
	for page := 1; page <= 10000; page++ {
		q.Set("per_page", "100")
		q.Set("page", fmt.Sprint(page))
		var items []T
		if err := g.api(ctx, path+"?"+q.Encode(), &items); err != nil {
			return nil, err
		}
		if items == nil {
			return nil, errors.New("expected a GitHub array response")
		}
		all = append(all, items...)
		if len(items) < 100 {
			return all, nil
		}
	}
	return nil, errors.New("history exceeded pagination limit; refusing incomplete duplicate check")
}
func marker(requestID string) string { return "<!-- bug-bot: " + requestID + " -->" }
func issueBody(c *Candidate, commit string) string {
	f := c.Finding
	return fmt.Sprintf("## Problem\n\n%s\n\n## Reproduction\n\n%s\n\n## Expected behavior\n\n%s\n\n## Actual behavior\n\n%s\n\n## Affected source\n\n%s\n\n## Evidence\n\n%s\n\n## Independent review\n\n%s\n\nInvestigated at commit `%s` by bug-bot.\n\n%s\n", f.RootCause, f.Reproduction, f.Expected, f.Actual, strings.Join(f.Files, "\n"), strings.Join(f.Evidence, "\n\n"), c.Review, commit, marker(c.RequestID))
}
func (g githubClient) create(ctx context.Context, c *Candidate, commit string) (*Issue, error) {
	fields := []string{"--method", "POST", "-f", "title=" + c.Finding.Title, "-f", "body=" + issueBody(c, commit)}
	for _, l := range g.config.Labels {
		fields = append(fields, "-f", "labels[]="+l)
	}
	var i Issue
	if err := g.api(ctx, g.path("/issues"), &i, fields...); err != nil {
		return nil, err
	}
	if err := validateCreated(g.config, c, &i); err != nil {
		return nil, err
	}
	return &i, nil
}
func validateCreated(cfg Config, c *Candidate, i *Issue) error {
	if i == nil || i.Number < 1 || i.URL != fmt.Sprintf("https://%s/%s/issues/%d", cfg.GitHub.Host, cfg.GitHubRepo(), i.Number) || !strings.Contains(i.Body, marker(c.RequestID)) || len(i.PullRequest) > 0 {
		return errors.New("created issue does not match repository and finding")
	}
	return nil
}
