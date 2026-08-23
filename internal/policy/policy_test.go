package policy

import (
	"errors"
	"testing"

	"github.com/maxbeizer/gh-hush/internal/config"
	"github.com/maxbeizer/gh-hush/internal/model"
)

func TestExternalOrganizationProtectionAppliesToEveryType(t *testing.T) {
	for _, subjectType := range []string{"Issue", "PullRequest", "Discussion", "Commit", "Release", "CheckSuite", "RepositoryVulnerabilityAlert"} {
		t.Run(subjectType, func(t *testing.T) {
			d := Classify(testConfig(), thread("1", "other/repo", subjectType, "subscribed"), model.Enrichment{})
			if d.Action != model.ActionKeep || d.Rules[0].ID != ruleExternalOrganization {
				t.Fatalf("decision = %#v", d)
			}
		})
	}
}

func TestExplicitHushAllowlistAndUnsupportedSafetyKeep(t *testing.T) {
	for _, subjectType := range []string{"Issue", "PullRequest", "Discussion", "Commit", "Release", "CheckSuite"} {
		t.Run(subjectType, func(t *testing.T) {
			d := Classify(withEvidenceRulesDisabled(testConfig()), thread("1", "github/repo", subjectType, "subscribed"), model.Enrichment{})
			if d.Action != model.ActionUnsubscribeAndMarkDone || d.Rules[0].ID != ruleAllOther {
				t.Fatalf("decision = %#v", d)
			}
		})
	}
	for _, subjectType := range []string{"", "SecurityAlert", "RepositoryInvitation", "UnknownFutureType"} {
		t.Run("unsupported "+subjectType, func(t *testing.T) {
			d := Classify(testConfig(), thread("1", "github/repo", subjectType, "subscribed"), model.Enrichment{})
			if d.Action != model.ActionKeep || d.Rules[len(d.Rules)-1].ID != ruleSafetyUnsupported {
				t.Fatalf("decision = %#v", d)
			}
		})
	}
}

func TestKeepRules(t *testing.T) {
	cfg := testConfig()
	tests := []struct {
		name     string
		item     model.Notification
		evidence model.Enrichment
		rule     string
	}{
		{"mention", thread("1", "github/repo", "Issue", "mention"), model.Enrichment{}, rulePersonalMention},
		{"assignment reason", thread("1", "github/repo", "Issue", "assign"), model.Enrichment{}, rulePersonalAssign},
		{"current assignee", thread("1", "github/repo", "Issue", "subscribed"), model.Enrichment{Subject: model.Resource{Assignees: []model.User{{Login: "octocat"}}}}, rulePersonalAssign},
		{"individual review", thread("1", "github/repo", "PullRequest", "review_requested"), model.Enrichment{Subject: model.Resource{RequestedReviewers: []model.User{{Login: "octocat"}}}}, ruleIndividualReview},
		{"author reason", thread("1", "github/repo", "Release", "author"), model.Enrichment{}, ruleUserAuthored},
		{"author response", thread("1", "github/repo", "Commit", "subscribed"), model.Enrichment{Subject: model.Resource{Author: model.User{Login: "octocat"}}}, ruleUserAuthored},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Classify(cfg, tt.item, tt.evidence)
			if d.Action != model.ActionKeep || !hasRule(d, tt.rule) {
				t.Fatalf("decision=%#v", d)
			}
		})
	}
}

func TestDiscussionHistoricalTeamMentionProtectsAndExactMatchIsRequired(t *testing.T) {
	item := thread("1", "github/repo", "Discussion", "team_mention")
	d := Classify(testConfig(), item, model.Enrichment{DiscussionComments: []model.Resource{{Body: "old @github/notifications mention"}, {Body: "new comment"}}})
	if d.Action != model.ActionKeep || !hasRule(d, ruleDiscussionTeam) {
		t.Fatalf("decision=%#v", d)
	}
	d = Classify(testConfig(), item, model.Enrichment{DiscussionComments: []model.Resource{{Body: "@github/notifications-extra"}}})
	if d.Action != model.ActionUnsubscribeAndMarkDone {
		t.Fatalf("partial mention decision=%#v", d)
	}
}

func TestRequiredEnrichmentFailureSafetyKeeps(t *testing.T) {
	item := thread("1", "github/repo", "Discussion", "subscribed")
	d := Classify(testConfig(), item, model.Enrichment{DiscussionCommentsErr: errors.New("pages unavailable")})
	if d.Action != model.ActionKeep || !hasRule(d, ruleSafetyFailure) || d.EnrichmentError == "" {
		t.Fatalf("decision=%#v", d)
	}
}

func TestEnrichmentRequirementsUseCompleteDiscussionHistory(t *testing.T) {
	r := EnrichmentRequirements(testConfig(), thread("1", "github/repo", "Discussion", "subscribed"))
	if !r.Subject || !r.DiscussionComments {
		t.Fatalf("requirements=%+v", r)
	}
	r = EnrichmentRequirements(testConfig(), thread("1", "github/repo", "Unknown", "subscribed"))
	if r != (model.EnrichmentRequirements{}) {
		t.Fatalf("unsupported requirements=%+v", r)
	}
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
