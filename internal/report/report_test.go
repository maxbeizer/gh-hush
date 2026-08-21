package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maxbeizer/gh-hush/internal/model"
)

func TestWriteIncludesDryRunGuaranteesAndDecisionFields(t *testing.T) {
	decision := model.Decision{
		Thread: model.Notification{
			Reason:     "mention",
			Repository: model.Repository{FullName: "example/repo"},
			Subject: model.Subject{
				Title: "Example issue",
				Type:  "Issue",
			},
		},
		URL:    "https://github.com/example/repo/issues/1",
		Action: model.ActionKeep,
		Rules: []model.Rule{{
			ID:       "keep.personal_mention",
			Evidence: "personal mention",
		}},
	}
	var output bytes.Buffer
	if err := Write(&output, []model.Decision{decision}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	for _, required := range []string{
		"dry run (read-only)",
		"No GitHub mutations were made",
		"URL:",
		"Subject type:",
		"Repository:",
		"Notification reason:",
		"Proposed action:",
		"Matching rules:",
		"keep.personal_mention",
		"Summary:",
	} {
		if !strings.Contains(output.String(), required) {
			t.Errorf("Write() output missing %q", required)
		}
	}
}
