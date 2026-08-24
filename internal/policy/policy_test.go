package policy

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/maxbeizer/gh-hush/internal/config"
	"github.com/maxbeizer/gh-hush/internal/model"
)

func TestExternalOrganizationProtectionAppliesToEveryType(t *testing.T) {
	for _, subjectType := range []string{"Issue", "PullRequest", "Discussion", "Commit", "Release", "CheckSuite", "RepositoryVulnerabilityAlert"} {
		t.Run(subjectType, func(t *testing.T) {
			source := &testEvidenceSource{}
			d := NewEvaluator(testConfig(), source).Evaluate(context.Background(), thread("1", "other/repo", subjectType, "subscribed"))
			if d.Action != model.ActionKeep || d.Rules[0].ID != ruleExternalOrganization || len(source.calls) != 0 {
				t.Fatalf("decision = %#v, evidence calls = %v", d, source.calls)
			}
		})
	}
}

func TestExplicitHushAllowlistAndUnsupportedSafetyKeep(t *testing.T) {
	for _, subjectType := range []string{"Issue", "PullRequest", "Discussion", "Commit", "Release", "CheckSuite"} {
		t.Run(subjectType, func(t *testing.T) {
			d := NewEvaluator(withEvidenceRulesDisabled(testConfig()), &testEvidenceSource{}).Evaluate(context.Background(), thread("1", "github/repo", subjectType, "subscribed"))
			if d.Action != model.ActionUnsubscribeAndMarkDone || d.Rules[0].ID != ruleAllOther {
				t.Fatalf("decision = %#v", d)
			}
		})
	}
	for _, subjectType := range []string{"", "SecurityAlert", "RepositoryInvitation", "UnknownFutureType"} {
		t.Run("unsupported "+subjectType, func(t *testing.T) {
			source := &testEvidenceSource{}
			d := NewEvaluator(testConfig(), source).Evaluate(context.Background(), thread("1", "github/repo", subjectType, "subscribed"))
			if d.Action != model.ActionKeep || d.Rules[len(d.Rules)-1].ID != ruleSafetyUnsupported || len(source.calls) != 0 {
				t.Fatalf("decision = %#v, evidence calls = %v", d, source.calls)
			}
		})
	}
}

func TestKeepRules(t *testing.T) {
	cfg := testConfig()
	tests := []struct {
		name    string
		item    model.Notification
		subject model.Resource
		rule    string
	}{
		{"mention", thread("1", "github/repo", "Issue", "mention"), model.Resource{}, rulePersonalMention},
		{"assignment reason", thread("1", "github/repo", "Issue", "assign"), model.Resource{}, rulePersonalAssign},
		{"current assignee", thread("1", "github/repo", "Issue", "subscribed"), model.Resource{Assignees: []model.User{{Login: "octocat"}}}, rulePersonalAssign},
		{"individual review", thread("1", "github/repo", "PullRequest", "review_requested"), model.Resource{RequestedReviewers: []model.User{{Login: "octocat"}}}, ruleIndividualReview},
		{"author reason", thread("1", "github/repo", "Release", "author"), model.Resource{}, ruleUserAuthored},
		{"author response", thread("1", "github/repo", "Commit", "subscribed"), model.Resource{Author: model.User{Login: "octocat"}}, ruleUserAuthored},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewEvaluator(cfg, &testEvidenceSource{subject: tt.subject}).Evaluate(context.Background(), tt.item)
			if d.Action != model.ActionKeep || !hasRule(d, tt.rule) {
				t.Fatalf("decision=%#v", d)
			}
		})
	}
}

