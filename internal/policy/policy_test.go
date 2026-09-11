package policy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/maxbeizer/gh-hush/internal/config"
	"github.com/maxbeizer/gh-hush/internal/model"
)

// Rule names come from the shipped recommended policy, which the tests parse
// directly so evaluation is exercised against the configuration users get.
const (
	ruleOutsideOrganization = "keep work outside my organization"
	rulePersonalMention     = "keep personal mentions"
	ruleAssignedToMe        = "keep work assigned to me"
	ruleReviewRequested     = "keep review requests for me"
	ruleAuthored            = "keep work I authored"
	ruleActiveTeamReview    = "keep my team's active reviews"
	ruleDiscussionTeam      = "keep team-mentioned discussions"
)

func TestSafetyKeepsUnsupportedSubjectTypesWithoutEvidence(t *testing.T) {
	for _, subjectType := range []string{"", "SecurityAlert", "RepositoryInvitation", "RepositoryVulnerabilityAlert", "UnknownFutureType"} {
		t.Run("unsupported "+subjectType, func(t *testing.T) {
			source := &testEvidenceSource{}
			d := NewEvaluator(testConfig(t), source).Evaluate(context.Background(), thread("1", "github/repo", subjectType, "subscribed"))
			if d.Action != model.ActionKeep || d.Rules[0].ID != ruleSafetyUnsupported || len(source.calls) != 0 {
				t.Fatalf("decision = %#v, evidence calls = %v", d, source.calls)
			}
		})
	}
}

func TestDefaultActionAppliesWhenNoRuleMatches(t *testing.T) {
	for _, subjectType := range []string{"Issue", "PullRequest", "Discussion", "Commit", "Release", "CheckSuite"} {
		t.Run(subjectType, func(t *testing.T) {
			source := &testEvidenceSource{}
			d := NewEvaluator(noEvidenceConfig(t), source).Evaluate(context.Background(), thread("1", "github/repo", subjectType, "subscribed"))
			if d.Action != model.ActionUnsubscribeAndMarkDone || d.Rules[0].ID != ruleDefault || len(source.calls) != 0 {
				t.Fatalf("decision = %#v, evidence calls = %v", d, source.calls)
			}
		})
	}
}

func TestOutsideOrganizationRuleNeedsNoEvidence(t *testing.T) {
	for _, subjectType := range []string{"Issue", "PullRequest", "Discussion", "Commit", "Release", "CheckSuite"} {
		t.Run(subjectType, func(t *testing.T) {
			source := &testEvidenceSource{}
			d := NewEvaluator(testConfig(t), source).Evaluate(context.Background(), thread("1", "other/repo", subjectType, "subscribed"))
			if d.Action != model.ActionKeep || d.Rules[0].ID != ruleOutsideOrganization || len(source.calls) != 0 {
				t.Fatalf("decision = %#v, evidence calls = %v", d, source.calls)
			}
		})
	}
}

func TestKeepRules(t *testing.T) {
	tests := []struct {
		name     string
		item     model.Notification
		subject  model.Resource
		comments []model.Resource
		rule     string
	}{
		{"mention", thread("1", "github/repo", "Issue", "mention"), model.Resource{}, nil, rulePersonalMention},
		{"assignment reason", thread("1", "github/repo", "Issue", "assign"), model.Resource{}, nil, ruleAssignedToMe},
		{"current assignee", thread("1", "github/repo", "Issue", "subscribed"), model.Resource{Assignees: []model.User{{Login: "octocat"}}}, nil, ruleAssignedToMe},
		{"individual review", thread("1", "github/repo", "PullRequest", "review_requested"), model.Resource{State: "closed", RequestedReviewers: []model.User{{Login: "octocat"}}}, nil, ruleReviewRequested},
		{"author reason", thread("1", "github/repo", "Release", "author"), model.Resource{}, nil, ruleAuthored},
		{"author response", thread("1", "github/repo", "Commit", "subscribed"), model.Resource{Author: model.User{Login: "octocat"}}, nil, ruleAuthored},
		{"active team review", thread("1", "github/repo", "PullRequest", "team_mention"), model.Resource{State: "open", RequestedTeams: []model.Team{{Slug: "notifications"}}}, nil, ruleActiveTeamReview},
		{"historical discussion team mention", thread("1", "github/repo", "Discussion", "team_mention"), model.Resource{}, []model.Resource{{Body: "old @github/notifications mention"}, {Body: "new comment"}}, ruleDiscussionTeam},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewEvaluator(testConfig(t), &testEvidenceSource{subject: tt.subject, comments: tt.comments}).Evaluate(context.Background(), tt.item)
			if d.Action != model.ActionKeep || d.Rules[0].ID != tt.rule {
				t.Fatalf("decision=%#v", d)
			}
			if d.Rules[0].Evidence == "" {
				t.Fatalf("matched rule did not explain itself: %#v", d.Rules)
			}
		})
	}
}

