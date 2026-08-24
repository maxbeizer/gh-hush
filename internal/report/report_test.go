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

func TestWriteNeverEmitsAnAPIOrMalformedDecisionURL(t *testing.T) {
	for _, candidate := range []string{
		"https://api.github.com/repos/example/repo/issues/1",
		"https://api.github.test/repos/example/repo/issues/1",
		"://malformed",
	} {
		decision := model.Decision{
			Thread: model.Notification{
				Repository: model.Repository{FullName: "example/repo", HTMLURL: "https://github.com/example/repo/"},
				Subject:    model.Subject{Title: "Subject", Type: "Issue", URL: candidate},
			},
			URL: candidate, Action: model.ActionKeep,
		}
		var output bytes.Buffer
		if err := Write(&output, []model.Decision{decision}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), candidate) || !strings.Contains(output.String(), "URL: https://github.com/example/repo") {
			t.Fatalf("candidate %q escaped URL contract: %s", candidate, output.String())
		}
	}
}

func TestWriteZeroUnreadNotifications(t *testing.T) {
	var output bytes.Buffer
	if err := Write(&output, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "zero unread notifications") {
		t.Fatalf("output=%s", output.String())
	}
}
