package policy

import (
	"errors"
	"testing"

	"github.com/maxbeizer/gh-hush/internal/config"
	"github.com/maxbeizer/gh-hush/internal/model"
)

func TestClassifyPrecedence(t *testing.T) {
	cfg := testConfig()
	base := model.Notification{
		ID:     "1",
		Reason: "subscribed",
		Repository: model.Repository{
			FullName: "github/example",
			HTMLURL:  "https://github.com/github/example",
		},
		Subject: model.Subject{
			Title: "Example",
			Type:  "PullRequest",
			URL:   "https://api.github.com/repos/github/example/pulls/1",
		},
	}

	tests := []struct {
		name       string
		thread     model.Notification
		enrichment model.Enrichment
		wantAction model.Action
		wantRules  []string
	}{
		{
			name:       "external issue wins over catch all",
			thread:     withSubject(base, "other/example", "Issue", "subscribed"),
			wantAction: model.ActionKeep,
			wantRules:  []string{ruleExternalIssue},
		},
		{
			name:   "all matching keep rules retain precedence order",
			thread: withSubject(base, "other/example", "Issue", "mention"),
			enrichment: model.Enrichment{Subject: model.Resource{
				User:      model.User{Login: "octocat"},
				Assignees: []model.User{{Login: "octocat"}},
			}},
			wantAction: model.ActionKeep,
			wantRules:  []string{ruleExternalIssue, rulePersonalMention, rulePersonalAssign, ruleUserAuthored},
		},
		{
			name:   "individual reviewer kept",
			thread: withSubject(base, "github/example", "PullRequest", "review_requested"),
			enrichment: model.Enrichment{Subject: model.Resource{
				RequestedReviewers: []model.User{{Login: "octocat"}},
			}},
			wantAction: model.ActionKeep,
			wantRules:  []string{ruleIndividualReview},
		},
		{
			name:   "team reviewer alone unsubscribed",
			thread: withSubject(base, "github/example", "PullRequest", "review_requested"),
			enrichment: model.Enrichment{Subject: model.Resource{
				RequestedTeams: []model.Team{{Slug: "notifications"}},
			}},
			wantAction: model.ActionUnsubscribe,
			wantRules:  []string{ruleAllOther},
		},
		{
			name:   "discussion exact team mention kept",
			thread: withSubject(base, "github/example", "Discussion", "team_mention"),
			enrichment: model.Enrichment{LatestComment: model.Resource{
				Body: "Could @github/notifications take a look?",
			}},
			wantAction: model.ActionKeep,
			wantRules:  []string{ruleDiscussionTeam},
		},
		{
			name:   "partial team mention does not match",
			thread: withSubject(base, "github/example", "Discussion", "team_mention"),
			enrichment: model.Enrichment{Subject: model.Resource{
				Body: "Could @github/notifications-extra take a look?",
			}},
			wantAction: model.ActionUnsubscribe,
			wantRules:  []string{ruleAllOther},
		},
		{
			name:       "enrichment failure conservatively keeps ambiguous team review",
			thread:     withSubject(base, "github/example", "PullRequest", "review_requested"),
			enrichment: model.Enrichment{SubjectErr: errors.New("API unavailable")},
			wantAction: model.ActionKeep,
			wantRules:  []string{ruleSafetyFailure},
		},
		{
			name:       "known personal mention stays matched despite enrichment failure",
			thread:     withSubject(base, "github/example", "Issue", "mention"),
			enrichment: model.Enrichment{SubjectErr: errors.New("API unavailable")},
			wantAction: model.ActionKeep,
			wantRules:  []string{rulePersonalMention},
		},
		{
			name:       "irrelevant latest comment failure does not keep pull request",
			thread:     withSubject(base, "github/example", "PullRequest", "subscribed"),
			enrichment: model.Enrichment{LatestCommentErr: errors.New("API unavailable")},
			wantAction: model.ActionUnsubscribe,
			wantRules:  []string{ruleAllOther},
		},
		{
			name:       "everything else unsubscribed",
			thread:     base,
			wantAction: model.ActionUnsubscribe,
			wantRules:  []string{ruleAllOther},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(cfg, tt.thread, tt.enrichment)
			if got.Action != tt.wantAction {
				t.Fatalf("Classify() action = %q, want %q", got.Action, tt.wantAction)
			}
			if len(got.Rules) != len(tt.wantRules) {
				t.Fatalf("Classify() rules = %#v, want %v", got.Rules, tt.wantRules)
			}
			for index, wantRule := range tt.wantRules {
				if got.Rules[index].ID != wantRule {
					t.Errorf("Classify() rule[%d] = %q, want %q", index, got.Rules[index].ID, wantRule)
				}
			}
		})
	}
}

