package policy

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/maxbeizer/gh-hush/internal/config"
	"github.com/maxbeizer/gh-hush/internal/model"
)

const (
	ruleExternalIssue    = "keep.external_organization_issue"
	rulePersonalMention  = "keep.personal_mention"
	rulePersonalAssign   = "keep.personal_assignment"
	ruleIndividualReview = "keep.individual_review_request"
	ruleUserAuthored     = "keep.user_authored_work"
	ruleDiscussionTeam   = "keep.discussion_team_mention"
	ruleSafetyFailure    = "safety.keep_on_enrichment_failure"
	ruleAllOther         = "unsubscribe.all_other_notifications"
)

// EnrichmentRequirements returns the evidence fields needed to evaluate the
// enabled rules for a notification. Notification reasons that conclusively
// match a rule do not require the same rule's subject evidence.
func EnrichmentRequirements(cfg config.Config, thread model.Notification) model.EnrichmentRequirements {
	subjectType := thread.Subject.Type
	requirements := model.EnrichmentRequirements{}

	if config.Enabled(cfg.Keep.PersonallyAssigned) && thread.Reason != "assign" &&
		(subjectType == "Issue" || subjectType == "PullRequest") {
		requirements.Subject = true
	}
	if config.Enabled(cfg.Keep.IndividuallyReviewRequested) && subjectType == "PullRequest" {
		requirements.Subject = true
	}
	if config.Enabled(cfg.Keep.AuthoredByUser) && thread.Reason != "author" &&
		(subjectType == "Issue" || subjectType == "PullRequest" || subjectType == "Discussion") {
		requirements.Subject = true
	}
	if config.Enabled(cfg.Keep.TeamMentionedDiscussions) && len(cfg.DiscussionTeamSlugs) > 0 &&
		subjectType == "Discussion" {
		requirements.Subject = true
		requirements.LatestComment = true
	}

	return requirements
}

// Classify applies ordered, deterministic policy rules to a notification.
func Classify(cfg config.Config, thread model.Notification, enrichment model.Enrichment) model.Decision {
	decision := model.Decision{
		Thread: thread,
		URL:    notificationURL(thread, enrichment),
	}

	repositoryOrg := strings.SplitN(thread.Repository.FullName, "/", 2)[0]
	if config.Enabled(cfg.Keep.ExternalOrganizationIssues) &&
		thread.Subject.Type == "Issue" &&
		!strings.EqualFold(repositoryOrg, cfg.GitHubOrganization) {
		decision.Rules = append(decision.Rules, model.Rule{
			ID:       ruleExternalIssue,
			Evidence: fmt.Sprintf("issue repository organization %q differs from configured organization %q", repositoryOrg, cfg.GitHubOrganization),
		})
	}

	if config.Enabled(cfg.Keep.PersonallyMentioned) && thread.Reason == "mention" {
		decision.Rules = append(decision.Rules, model.Rule{
			ID:       rulePersonalMention,
			Evidence: `GitHub notification reason is "mention" (team mentions use "team_mention")`,
		})
	}

	if config.Enabled(cfg.Keep.PersonallyAssigned) &&
		(thread.Reason == "assign" || containsUser(enrichment.Subject.Assignees, cfg.User)) {
		decision.Rules = append(decision.Rules, model.Rule{
			ID:       rulePersonalAssign,
			Evidence: fmt.Sprintf("%q is personally assigned", cfg.User),
		})
	}

	if config.Enabled(cfg.Keep.IndividuallyReviewRequested) &&
		containsUser(enrichment.Subject.RequestedReviewers, cfg.User) {
		decision.Rules = append(decision.Rules, model.Rule{
			ID:       ruleIndividualReview,
			Evidence: fmt.Sprintf("%q appears in requested_reviewers; team requests alone do not match", cfg.User),
		})
	}

	if config.Enabled(cfg.Keep.AuthoredByUser) &&
		(thread.Reason == "author" || strings.EqualFold(enrichment.Subject.User.Login, cfg.User)) {
		decision.Rules = append(decision.Rules, model.Rule{
			ID:       ruleUserAuthored,
			Evidence: fmt.Sprintf("%q authored the notification subject", cfg.User),
		})
	}

	if config.Enabled(cfg.Keep.TeamMentionedDiscussions) && thread.Subject.Type == "Discussion" {
		for _, team := range matchingTeamMentions(cfg.DiscussionTeamSlugs, enrichment.Subject.Body, enrichment.LatestComment.Body) {
			decision.Rules = append(decision.Rules, model.Rule{
				ID:       ruleDiscussionTeam,
				Evidence: fmt.Sprintf("discussion subject or latest comment contains exact team mention @%s", team),
			})
		}
	}

	requirements := EnrichmentRequirements(cfg, thread)
	var enrichmentErrors []error
	if requirements.Subject && enrichment.SubjectErr != nil {
		enrichmentErrors = append(enrichmentErrors, enrichment.SubjectErr)
	}
	if requirements.LatestComment && enrichment.LatestCommentErr != nil {
		enrichmentErrors = append(enrichmentErrors, enrichment.LatestCommentErr)
	}
	if enrichmentErr := errors.Join(enrichmentErrors...); enrichmentErr != nil {
		decision.EnrichmentError = enrichmentErr.Error()
		if len(decision.Rules) == 0 {
			decision.Rules = append(decision.Rules, model.Rule{
				ID:       ruleSafetyFailure,
				Evidence: fmt.Sprintf("required evidence was unavailable: %v", enrichmentErr),
			})
		}
	}

	if len(decision.Rules) > 0 {
		decision.Action = model.ActionKeep
		return decision
	}

	decision.Action = model.ActionUnsubscribe
	decision.Rules = []model.Rule{{
		ID:       ruleAllOther,
		Evidence: "no enabled keep rule matched after successful evaluation",
	}}
	return decision
}

func containsUser(users []model.User, login string) bool {
	for _, user := range users {
		if strings.EqualFold(user.Login, login) {
			return true
		}
	}
	return false
}

func matchingTeamMentions(teams []string, bodies ...string) []string {
	var matches []string
	for _, team := range teams {
		pattern := regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_.-])@` + regexp.QuoteMeta(team) + `([^A-Za-z0-9_.-]|$)`)
		for _, body := range bodies {
			if pattern.MatchString(body) {
				matches = append(matches, team)
				break
			}
		}
	}
	return matches
}

func notificationURL(thread model.Notification, enrichment model.Enrichment) string {
	if enrichment.Subject.HTMLURL != "" {
		return enrichment.Subject.HTMLURL
	}
	if thread.Subject.URL != "" {
		return thread.Subject.URL
	}
	if thread.Repository.HTMLURL != "" {
		return thread.Repository.HTMLURL + "/notifications"
	}
	return "unavailable"
}
