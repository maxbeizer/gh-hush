package manifest

import (
	"strings"
	"testing"

	"github.com/maxbeizer/gh-hush/internal/model"
)

func TestBuildIncludesOnlyExplicitUnsubscribes(t *testing.T) {
	decisions := []model.Decision{
		{
			Thread: model.Notification{ID: "keep"},
			Action: model.ActionKeep,
		},
		{
			Thread: model.Notification{
				ID:         "unsubscribe",
				Reason:     "subscribed",
				Repository: model.Repository{FullName: "example/repo"},
				Subject:    model.Subject{Type: "Issue"},
			},
			URL:    "https://github.com/example/repo/issues/1",
			Action: model.ActionUnsubscribe,
			Rules:  []model.Rule{{ID: "unsubscribe.all_other_notifications", Evidence: "no keep rule matched"}},
		},
	}

	got := Build("octocat", "config-hash", decisions)
	if got.Reviewed {
		t.Fatal("Build() reviewed = true, want false")
	}
	if len(got.Actions) != 1 || got.Actions[0].ThreadID != "unsubscribe" {
		t.Fatalf("Build() actions = %#v, want only explicit unsubscribe", got.Actions)
	}
}

func TestValidateForFutureApply(t *testing.T) {
	valid := Manifest{
		SchemaVersion:     schemaVersion,
		AuthenticatedUser: "octocat",
		Reviewed:          true,
		Actions: []Action{{
			ThreadID: "123",
			Action:   model.ActionUnsubscribe,
		}},
	}

	tests := []struct {
		name    string
		change  func(*Manifest)
		wantErr string
	}{
		{name: "valid"},
		{name: "must be reviewed", change: func(m *Manifest) { m.Reviewed = false }, wantErr: "explicitly marked reviewed"},
		{name: "user must match", change: func(m *Manifest) { m.AuthenticatedUser = "other" }, wantErr: "does not match"},
		{name: "thread id required", change: func(m *Manifest) { m.Actions[0].ThreadID = "" }, wantErr: "not an explicit unsubscribe target"},
		{name: "unsubscribe action required", change: func(m *Manifest) { m.Actions[0].Action = model.ActionKeep }, wantErr: "not an explicit unsubscribe target"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := valid
			candidate.Actions = append([]Action(nil), valid.Actions...)
			if tt.change != nil {
				tt.change(&candidate)
			}
			err := ValidateForFutureApply(candidate, "octocat")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateForFutureApply() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateForFutureApply() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}
