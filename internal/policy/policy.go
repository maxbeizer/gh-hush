// Package policy evaluates the ordered v3 rule list against a notification,
// acquiring only the GitHub evidence the matching predicates inspect.
package policy

import (
	"context"

	"github.com/maxbeizer/gh-hush/internal/config"
	"github.com/maxbeizer/gh-hush/internal/model"
	"github.com/maxbeizer/gh-hush/internal/predicate"
	"github.com/maxbeizer/gh-hush/internal/reporturl"
)

const (
	ruleSafetyUnsupported = "safety.keep_unsupported_subject_type"
	ruleSafetyFailure     = "safety.keep_on_missing_evidence"
	ruleDefault           = "defaults.action"
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

// Evaluator owns evidence selection and acquisition, safety handling, ordered
// rule evaluation, and production of the final decision.
type Evaluator struct {
	cfg    config.Config
	source EvidenceSource
}

func NewEvaluator(cfg config.Config, source EvidenceSource) *Evaluator {
	return &Evaluator{cfg: cfg, source: source}
}

// Evaluate produces a policy decision using only classification-required
// evidence.
func (e *Evaluator) Evaluate(ctx context.Context, thread model.Notification) model.Decision {
	return e.evaluate(ctx, thread, false)
}

// EvaluateForPreview also attempts to resolve the subject resource's exact
// browser URL. Display-only subject data and failures are isolated from policy
// classification and its safety semantics.
func (e *Evaluator) EvaluateForPreview(ctx context.Context, thread model.Notification) model.Decision {
	return e.evaluate(ctx, thread, true)
}

func isHushableSubjectType(subjectType string) bool {
	_, ok := hushableSubjectTypes[subjectType]
	return ok
}

func (e *Evaluator) evaluate(ctx context.Context, thread model.Notification, resolveDisplayURL bool) model.Decision {
	evidence := predicate.NewEvidence(thread, e.cfg.PredicateIdentity(),
		func() (model.Resource, error) { return e.source.FetchSubject(ctx, thread) },
		func() ([]model.Resource, error) { return e.source.FetchDiscussionComments(ctx, thread) },
	)
	decision := e.decide(thread, evidence)

	displaySubject := evidence.SubjectValue()
	if resolveDisplayURL && !evidence.SubjectFetched() && thread.Subject.URL != "" {
		// A display-only request must not add classification evidence.
		displaySubject, _ = e.source.FetchSubject(ctx, thread)
	}
	repositoryURL := reporturl.Repository(thread.Repository.HTMLURL, thread.Repository.FullName)
	decision.URL = reporturl.Safe(displaySubject.HTMLURL, repositoryURL)
	return decision
}

func (e *Evaluator) decide(thread model.Notification, evidence *predicate.Evidence) model.Decision {
	decision := model.Decision{Thread: thread}

	// Safety: subject types outside the explicit hush allowlist are never
	// hushed, and no user rule can defeat that.
	if !isHushableSubjectType(thread.Subject.Type) {
		decision.Action = model.ActionKeep
		decision.Rules = []model.Rule{{ID: ruleSafetyUnsupported, Evidence: "subject type is not in the explicit hush allowlist"}}
		return decision
	}

	for _, rule := range e.cfg.Rules {
		matched, err := rule.When.Match(evidence)
		if err != nil {
			if e.cfg.Defaults.OnMissingEvidence == config.OnMissingKeep {
				decision.Action = model.ActionKeep
				decision.EnrichmentError = err.Error()
				decision.Rules = []model.Rule{{ID: ruleSafetyFailure, Evidence: "required classification evidence was unavailable: " + err.Error()}}
				return decision
			}
			// Posture is hush-on-missing: treat the indeterminate rule as a
			// non-match and continue, but record the warning for the report.
			if decision.EnrichmentError == "" {
				decision.EnrichmentError = err.Error()
			}
			continue
		}
		if matched {
			decision.Rules = []model.Rule{{ID: rule.Name, Evidence: rule.When.Describe()}}
			if rule.Action == config.ActionKeep {
				decision.Action = model.ActionKeep
			} else {
				decision.Action = model.ActionUnsubscribeAndMarkDone
			}
			return decision
		}
	}

	if e.cfg.Defaults.Action == config.ActionKeep {
		decision.Action = model.ActionKeep
	} else {
		decision.Action = model.ActionUnsubscribeAndMarkDone
	}
	decision.Rules = []model.Rule{{ID: ruleDefault, Evidence: "no rule matched; applied the terminal default action"}}
	return decision
}
