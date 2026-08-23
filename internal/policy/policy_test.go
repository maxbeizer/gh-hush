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

func TestDecisionURLPreservesEvidenceAndNotificationFallbackOrder(t *testing.T) {
	item := thread("1", "github/repo", "Issue", "subscribed")
	item.Repository.HTMLURL = "https://github.test/github/repo"

	d := NewEvaluator(testConfig(), &testEvidenceSource{subject: model.Resource{HTMLURL: "https://github.test/github/repo/issues/1"}}).Evaluate(context.Background(), item)
	if d.URL != "https://github.test/github/repo/issues/1" {
		t.Fatalf("evidence URL=%q", d.URL)
	}

	d = NewEvaluator(testConfig(), &testEvidenceSource{subjectErr: errors.New("subject unavailable")}).Evaluate(context.Background(), item)
	if d.URL != item.Subject.URL {
		t.Fatalf("subject API fallback URL=%q want=%q", d.URL, item.Subject.URL)
	}

	item.Subject.URL = ""
	d = NewEvaluator(withEvidenceRulesDisabled(testConfig()), &testEvidenceSource{}).Evaluate(context.Background(), item)
	if d.URL != item.Repository.HTMLURL+"/notifications" {
		t.Fatalf("repository fallback URL=%q", d.URL)
	}

	item.Repository.HTMLURL = ""
	d = NewEvaluator(withEvidenceRulesDisabled(testConfig()), &testEvidenceSource{}).Evaluate(context.Background(), item)
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
		{"evidence rules disabled uses none", withEvidenceRulesDisabled(testConfig()), thread("1", "github/repo", "Issue", "subscribed"), nil},
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
