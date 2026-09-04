package policy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/maxbeizer/gh-hush/internal/config"
	"github.com/maxbeizer/gh-hush/internal/model"
	"github.com/maxbeizer/gh-hush/internal/reporturl"
)

const (
	ruleExternalOrganization = "keep.external_organization"
	rulePersonalMention      = "keep.personal_mention"
	rulePersonalAssign       = "keep.personal_assignment"
	ruleIndividualReview     = "keep.individual_review_request"
	ruleActiveTeamReview     = "keep.active_team_review_request"
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

// Evaluate produces a policy decision using only classification-required
// evidence. In particular, application revalidation does not make requests
// solely to improve a report URL.
func (e *Evaluator) Evaluate(ctx context.Context, thread model.Notification) model.Decision {
	return e.evaluate(ctx, thread, false)
}

// EvaluateForPreview also attempts to resolve the subject resource's exact
// browser URL. Display-only subject data and failures are isolated from policy
// classification and its safety semantics.
func (e *Evaluator) EvaluateForPreview(ctx context.Context, thread model.Notification) model.Decision {
	return e.evaluate(ctx, thread, true)
}

func (e *Evaluator) evaluate(ctx context.Context, thread model.Notification, resolveDisplayURL bool) model.Decision {
	requirements := e.evidenceRequirements(thread)
	var evidence evaluationEvidence
	if requirements.subject {
		// FetchSubject intentionally handles an empty API URL as an error. Required
		// evidence must never be skipped merely because the URL is absent.
		evidence.subject, evidence.subjectErr = e.source.FetchSubject(ctx, thread)
	}
	if requirements.discussionComments {
		evidence.discussionComments, evidence.discussionCommentsErr = e.source.FetchDiscussionComments(ctx, thread)
	}

	displaySubject := evidence.subject
	if resolveDisplayURL && !requirements.subject && thread.Subject.URL != "" {
		// Do not place this resource in classification evidence: fields returned by
		// a display-only request must not add assignment, authorship, or other rules.
		displaySubject, _ = e.source.FetchSubject(ctx, thread)
	}
	decision := e.decide(thread, requirements, evidence)
	repositoryURL := reporturl.Repository(thread.Repository.HTMLURL, thread.Repository.FullName)
	decision.URL = reporturl.Safe(displaySubject.HTMLURL, repositoryURL)
	return decision
}

type evidenceRequirements struct {
	subject            bool
	pullRequestState   bool
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
	if config.Enabled(e.cfg.Keep.ActiveTeamReviewRequestedPullRequests) && len(e.cfg.TeamSlugs) > 0 && t == "PullRequest" {
		requirements.subject = true
		requirements.pullRequestState = true
	}
	if config.Enabled(e.cfg.Keep.AuthoredByUser) && thread.Reason != "author" {
		requirements.subject = true
	}
	if config.Enabled(e.cfg.Keep.TeamMentionedDiscussions) && len(e.cfg.TeamSlugs) > 0 && t == "Discussion" {
		requirements.subject = true
		requirements.discussionComments = true
	}
	return requirements
}

func (e *Evaluator) decide(thread model.Notification, requirements evidenceRequirements, evidence evaluationEvidence) model.Decision {
	decision := model.Decision{Thread: thread}
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
	if config.Enabled(e.cfg.Keep.ActiveTeamReviewRequestedPullRequests) && thread.Subject.Type == "PullRequest" && evidence.subject.State == "open" {
		for _, team := range matchingRequestedTeams(e.cfg.TeamSlugs, evidence.subject.RequestedTeams) {
			decision.Rules = append(decision.Rules, model.Rule{ID: ruleActiveTeamReview, Evidence: fmt.Sprintf("open pull request currently requests review from @%s", team)})
		}
	}
	if config.Enabled(e.cfg.Keep.AuthoredByUser) && (thread.Reason == "author" || strings.EqualFold(resourceAuthor(evidence.subject), e.cfg.User)) {
		decision.Rules = append(decision.Rules, model.Rule{ID: ruleUserAuthored, Evidence: fmt.Sprintf("%q authored the notification subject", e.cfg.User)})
	}
	if config.Enabled(e.cfg.Keep.TeamMentionedDiscussions) && thread.Subject.Type == "Discussion" {
		bodies := []string{evidence.subject.Body}
		for _, comment := range evidence.discussionComments {
			bodies = append(bodies, comment.Body)
		}
		for _, team := range matchingTeamMentions(e.cfg.TeamSlugs, bodies...) {
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
	if requirements.pullRequestState && evidence.subjectErr == nil && evidence.subject.State != "open" && evidence.subject.State != "closed" {
		evidenceErrors = append(evidenceErrors, fmt.Errorf("pull request state %q is unavailable or unsupported", evidence.subject.State))
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

func matchingRequestedTeams(configured []string, requested []model.Team) []string {
	var matches []string
	for _, configuredTeam := range configured {
		parts := strings.SplitN(configuredTeam, "/", 2)
		for _, requestedTeam := range requested {
			if len(parts) == 2 && strings.EqualFold(parts[1], requestedTeam.Slug) {
				matches = append(matches, configuredTeam)
				break
			}
		}
	}
	return matches
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