func TestPreviewDisplayEnrichmentIsIsolatedFromClassification(t *testing.T) {
	item := thread("1", "github/repo", "Issue", "mention")
	item.Repository.HTMLURL = "https://github.test/github/repo/"
	source := &testEvidenceSource{subject: model.Resource{
		HTMLURL:   "https://github.test/github/repo/issues/1",
		Assignees: []model.User{{Login: "octocat"}},
		User:      model.User{Login: "octocat"},
	}}

	d := NewEvaluator(testConfig(), source).EvaluateForPreview(context.Background(), item)
	if d.Action != model.ActionKeep || d.URL != source.subject.HTMLURL || !reflect.DeepEqual(source.calls, []string{"subject"}) {
		t.Fatalf("decision=%#v calls=%v", d, source.calls)
	}
	if len(d.Rules) != 1 || d.Rules[0].ID != rulePersonalMention {
		t.Fatalf("display-only fields changed classification rules: %#v", d.Rules)
	}

	// A partial resource remains useful for display even when its display-only
	// request fails, but the failure is not policy enrichment failure.
	source = &testEvidenceSource{subject: model.Resource{HTMLURL: source.subject.HTMLURL}, subjectErr: errors.New("display unavailable")}
	d = NewEvaluator(testConfig(), source).EvaluateForPreview(context.Background(), item)
	if d.Action != model.ActionKeep || d.EnrichmentError != "" || d.URL != source.subject.HTMLURL || hasRule(d, ruleSafetyFailure) {
		t.Fatalf("display failure decision=%#v", d)
	}
}

func TestDiscussionHistoricalTeamMentionProtectsAndExactMatchIsRequired(t *testing.T) {
	item := thread("1", "github/repo", "Discussion", "team_mention")
	d := NewEvaluator(testConfig(), &testEvidenceSource{comments: []model.Resource{{Body: "old @github/notifications mention"}, {Body: "new comment"}}}).Evaluate(context.Background(), item)
	if d.Action != model.ActionKeep || !hasRule(d, ruleDiscussionTeam) {
		t.Fatalf("decision=%#v", d)
	}
	d = NewEvaluator(testConfig(), &testEvidenceSource{comments: []model.Resource{{Body: "@github/notifications-extra"}}}).Evaluate(context.Background(), item)
	if d.Action != model.ActionUnsubscribeAndMarkDone {
		t.Fatalf("partial mention decision=%#v", d)
	}
}

func TestPartialEvidenceIsEvaluatedAlongsideSafetyFailure(t *testing.T) {
	t.Run("Discussion comment team mention", func(t *testing.T) {
		source := &testEvidenceSource{
			comments:    []model.Resource{{Body: "historical @github/notifications mention"}},
			commentsErr: errors.New("later page unavailable"),
		}
		d := NewEvaluator(testConfig(), source).Evaluate(context.Background(), thread("1", "github/repo", "Discussion", "subscribed"))
		wantRules := []model.Rule{
			{ID: ruleDiscussionTeam, Evidence: "discussion body or complete comment history contains exact team mention @github/notifications"},
			{ID: ruleSafetyFailure, Evidence: "required classification evidence was unavailable: later page unavailable"},
		}
		if d.Action != model.ActionKeep || !reflect.DeepEqual(d.Rules, wantRules) || d.EnrichmentError != "later page unavailable" {
			t.Fatalf("decision=%#v", d)
		}
	})

	t.Run("subject URL", func(t *testing.T) {
		source := &testEvidenceSource{
			subject:    model.Resource{HTMLURL: "https://github.test/issues/1"},
			subjectErr: errors.New("subject decode incomplete"),
		}
		d := NewEvaluator(testConfig(), source).Evaluate(context.Background(), thread("1", "github/repo", "Issue", "subscribed"))
		wantRules := []model.Rule{{ID: ruleSafetyFailure, Evidence: "required classification evidence was unavailable: subject decode incomplete"}}
		if d.Action != model.ActionKeep || d.URL != source.subject.HTMLURL || !reflect.DeepEqual(d.Rules, wantRules) || d.EnrichmentError != "subject decode incomplete" {
			t.Fatalf("decision=%#v", d)
		}
	})
}