func TestFirstMatchingRuleWins(t *testing.T) {
	cfg := parseConfig(t, `version: 3
identity:
  user: octocat
  organization: github
  teams: []
defaults:
  action: keep
  on_missing_evidence: keep
rules:
  - name: hush noisy repository
    action: hush
    when:
      repository: github/noisy
  - name: keep personal mentions
    action: keep
    when:
      reason: [mention]
`)
	d := NewEvaluator(cfg, &testEvidenceSource{}).Evaluate(context.Background(), thread("1", "github/noisy", "Issue", "mention"))
	if d.Action != model.ActionUnsubscribeAndMarkDone || d.Rules[0].ID != "hush noisy repository" {
		t.Fatalf("earlier rule did not win: %#v", d)
	}
	d = NewEvaluator(cfg, &testEvidenceSource{}).Evaluate(context.Background(), thread("1", "github/quiet", "Issue", "mention"))
	if d.Action != model.ActionKeep || d.Rules[0].ID != rulePersonalMention {
		t.Fatalf("later rule did not match: %#v", d)
	}
	d = NewEvaluator(cfg, &testEvidenceSource{}).Evaluate(context.Background(), thread("1", "github/quiet", "Issue", "subscribed"))
	if d.Action != model.ActionKeep || d.Rules[0].ID != ruleDefault {
		t.Fatalf("terminal default not applied: %#v", d)
	}
}

func TestWatchedRepositoryRulesKeepConfiguredSubjects(t *testing.T) {
	cfg := parseConfig(t, `version: 3
identity:
  user: octocat
  organization: github
  teams: []
defaults:
  action: hush
  on_missing_evidence: keep
rules:
  - name: watch everything in one repository
    action: keep
    when:
      repository:
        any_of: [github/allofit]
  - name: watch active work
    action: keep
    when:
      all:
        - repository:
            any_of: [github/watched, github/dependency-*]
        - any:
            - all: [{subject_type: [PullRequest]}, {state: open}]
            - all: [{subject_type: [Issue]}, {state: open}]
            - all: [{subject_type: [Discussion]}, {state_not: closed}]
`)
	tests := []struct {
		name    string
		item    model.Notification
		subject model.Resource
		keep    bool
	}{
		{"all notifications", thread("1", "github/allofit", "Release", "subscribed"), model.Resource{}, true},
		{"open pull request", thread("1", "github/watched", "PullRequest", "subscribed"), model.Resource{State: "open"}, true},
		{"glob match", thread("1", "github/dependency-graph-api", "Issue", "subscribed"), model.Resource{State: "open"}, true},
		{"closed pull request", thread("1", "github/watched", "PullRequest", "subscribed"), model.Resource{State: "closed"}, false},
		{"open locked discussion", thread("1", "github/watched", "Discussion", "subscribed"), model.Resource{State: "locked"}, true},
		{"closed locked discussion", thread("1", "github/watched", "Discussion", "subscribed"), model.Resource{State: "locked", StateReason: stringPointer("resolved")}, false},
		{"unsupported subject type in watched repository", thread("1", "github/watched", "Release", "subscribed"), model.Resource{}, false},
		{"unwatched repository", thread("1", "github/other", "Issue", "subscribed"), model.Resource{State: "open"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewEvaluator(cfg, &testEvidenceSource{subject: tt.subject}).Evaluate(context.Background(), tt.item)
			if (d.Action == model.ActionKeep) != tt.keep {
				t.Fatalf("decision = %#v, want keep = %v", d, tt.keep)
			}
		})
	}
}

