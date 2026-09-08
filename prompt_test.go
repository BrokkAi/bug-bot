package bugbot

import (
	"strings"
	"testing"
)

func TestReceiptsRejectMissingEvidenceAndPartialReview(t *testing.T) {
	for _, raw := range []string{
		`BUG_RESULT {"summary":"Checked parser","findings":null}`,
		`BUG_RESULT {"summary":"Checked parser","findings":[{"title":"Bug"}]}`,
		`BUG_RESULT {"summary":"Checked parser","findings":[],"unknown":true}`,
		"BUG_RESULT {\"summary\":\"Checked parser\",\"findings\":[]}\ntrailing",
	} {
		if _, err := parseScan(raw, 3); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	issues := []Issue{{Number: 1}, {Number: 2}}
	for _, raw := range []string{
		`BUG_REVIEW {"verdict":"new","reason":"Different","checked":[1]}`,
		`BUG_REVIEW {"verdict":"new","reason":"Different","checked":[1,1]}`,
		`BUG_REVIEW {"verdict":"duplicate","reason":"Same","checked":[1,2],"duplicate":3}`,
		`BUG_REVIEW {"verdict":"new","reason":"","checked":[1,2]}`,
	} {
		if _, err := parseReview(raw, issues); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := parseReview(`BUG_REVIEW {"verdict":"new","reason":"Different causes","checked":[2,1]}`, issues); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalReceiptWithJoinedMessages(t *testing.T) {
	valid := `BUG_REVIEW {"verdict":"new","reason":"Verified; BUG_REVIEW { is the receipt marker","checked":[]}`
	for _, text := range []string{valid, "Completed the reproduction." + valid, "Earlier BUG_REVIEW example." + valid, "Commentary\n" + valid + "\n"} {
		if _, err := parseReview(text, nil); err != nil {
			t.Errorf("valid final receipt rejected: %v", err)
		}
	}
	for _, text := range []string{
		valid + " superseded", valid + "\nActually, I cannot verify this.", valid + "\n```",
		valid + `BUG_REVIEW {"verdict":`, valid + ` {"verdict":"invalid"}`,
		`Progress.BUG_REVIEW {"verdict":"new","reason":"Verified","checked":[],"unexpected":true}`,
	} {
		if _, err := parseReview(text, nil); err == nil {
			t.Errorf("accepted nonterminal or malformed receipt: %s", text)
		}
	}
}
func TestReviewChunksPreserveAllTextAndIssueNumbers(t *testing.T) {
	body := strings.Repeat("é\n", 30000)
	issues := []Issue{{Number: 1, Title: "Large", Body: body, Comments: []string{"critical evidence"}}}
	for n := 2; n < 50; n++ {
		issues = append(issues, Issue{Number: n, Title: "Other", Body: "small"})
	}
	chunks := reviewChunks(issues)
	if len(chunks) < 5 {
		t.Fatal("history was not split")
	}
	var joined string
	seen := map[int]bool{}
	for _, chunk := range chunks {
		within := map[int]bool{}
		for _, i := range chunk {
			if within[i.Number] {
				t.Fatal("duplicate issue number within chunk")
			}
			within[i.Number] = true
			seen[i.Number] = true
			if i.Number == 1 {
				joined += i.Body
			}
		}
	}
	if joined != body+"\n\nIssue comment:\ncritical evidence" || len(seen) != len(issues) {
		t.Fatal("history truncated")
	}
}