func TestRequiredSubjectEvidenceWithEmptyURLIsConservativelyKept(t *testing.T) {
	item := thread("1", "github/repo", "Issue", "subscribed")
	item.Subject.URL = ""
	source := &testEvidenceSource{subjectErr: errors.New("notification subject did not include an API URL")}

	d := NewEvaluator(testConfig(), source).Evaluate(context.Background(), item)
	if d.Action != model.ActionKeep || d.EnrichmentError == "" || !hasRule(d, ruleSafetyFailure) || !reflect.DeepEqual(source.calls, []string{"subject"}) {
		t.Fatalf("decision=%#v calls=%v", d, source.calls)
	}
}

func TestRequiredEvidenceFailureSafetyIsFieldSpecific(t *testing.T) {
	item := thread("1", "github/repo", "Discussion", "subscribed")
	source := &testEvidenceSource{
		subject:     model.Resource{Body: "available", HTMLURL: "https://github.test/discussion/1"},
		commentsErr: errors.New("pages unavailable"),
	}
	d := NewEvaluator(testConfig(), source).Evaluate(context.Background(), item)
	if d.Action != model.ActionKeep || !hasRule(d, ruleSafetyFailure) || d.EnrichmentError != "pages unavailable" {
		t.Fatalf("decision=%#v", d)
	}
	if d.URL != source.subject.HTMLURL {
		t.Fatalf("URL=%q want available subject HTML URL %q", d.URL, source.subject.HTMLURL)
	}
}

func TestPreviewURLValidationAndFallbacks(t *testing.T) {
	for _, subjectType := range []string{"Issue", "PullRequest", "Discussion", "Commit", "Release", "CheckSuite", "RepositoryVulnerabilityAlert", "UnknownFutureType"} {
		t.Run(subjectType, func(t *testing.T) {
			item := thread("1", "github/repo", subjectType, "mention")
			item.Repository.HTMLURL = "https://github.test/github/repo/"
			source := &testEvidenceSource{subject: model.Resource{HTMLURL: "https://github.test/github/repo/subjects/1"}}
			d := NewEvaluator(testConfig(), source).EvaluateForPreview(context.Background(), item)
			if d.URL != source.subject.HTMLURL || !reflect.DeepEqual(source.calls, []string{"subject"}) {
				t.Fatalf("decision=%#v calls=%v", d, source.calls)
			}
		})
	}

	item := thread("1", "github/repo", "Issue", "mention")
	item.Repository.HTMLURL = "https://github.test/github/repo/"
	for _, tt := range []struct {
		name    string
		htmlURL string
		want    string
	}{
		{"empty HTML URL", "", "https://github.test/github/repo/"},
		{"malformed HTML URL", "://bad", "https://github.test/github/repo/"},
		{"GitHub API HTML URL", "https://api.github.com/repos/github/repo/issues/1", "https://github.test/github/repo/"},
		{"other API host", "https://api.github.test/repos/github/repo/issues/1", "https://github.test/github/repo/"},
		{"enterprise API path", "https://github.example/api/v3/repos/github/repo/issues/1", "https://github.test/github/repo/"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := NewEvaluator(testConfig(), &testEvidenceSource{subject: model.Resource{HTMLURL: tt.htmlURL}}).EvaluateForPreview(context.Background(), item)
			if d.URL != tt.want || d.URL == item.Subject.URL {
				t.Fatalf("URL=%q want=%q", d.URL, tt.want)
			}
		})
	}

	item.Subject.URL = ""
	source := &testEvidenceSource{}
	d := NewEvaluator(withEvidenceRulesDisabled(testConfig()), source).EvaluateForPreview(context.Background(), item)
	if d.URL != "https://github.test/github/repo/" || len(source.calls) != 0 {
		t.Fatalf("repository fallback decision=%#v calls=%v", d, source.calls)
	}
	item.Repository.HTMLURL = ""
	d = NewEvaluator(withEvidenceRulesDisabled(testConfig()), source).EvaluateForPreview(context.Background(), item)
	if d.URL != "unavailable" {
		t.Fatalf("terminal fallback URL=%q", d.URL)
	}
}