func TestWatchedRepositoryAllNotificationsNeedsNoEvidence(t *testing.T) {
	cfg := parseConfig(t, `version: 3
identity:
  user: octocat
  organization: github
  teams: []
defaults:
  action: hush
  on_missing_evidence: keep
rules:
  - name: watch everything
    action: keep
    when:
      repository: github/watched
`)
	source := &testEvidenceSource{}
	d := NewEvaluator(cfg, source).Evaluate(context.Background(), thread("1", "github/watched", "PullRequest", "subscribed"))
	if d.Action != model.ActionKeep || d.Rules[0].ID != "watch everything" || len(source.calls) != 0 {
		t.Fatalf("decision = %#v, evidence calls = %v", d, source.calls)
	}
}

func TestTeamReviewRequestsOnlyProtectMatchingOpenPullRequests(t *testing.T) {
	tests := []struct {
		name    string
		item    model.Notification
		subject model.Resource
	}{
		{"closed pull request", thread("1", "github/repo", "PullRequest", "team_mention"), model.Resource{State: "closed", RequestedTeams: []model.Team{{Slug: "notifications"}}}},
		{"unconfigured team", thread("1", "github/repo", "PullRequest", "team_mention"), model.Resource{State: "open", RequestedTeams: []model.Team{{Slug: "other"}}}},
		{"non-pull-request subject", thread("1", "github/repo", "Issue", "subscribed"), model.Resource{State: "open", RequestedTeams: []model.Team{{Slug: "notifications"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewEvaluator(testConfig(t), &testEvidenceSource{subject: tt.subject}).Evaluate(context.Background(), tt.item)
			if d.Action != model.ActionUnsubscribeAndMarkDone || d.Rules[0].ID != ruleDefault {
				t.Fatalf("decision=%#v", d)
			}
		})
	}
}

func TestTeamReviewRequestDoesNotMatchSameSlugInAnotherOrganization(t *testing.T) {
	cfg := parseConfig(t, `version: 3
identity:
  user: octocat
  organization: github
  teams: [github/notifications]
defaults:
  action: hush
  on_missing_evidence: keep
rules:
  - name: keep my team's active reviews
    action: keep
    when:
      all:
        - subject_type: [PullRequest]
        - state: open
        - review_requested_team: my_teams
`)
	d := NewEvaluator(cfg, &testEvidenceSource{subject: model.Resource{State: "open", RequestedTeams: []model.Team{{Slug: "notifications"}}}}).Evaluate(context.Background(), thread("1", "other/repo", "PullRequest", "team_mention"))
	if d.Action != model.ActionUnsubscribeAndMarkDone || d.Rules[0].ID != ruleDefault {
		t.Fatalf("decision=%#v", d)
	}
}

func TestExactTeamMentionIsRequired(t *testing.T) {
	item := thread("1", "github/repo", "Discussion", "team_mention")
	d := NewEvaluator(testConfig(t), &testEvidenceSource{comments: []model.Resource{{Body: "@github/notifications-extra"}}}).Evaluate(context.Background(), item)
	if d.Action != model.ActionUnsubscribeAndMarkDone {
		t.Fatalf("partial mention decision=%#v", d)
	}
}

func TestMissingEvidencePostureKeepStopsEvaluation(t *testing.T) {
	tests := []struct {
		name   string
		item   model.Notification
		source *testEvidenceSource
	}{
		{"unavailable pull request state", thread("1", "github/repo", "PullRequest", "team_mention"),
			&testEvidenceSource{subject: model.Resource{RequestedTeams: []model.Team{{Slug: "notifications"}}}}},
		{"unavailable subject", thread("1", "github/repo", "Issue", "subscribed"),
			&testEvidenceSource{subjectErr: errors.New("subject decode incomplete")}},
		{"unavailable discussion comments", thread("1", "github/repo", "Discussion", "subscribed"),
			&testEvidenceSource{subject: model.Resource{Body: "available"}, commentsErr: errors.New("pages unavailable")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewEvaluator(testConfig(t), tt.source).Evaluate(context.Background(), tt.item)
			if d.Action != model.ActionKeep || d.Rules[0].ID != ruleSafetyFailure || d.EnrichmentError == "" {
				t.Fatalf("decision=%#v", d)
			}
		})
	}
}

func TestMissingEvidencePostureHushContinuesAndReportsTheFailure(t *testing.T) {
	cfg := parseConfig(t, `version: 3
identity:
  user: octocat
  organization: github
  teams: []
defaults:
  action: hush
  on_missing_evidence: hush
rules:
  - name: keep open pull requests
    action: keep
    when:
      all:
        - subject_type: [PullRequest]
        - state: open
  - name: keep personal mentions
    action: keep
    when:
      reason: [mention]
`)
	source := &testEvidenceSource{subjectErr: errors.New("subject unavailable")}
	d := NewEvaluator(cfg, source).Evaluate(context.Background(), thread("1", "github/repo", "PullRequest", "mention"))
	if d.Action != model.ActionKeep || d.Rules[0].ID != rulePersonalMention || d.EnrichmentError != "subject unavailable" {
		t.Fatalf("decision=%#v", d)
	}

	d = NewEvaluator(cfg, source).Evaluate(context.Background(), thread("1", "github/repo", "PullRequest", "subscribed"))
	if d.Action != model.ActionUnsubscribeAndMarkDone || d.Rules[0].ID != ruleDefault || d.EnrichmentError != "subject unavailable" {
		t.Fatalf("decision=%#v", d)
	}
}

func TestRequiredSubjectEvidenceWithEmptyURLIsConservativelyKept(t *testing.T) {
	item := thread("1", "github/repo", "Issue", "subscribed")
	item.Subject.URL = ""
	source := &testEvidenceSource{subjectErr: errors.New("notification subject did not include an API URL")}

	d := NewEvaluator(testConfig(t), source).Evaluate(context.Background(), item)
	if d.Action != model.ActionKeep || d.EnrichmentError == "" || d.Rules[0].ID != ruleSafetyFailure || !reflect.DeepEqual(source.calls, []string{"subject"}) {
		t.Fatalf("decision=%#v calls=%v", d, source.calls)
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

	d := NewEvaluator(testConfig(t), source).EvaluateForPreview(context.Background(), item)
	if d.Action != model.ActionKeep || d.URL != source.subject.HTMLURL || !reflect.DeepEqual(source.calls, []string{"subject"}) {
		t.Fatalf("decision=%#v calls=%v", d, source.calls)
	}
	if len(d.Rules) != 1 || d.Rules[0].ID != rulePersonalMention {
		t.Fatalf("display-only fields changed classification rules: %#v", d.Rules)
	}

	// A partial resource remains useful for display even when its display-only
	// request fails, but the failure is not policy enrichment failure.
	source = &testEvidenceSource{subject: model.Resource{HTMLURL: source.subject.HTMLURL}, subjectErr: errors.New("display unavailable")}
	d = NewEvaluator(testConfig(t), source).EvaluateForPreview(context.Background(), item)
	if d.Action != model.ActionKeep || d.EnrichmentError != "" || d.URL != source.subject.HTMLURL || d.Rules[0].ID != rulePersonalMention {
		t.Fatalf("display failure decision=%#v", d)
	}
}

func TestEvidenceFailureStillReportsAvailableSubjectURL(t *testing.T) {
	source := &testEvidenceSource{
		subject:     model.Resource{Body: "available", HTMLURL: "https://github.test/discussion/1"},
		commentsErr: errors.New("pages unavailable"),
	}
	d := NewEvaluator(testConfig(t), source).Evaluate(context.Background(), thread("1", "github/repo", "Discussion", "subscribed"))
	if d.Action != model.ActionKeep || d.Rules[0].ID != ruleSafetyFailure || d.EnrichmentError != "pages unavailable" {
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
			d := NewEvaluator(testConfig(t), source).EvaluateForPreview(context.Background(), item)
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
			d := NewEvaluator(testConfig(t), &testEvidenceSource{subject: model.Resource{HTMLURL: tt.htmlURL}}).EvaluateForPreview(context.Background(), item)
			if d.URL != tt.want || d.URL == item.Subject.URL {
				t.Fatalf("URL=%q want=%q", d.URL, tt.want)
			}
		})
	}

	item.Subject.URL = ""
	source := &testEvidenceSource{}
	d := NewEvaluator(noEvidenceConfig(t), source).EvaluateForPreview(context.Background(), item)
	if d.URL != "https://github.test/github/repo/" || len(source.calls) != 0 {
		t.Fatalf("repository fallback decision=%#v calls=%v", d, source.calls)
	}
	item.Repository.HTMLURL = ""
	d = NewEvaluator(noEvidenceConfig(t), source).EvaluateForPreview(context.Background(), item)
	if d.URL != "https://github.com/github/repo" {
		t.Fatalf("derived repository fallback URL=%q", d.URL)
	}
	item.Repository.FullName = "invalid/repo/extra"
	d = NewEvaluator(noEvidenceConfig(t), source).EvaluateForPreview(context.Background(), item)
	if d.URL != "unavailable" {
		t.Fatalf("terminal fallback URL=%q", d.URL)
	}
}

// Evidence planning is a consequence of evaluation: a predicate fetches only
// what it inspects, and each resource is fetched at most once per notification.
func TestEvaluatorSelectsOnlyNecessaryEvidence(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		item model.Notification
		want []string
	}{
		{"discussion uses complete history", testConfig(t), thread("1", "github/repo", "Discussion", "subscribed"), []string{"subject", "discussion_comments"}},
		{"issue fetches the subject once", testConfig(t), thread("1", "github/repo", "Issue", "subscribed"), []string{"subject"}},
		{"unsupported uses none", testConfig(t), thread("1", "github/repo", "Unknown", "subscribed"), nil},
		{"conclusive reason uses none", testConfig(t), thread("1", "github/repo", "Issue", "mention"), nil},
		{"outside the organization uses none", testConfig(t), thread("1", "other/repo", "Issue", "subscribed"), nil},
		{"rules without evidence use none", noEvidenceConfig(t), thread("1", "github/repo", "Issue", "subscribed"), nil},
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
	d := NewEvaluator(testConfig(t), source).Evaluate(ctx, thread("1", "github/repo", "Issue", "subscribed"))
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

func stringPointer(value string) *string { return &value }

func thread(id, repo, typ, reason string) model.Notification {
	return model.Notification{ID: id, Reason: reason, Repository: model.Repository{FullName: repo}, Subject: model.Subject{Type: typ, URL: "https://api.github.test/subject"}}
}

// testConfig is the recommended policy gh hush init-config writes, so policy
// tests and the shipped starter configuration cannot drift apart.
func testConfig(t *testing.T) config.Config {
	t.Helper()
	return parseConfig(t, string(config.RecommendedConfigYAML("octocat", "github", []string{"github/notifications"})))
}

// noEvidenceConfig contains only rules that classify from the notification
// itself, so any GitHub fetch during evaluation is a planning defect.
func noEvidenceConfig(t *testing.T) config.Config {
	t.Helper()
	return parseConfig(t, `version: 3
identity:
  user: octocat
  organization: github
  teams: []
defaults:
  action: hush
  on_missing_evidence: keep
rules:
  - name: keep work outside my organization
    action: keep
    when:
      repository:
        owner_not: github
`)
}

func parseConfig(t *testing.T, document string) config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(document))
	if err != nil {
		t.Fatalf("test configuration is invalid: %v\n%s", err, strings.TrimSpace(document))
	}
	return cfg
}
