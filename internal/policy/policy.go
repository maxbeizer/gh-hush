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
	ruleExternalOrganization = "keep.external_organization"
	rulePersonalMention      = "keep.personal_mention"
	rulePersonalAssign       = "keep.personal_assignment"
	ruleIndividualReview     = "keep.individual_review_request"
	ruleUserAuthored         = "keep.user_authored_work"
	ruleDiscussionTeam       = "keep.discussion_team_mention"
	ruleSafetyFailure        = "safety.keep_on_enrichment_failure"
	ruleSafetyUnsupported    = "safety.keep_unsupported_subject_type"
	ruleAllOther             = "hush.all_other_notifications"
)

var hushableSubjectTypes = map[string]struct{}{
	"Issue": {}, "PullRequest": {}, "Discussion": {}, "Commit": {}, "Release": {}, "CheckSuite": {},
}

func IsHushableSubjectType(subjectType string) bool {
	_, ok := hushableSubjectTypes[subjectType]
	return ok
}

func EnrichmentRequirements(cfg config.Config, thread model.Notification) model.EnrichmentRequirements {
	if !IsHushableSubjectType(thread.Subject.Type) {
		return model.EnrichmentRequirements{}
	}
	repositoryOrg := strings.SplitN(thread.Repository.FullName, "/", 2)[0]
	if (config.Enabled(cfg.Keep.ExternalOrganizationIssues) && !strings.EqualFold(repositoryOrg, cfg.GitHubOrganization)) ||
		(config.Enabled(cfg.Keep.PersonallyMentioned) && thread.Reason == "mention") ||
		(config.Enabled(cfg.Keep.PersonallyAssigned) && thread.Reason == "assign") ||
		(config.Enabled(cfg.Keep.AuthoredByUser) && thread.Reason == "author") {
		// The action is already conclusively Keep; no API evidence is required.
		return model.EnrichmentRequirements{}
	}
	r := model.EnrichmentRequirements{}
	t := thread.Subject.Type
	if config.Enabled(cfg.Keep.PersonallyAssigned) && thread.Reason != "assign" && (t == "Issue" || t == "PullRequest") {
		r.Subject = true
	}
	if config.Enabled(cfg.Keep.IndividuallyReviewRequested) && t == "PullRequest" {
		r.Subject = true
	}
	if config.Enabled(cfg.Keep.AuthoredByUser) && thread.Reason != "author" {
		r.Subject = true
	}
	if config.Enabled(cfg.Keep.TeamMentionedDiscussions) && len(cfg.DiscussionTeamSlugs) > 0 && t == "Discussion" {
		r.Subject = true
		r.DiscussionComments = true
	}
	return r
}

func Classify(cfg config.Config, thread model.Notification, enrichment model.Enrichment) model.Decision {
	decision := model.Decision{Thread: thread, URL: notificationURL(thread, enrichment)}
	repositoryOrg := strings.SplitN(thread.Repository.FullName, "/", 2)[0]
	if config.Enabled(cfg.Keep.ExternalOrganizationIssues) && !strings.EqualFold(repositoryOrg, cfg.GitHubOrganization) {
		decision.Rules = append(decision.Rules, model.Rule{ID: ruleExternalOrganization, Evidence: fmt.Sprintf("repository organization %q differs from configured organization %q", repositoryOrg, cfg.GitHubOrganization)})
	}
	if config.Enabled(cfg.Keep.PersonallyMentioned) && thread.Reason == "mention" {
		decision.Rules = append(decision.Rules, model.Rule{ID: rulePersonalMention, Evidence: `GitHub notification reason is "mention"`})
	}
	if config.Enabled(cfg.Keep.PersonallyAssigned) && (thread.Reason == "assign" || containsUser(enrichment.Subject.Assignees, cfg.User)) {
		decision.Rules = append(decision.Rules, model.Rule{ID: rulePersonalAssign, Evidence: fmt.Sprintf("%q is personally assigned", cfg.User)})
	}
	if config.Enabled(cfg.Keep.IndividuallyReviewRequested) && containsUser(enrichment.Subject.RequestedReviewers, cfg.User) {
		decision.Rules = append(decision.Rules, model.Rule{ID: ruleIndividualReview, Evidence: fmt.Sprintf("%q appears in requested_reviewers; team requests alone do not match", cfg.User)})
	}
	if config.Enabled(cfg.Keep.AuthoredByUser) && (thread.Reason == "author" || strings.EqualFold(resourceAuthor(enrichment.Subject), cfg.User)) {
		decision.Rules = append(decision.Rules, model.Rule{ID: ruleUserAuthored, Evidence: fmt.Sprintf("%q authored the notification subject", cfg.User)})
	}
	if config.Enabled(cfg.Keep.TeamMentionedDiscussions) && thread.Subject.Type == "Discussion" {
		bodies := []string{enrichment.Subject.Body}
		for _, comment := range enrichment.DiscussionComments {
			bodies = append(bodies, comment.Body)
		}
		for _, team := range matchingTeamMentions(cfg.DiscussionTeamSlugs, bodies...) {
			decision.Rules = append(decision.Rules, model.Rule{ID: ruleDiscussionTeam, Evidence: fmt.Sprintf("discussion body or complete comment history contains exact team mention @%s", team)})
		}
	}
	if !IsHushableSubjectType(thread.Subject.Type) {
		decision.Rules = append(decision.Rules, model.Rule{ID: ruleSafetyUnsupported, Evidence: fmt.Sprintf("subject type %q is not in the explicit hush allowlist", thread.Subject.Type)})
	}

	requirements := EnrichmentRequirements(cfg, thread)
	var evidenceErrors []error
	if requirements.Subject && enrichment.SubjectErr != nil {
		evidenceErrors = append(evidenceErrors, enrichment.SubjectErr)
	}
	if requirements.DiscussionComments && enrichment.DiscussionCommentsErr != nil {
		evidenceErrors = append(evidenceErrors, enrichment.DiscussionCommentsErr)
	}
	if evidenceErr := errors.Join(evidenceErrors...); evidenceErr != nil {
		decision.EnrichmentError = evidenceErr.Error()
		if len(decision.Rules) == 0 {
			decision.Rules = append(decision.Rules, model.Rule{ID: ruleSafetyFailure, Evidence: fmt.Sprintf("required classification evidence was unavailable: %v", evidenceErr)})
		}
	}
	if len(decision.Rules) > 0 {
		decision.Action = model.ActionKeep
		return decision
	}
	decision.Action = model.ActionUnsubscribeAndMarkDone
	decision.Rules = []model.Rule{{ID: ruleAllOther, Evidence: "no enabled keep or safety rule matched after successful evaluation"}}
	return decision
}

func resourceAuthor(resource model.Resource) string {
	if resource.User.Login != "" {
		return resource.User.Login
	}
	return resource.Author.Login
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