func TestEnrichmentRequirements(t *testing.T) {
	cfg := testConfig()
	base := model.Notification{
		Reason:  "subscribed",
		Subject: model.Subject{Type: "PullRequest"},
	}

	tests := []struct {
		name string
		cfg  config.Config
		item model.Notification
		want model.EnrichmentRequirements
	}{
		{
			name: "pull request needs subject but not latest comment",
			cfg:  cfg,
			item: base,
			want: model.EnrichmentRequirements{Subject: true},
		},
		{
			name: "issue needs subject but not latest comment",
			cfg:  cfg,
			item: withSubject(base, "github/example", "Issue", "subscribed"),
			want: model.EnrichmentRequirements{Subject: true},
		},
		{
			name: "discussion team rule needs subject and latest comment",
			cfg:  cfg,
			item: withSubject(base, "github/example", "Discussion", "subscribed"),
			want: model.EnrichmentRequirements{Subject: true, LatestComment: true},
		},
		{
			name: "discussion team rule with no teams needs no enrichment",
			cfg:  configWithOnlyEmptyDiscussionTeamRuleEnabled(cfg),
			item: withSubject(base, "github/example", "Discussion", "subscribed"),
			want: model.EnrichmentRequirements{},
		},
		{
			name: "disabled evidence rules need no enrichment",
			cfg:  configWithEvidenceRulesDisabled(cfg),
			item: base,
			want: model.EnrichmentRequirements{},
		},
		{
			name: "assignment reason avoids assignment-only enrichment",
			cfg:  configWithOnlyAssignmentEnabled(cfg),
			item: withSubject(base, "github/example", "Issue", "assign"),
			want: model.EnrichmentRequirements{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EnrichmentRequirements(tt.cfg, tt.item); got != tt.want {
				t.Fatalf("EnrichmentRequirements() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDisabledRuleFailureDoesNotSafetyKeep(t *testing.T) {
	cfg := configWithEvidenceRulesDisabled(testConfig())
	thread := model.Notification{Reason: "subscribed", Repository: model.Repository{FullName: "github/example"}, Subject: model.Subject{Type: "PullRequest"}}
	enrichment := model.Enrichment{SubjectErr: errors.New("irrelevant subject failure")}

	decision := Classify(cfg, thread, enrichment)
	if decision.Action != model.ActionUnsubscribe || decision.Rules[0].ID != ruleAllOther {
		t.Fatalf("Classify() = action %q rules %#v, want catch-all unsubscribe", decision.Action, decision.Rules)
	}
	if decision.EnrichmentError != "" {
		t.Fatalf("Classify() enrichment error = %q, want none", decision.EnrichmentError)
	}
}

func TestEmptyDiscussionTeamListFailureDoesNotSafetyKeep(t *testing.T) {
	cfg := configWithOnlyEmptyDiscussionTeamRuleEnabled(testConfig())
	thread := model.Notification{Reason: "team_mention", Repository: model.Repository{FullName: "github/example"}, Subject: model.Subject{Type: "Discussion"}}
	enrichment := model.Enrichment{
		SubjectErr:       errors.New("subject API unavailable"),
		LatestCommentErr: errors.New("comment API unavailable"),
	}

	decision := Classify(cfg, thread, enrichment)
	if decision.Action != model.ActionUnsubscribe || decision.Rules[0].ID != ruleAllOther {
		t.Fatalf("Classify() = action %q rules %#v, want catch-all unsubscribe", decision.Action, decision.Rules)
	}
	if decision.EnrichmentError != "" {
		t.Fatalf("Classify() enrichment error = %q, want none", decision.EnrichmentError)
	}
}

func configWithEvidenceRulesDisabled(cfg config.Config) config.Config {
	disabled := false
	cfg.Keep.PersonallyAssigned = &disabled
	cfg.Keep.IndividuallyReviewRequested = &disabled
	cfg.Keep.AuthoredByUser = &disabled
	cfg.Keep.TeamMentionedDiscussions = &disabled
	return cfg
}

func configWithOnlyAssignmentEnabled(cfg config.Config) config.Config {
	cfg = configWithEvidenceRulesDisabled(cfg)
	enabled := true
	cfg.Keep.PersonallyAssigned = &enabled
	return cfg
}

func configWithOnlyEmptyDiscussionTeamRuleEnabled(cfg config.Config) config.Config {
	cfg = configWithEvidenceRulesDisabled(cfg)
	enabled := true
	cfg.Keep.TeamMentionedDiscussions = &enabled
	cfg.DiscussionTeamSlugs = nil
	return cfg
}

func testConfig() config.Config {
	enabled := true
	cfg := config.Config{
		User:                "octocat",
		GitHubOrganization:  "github",
		RunMode:             "ad_hoc",
		DiscussionTeamSlugs: []string{"github/notifications"},
		Keep: config.Keep{
			ExternalOrganizationIssues:  &enabled,
			PersonallyMentioned:         &enabled,
			PersonallyAssigned:          &enabled,
			IndividuallyReviewRequested: &enabled,
			AuthoredByUser:              &enabled,
			TeamMentionedDiscussions:    &enabled,
		},
	}
	return cfg
}

func withSubject(base model.Notification, repository, subjectType, reason string) model.Notification {
	base.Repository.FullName = repository
	base.Subject.Type = subjectType
	base.Reason = reason
	return base
}