func TestEvaluatorSelectsOnlyNecessaryEvidence(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		item model.Notification
		want []string
	}{
		{"discussion uses complete history", testConfig(), thread("1", "github/repo", "Discussion", "subscribed"), []string{"subject", "discussion_comments"}},
		{"unsupported uses none", testConfig(), thread("1", "github/repo", "Unknown", "subscribed"), nil},
		{"conclusive reason uses none", testConfig(), thread("1", "github/repo", "Issue", "mention"), nil},
		{"disabled evidence rules use none", withEvidenceRulesDisabled(testConfig()), thread("1", "github/repo", "Issue", "subscribed"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &testEvidenceSource{}
			NewEvaluator(tt.cfg, source).Evaluate(context.Background(), tt.item)
			if !reflect.DeepEqual(source.calls, tt.want) {
				t.Fatalf("evidence calls=%v want=%v", source.calls, tt.want)
			}
		})
	}
}

func TestEvaluatorPassesContextToEvidenceSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &testEvidenceSource{checkContext: true}
	d := NewEvaluator(testConfig(), source).Evaluate(ctx, thread("1", "github/repo", "Issue", "subscribed"))
	// Decision intentionally retains evidence failures as reportable text.
	if !errors.Is(source.contextErr, context.Canceled) || d.Action != model.ActionKeep || d.EnrichmentError != context.Canceled.Error() {
		t.Fatalf("context err=%v decision=%#v", source.contextErr, d)
	}
}

type testEvidenceSource struct {
	subject      model.Resource
	comments     []model.Resource
	subjectErr   error
	commentsErr  error
	calls        []string
	checkContext bool
	contextErr   error
}

func (s *testEvidenceSource) FetchSubject(ctx context.Context, _ model.Notification) (model.Resource, error) {
	s.calls = append(s.calls, "subject")
	if s.checkContext {
		s.contextErr = ctx.Err()
		if s.contextErr != nil {
			return model.Resource{}, s.contextErr
		}
	}
	return s.subject, s.subjectErr
}

func (s *testEvidenceSource) FetchDiscussionComments(ctx context.Context, _ model.Notification) ([]model.Resource, error) {
	s.calls = append(s.calls, "discussion_comments")
	if s.checkContext {
		s.contextErr = ctx.Err()
		if s.contextErr != nil {
			return nil, s.contextErr
		}
	}
	return s.comments, s.commentsErr
}

func hasRule(d model.Decision, id string) bool {
	for _, r := range d.Rules {
		if r.ID == id {
			return true
		}
	}
	return false
}

func thread(id, repo, typ, reason string) model.Notification {
	return model.Notification{ID: id, Reason: reason, Repository: model.Repository{FullName: repo}, Subject: model.Subject{Type: typ, URL: "https://api.github.test/subject"}}
}

func testConfig() config.Config {
	on := true
	cfg := config.Config{User: "octocat", GitHubOrganization: "github", DiscussionTeamSlugs: []string{"github/notifications"}, Keep: config.Keep{ExternalOrganizationIssues: &on, PersonallyMentioned: &on, PersonallyAssigned: &on, IndividuallyReviewRequested: &on, AuthoredByUser: &on, TeamMentionedDiscussions: &on}}
	cfg.Hush.AllOtherNotifications = &on
	return cfg
}

func withEvidenceRulesDisabled(cfg config.Config) config.Config {
	off := false
	cfg.Keep.PersonallyAssigned = &off
	cfg.Keep.IndividuallyReviewRequested = &off
	cfg.Keep.AuthoredByUser = &off
	cfg.Keep.TeamMentionedDiscussions = &off
	return cfg
}
