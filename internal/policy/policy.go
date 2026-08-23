package policy

import (
	"context"
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

// EvidenceSource is the single boundary between policy evaluation and GitHub.
// Evaluator decides which evidence is necessary; implementations only acquire
// the requested GitHub resource without interpreting policy.
type EvidenceSource interface {
	FetchSubject(context.Context, model.Notification) (model.Resource, error)
	FetchDiscussionComments(context.Context, model.Notification) ([]model.Resource, error)
}

// Evaluator owns evidence selection and acquisition, safety handling, keep
// rules, the explicit hush allowlist, and production of the final decision.
type Evaluator struct {
	cfg    config.Config
	source EvidenceSource
}

func NewEvaluator(cfg config.Config, source EvidenceSource) *Evaluator {
	return &Evaluator{cfg: cfg, source: source}
}

// Evaluate produces the complete policy decision for a notification.
func (e *Evaluator) Evaluate(ctx context.Context, thread model.Notification) model.Decision {
	requirements := e.evidenceRequirements(thread)
	var evidence evaluationEvidence
	if requirements.subject {
		evidence.subject, evidence.subjectErr = e.source.FetchSubject(ctx, thread)
	}
	if requirements.discussionComments {
		evidence.discussionComments, evidence.discussionCommentsErr = e.source.FetchDiscussionComments(ctx, thread)
	}
	return e.decide(thread, requirements, evidence)
}

type evidenceRequirements struct {
	subject            bool
	discussionComments bool
}

type evaluationEvidence struct {
	subject               model.Resource
	discussionComments    []model.Resource
	subjectErr            error
	discussionCommentsErr error
}

func isHushableSubjectType(subjectType string) bool {
	_, ok := hushableSubjectTypes[subjectType]
	return ok
}

func (e *Evaluator) evidenceRequirements(thread model.Notification) evidenceRequirements {
	if !isHushableSubjectType(thread.Subject.Type) {
		return evidenceRequirements{}
	}
	repositoryOrg := strings.SplitN(thread.Repository.FullName, "/", 2)[0]
	if (config.Enabled(e.cfg.Keep.ExternalOrganizationIssues) && !strings.EqualFold(repositoryOrg, e.cfg.GitHubOrganization)) ||
		(config.Enabled(e.cfg.Keep.PersonallyMentioned) && thread.Reason == "mention") ||
		(config.Enabled(e.cfg.Keep.PersonallyAssigned) && thread.Reason == "assign") ||
		(config.Enabled(e.cfg.Keep.AuthoredByUser) && thread.Reason == "author") {
		// The action is already conclusively Keep; no API evidence is required.
		return evidenceRequirements{}
	}
	var requirements evidenceRequirements
	t := thread.Subject.Type
	if config.Enabled(e.cfg.Keep.PersonallyAssigned) && thread.Reason != "assign" && (t == "Issue" || t == "PullRequest") {
		requirements.subject = true
	}
	if config.Enabled(e.cfg.Keep.IndividuallyReviewRequested) && t == "PullRequest" {
		requirements.subject = true
	}
	if config.Enabled(e.cfg.Keep.AuthoredByUser) && thread.Reason != "author" {
		requirements.subject = true
	}
	if config.Enabled(e.cfg.Keep.TeamMentionedDiscussions) && len(e.cfg.DiscussionTeamSlugs) > 0 && t == "Discussion" {
		requirements.subject = true
		requirements.discussionComments = true
	}
	return requirements
}

func (e *Evaluator) decide(thread model.Notification, requirements evidenceRequirements, evidence evaluationEvidence) model.Decision {
	decision := model.Decision{Thread: thread, URL: notificationURL(thread, evidence)}
	repositoryOrg := strings.SplitN(thread.Repository.FullName, "/", 2)[0]
	if config.Enabled(e.cfg.Keep.ExternalOrganizationIssues) && !strings.EqualFold(repositoryOrg, e.cfg.GitHubOrganization) {
		decision.Rules = append(decision.Rules, model.Rule{ID: ruleExternalOrganization, Evidence: fmt.Sprintf("repository organization %q differs from configured organization %q", repositoryOrg, e.cfg.GitHubOrganization)})
	}
	if config.Enabled(e.cfg.Keep.PersonallyMentioned) && thread.Reason == "mention" {
		decision.Rules = append(decision.Rules, model.Rule{ID: rulePersonalMention, Evidence: `GitHub notification reason is "mention"`})
	}
	if config.Enabled(e.cfg.Keep.PersonallyAssigned) && (thread.Reason == "assign" || containsUser(evidence.subject.Assignees, e.cfg.User)) {
		decision.Rules = append(decision.Rules, model.Rule{ID: rulePersonalAssign, Evidence: fmt.Sprintf("%q is personally assigned", e.cfg.User)})
	}
	if config.Enabled(e.cfg.Keep.IndividuallyReviewRequested) && containsUser(evidence.subject.RequestedReviewers, e.cfg.User) {
		decision.Rules = append(decision.Rules, model.Rule{ID: ruleIndividualReview, Evidence: fmt.Sprintf("%q appears in requested_reviewers; team requests alone do not match", e.cfg.User)})
	}
	if config.Enabled(e.cfg.Keep.AuthoredByUser) && (thread.Reason == "author" || strings.EqualFold(resourceAuthor(evidence.subject), e.cfg.User)) {
		decision.Rules = append(decision.Rules, model.Rule{ID: ruleUserAuthored, Evidence: fmt.Sprintf("%q authored the notification subject", e.cfg.User)})
	}
	if config.Enabled(e.cfg.Keep.TeamMentionedDiscussions) && thread.Subject.Type == "Discussion" {
		bodies := []string{evidence.subject.Body}
		for _, comment := range evidence.discussionComments {
			bodies = append(bodies, comment.Body)
		}
		for _, team := range matchingTeamMentions(e.cfg.DiscussionTeamSlugs, bodies...) {
			decision.Rules = append(decision.Rules, model.Rule{ID: ruleDiscussionTeam, Evidence: fmt.Sprintf("discussion body or complete comment history contains exact team mention @%s", team)})
		}
	}
	if !isHushableSubjectType(thread.Subject.Type) {
		decision.Rules = append(decision.Rules, model.Rule{ID: ruleSafetyUnsupported, Evidence: fmt.Sprintf("subject type %q is not in the explicit hush allowlist", thread.Subject.Type)})
	}

	var evidenceErrors []error
	if requirements.subject && evidence.subjectErr != nil {
		evidenceErrors = append(evidenceErrors, evidence.subjectErr)
	}
	if requirements.discussionComments && evidence.discussionCommentsErr != nil {
		evidenceErrors = append(evidenceErrors, evidence.discussionCommentsErr)
	}
	if evidenceErr := errors.Join(evidenceErrors...); evidenceErr != nil {
		decision.EnrichmentError = evidenceErr.Error()
		decision.Rules = append(decision.Rules, model.Rule{ID: ruleSafetyFailure, Evidence: fmt.Sprintf("required classification evidence was unavailable: %v", evidenceErr)})
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

func notificationURL(thread model.Notification, evidence evaluationEvidence) string {
	if evidence.subject.HTMLURL != "" {
		return evidence.subject.HTMLURL
	}
	if thread.Subject.URL != "" {
		return thread.Subject.URL
	}
	if thread.Repository.HTMLURL != "" {
		return thread.Repository.HTMLURL + "/notifications"
	}
	return "unavailable"
}
