package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maxbeizer/gh-hush/internal/model"
)

func TestWriteIncludesCompletePreviewAndRenamedAction(t *testing.T) {
	decisions := []model.Decision{
		{Thread: model.Notification{Reason: "mention", Repository: model.Repository{FullName: "example/repo"}, Subject: model.Subject{Title: "Keep", Type: "Issue"}}, URL: "keep-url", Action: model.ActionKeep, Rules: []model.Rule{{ID: "keep.personal_mention", Evidence: "mention"}}},
		{Thread: model.Notification{Reason: "subscribed", Repository: model.Repository{FullName: "example/repo"}, Subject: model.Subject{Title: "Hush", Type: "Release"}}, URL: "hush-url", Action: model.ActionUnsubscribeAndMarkDone, Rules: []model.Rule{{ID: "hush.all_other_notifications", Evidence: "catch all"}}},
	}
	var output bytes.Buffer
	if err := Write(&output, decisions); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"No GitHub mutations were made", "inbox state", "URL:", "Subject type:", "Repository:", "Notification reason:", "Proposed action: unsubscribe_and_mark_done", "Matching rules:", "Summary: 1 keep, 1 propose unsubscribe_and_mark_done, 2 total"} {
		if !strings.Contains(output.String(), required) {
			t.Errorf("missing %q in %s", required, output.String())
		}
	}
}

func TestWriteZeroActiveNotifications(t *testing.T) {
	var output bytes.Buffer
	if err := Write(&output, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "zero active inbox notifications") {
		t.Fatalf("output=%s", output.String())
	}
}
