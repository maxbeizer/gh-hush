// Package predicate implements the closed, composable matching vocabulary used
// by the v3 gh-hush configuration. A predicate answers a single question about
// a notification and the evidence it needs. Predicates combine with any, all,
// and not, and each acquires only the GitHub evidence it actually inspects, so
// evidence planning falls out of evaluation rather than being mirrored by hand.
package predicate

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/maxbeizer/gh-hush/internal/model"
)

var now = time.Now

// Identity is the acting user, their organization, and their team slugs in
// org/team-slug form. Predicates resolve "me" and "my_teams" against it.
type Identity struct {
	User         string
	Organization string
	Teams        []string
}

// Evidence provides lazy, memoized access to the GitHub resources a predicate
// inspects. Each distinct resource is fetched at most once regardless of how
// many predicates request it, and a fetch failure is reported to the caller as
// a matching error rather than silently treated as a non-match.
type Evidence struct {
	Notification model.Notification
	Identity     Identity

	fetchSubject  func() (model.Resource, error)
	fetchComments func() ([]model.Resource, error)

	subjectDone bool
	subject     model.Resource
	subjectErr  error

	commentsDone bool
	comments     []model.Resource
	commentsErr  error
}

// NewEvidence builds evidence backed by the supplied lazy fetchers.
func NewEvidence(n model.Notification, id Identity, fetchSubject func() (model.Resource, error), fetchComments func() ([]model.Resource, error)) *Evidence {
	return &Evidence{Notification: n, Identity: id, fetchSubject: fetchSubject, fetchComments: fetchComments}
}

// Subject returns the notification subject resource, fetching once.
func (e *Evidence) Subject() (model.Resource, error) {
	if !e.subjectDone {
		e.subjectDone = true
		if e.fetchSubject != nil {
			e.subject, e.subjectErr = e.fetchSubject()
		}
	}
	return e.subject, e.subjectErr
}

// Comments returns the subject's complete comment history, fetching once.
func (e *Evidence) Comments() ([]model.Resource, error) {
	if !e.commentsDone {
		e.commentsDone = true
		if e.fetchComments != nil {
			e.comments, e.commentsErr = e.fetchComments()
		}
	}
	return e.comments, e.commentsErr
}

// SubjectFetched reports whether classification already fetched the subject.
func (e *Evidence) SubjectFetched() bool { return e.subjectDone }

// SubjectValue returns the last subject fetched, or the zero resource.
func (e *Evidence) SubjectValue() model.Resource { return e.subject }

func (e *Evidence) resolveLogin(value string) string {
	if strings.EqualFold(value, "me") {
		return e.Identity.User
	}
	return value
}

func (e *Evidence) resolveTeams(value string) []string {
	if strings.EqualFold(value, "my_teams") {
		return e.Identity.Teams
	}
	return []string{value}
}

// Predicate answers one question about a notification. Match reports whether
// the notification matches and, separately, whether required evidence was
// unavailable. A non-nil error means the answer is indeterminate.
type Predicate interface {
	Match(e *Evidence) (bool, error)
	Describe() string
}

// Node is a decoded predicate tree. The zero Node matches everything, which
// lets a rule omit `when` to act as an unconditional catch-all.
type Node struct {
	pred Predicate
}

// Match evaluates the node. An empty node matches unconditionally.
func (n Node) Match(e *Evidence) (bool, error) {
	if n.pred == nil {
		return true, nil
	}
	return n.pred.Match(e)
}

// Describe renders a short human explanation of the node.
func (n Node) Describe() string {
	if n.pred == nil {
		return "any notification"
	}
	return n.pred.Describe()
}

// Empty reports whether the node carries no predicate.
func (n Node) Empty() bool { return n.pred == nil }

type allPredicate struct{ preds []Predicate }

func (p allPredicate) Match(e *Evidence) (bool, error) {
	var firstErr error
	for _, child := range p.preds {
		matched, err := child.Match(e)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !matched {
			// A conclusive non-match makes the conjunction false regardless of
			// any indeterminate sibling, so the result is not evidence-limited.
			return false, nil
		}
	}
	if firstErr != nil {
		return false, firstErr
	}
	return true, nil
}

func (p allPredicate) Describe() string { return "all of (" + describeAll(p.preds) + ")" }

type anyPredicate struct{ preds []Predicate }

func (p anyPredicate) Match(e *Evidence) (bool, error) {
	var firstErr error
	for _, child := range p.preds {
		matched, err := child.Match(e)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if matched {
			return true, nil
		}
	}
	if firstErr != nil {
		return false, firstErr
	}
	return false, nil
}

func (p anyPredicate) Describe() string { return "any of (" + describeAll(p.preds) + ")" }

type notPredicate struct{ pred Predicate }

func (p notPredicate) Match(e *Evidence) (bool, error) {
	matched, err := p.pred.Match(e)
	if err != nil {
		return false, err
	}
	return !matched, nil
}

func (p notPredicate) Describe() string { return "not (" + p.pred.Describe() + ")" }

func describeAll(preds []Predicate) string {
	parts := make([]string, len(preds))
	for i, pred := range preds {
		parts[i] = pred.Describe()
	}
	return strings.Join(parts, ", ")
}

type repositoryPredicate struct {
	owner    string
	ownerNot string
	anyOf    []string
}

func (p repositoryPredicate) Match(e *Evidence) (bool, error) {
	full := e.Notification.Repository.FullName
	owner := repositoryOwner(full)
	if p.owner != "" && !strings.EqualFold(owner, p.owner) {
		return false, nil
	}
	if p.ownerNot != "" && strings.EqualFold(owner, p.ownerNot) {
		return false, nil
	}
	if len(p.anyOf) > 0 {
		for _, pattern := range p.anyOf {
			if matchRepositoryPattern(pattern, full, owner) {
				return true, nil
			}
		}
		return false, nil
	}
	// With only owner / owner_not constraints, reaching here means they held.
	return true, nil
}

func (p repositoryPredicate) Describe() string {
	switch {
	case p.ownerNot != "":
		return fmt.Sprintf("repository owner is not %q", p.ownerNot)
	case p.owner != "":
		return fmt.Sprintf("repository owner is %q", p.owner)
	default:
		return "repository matches " + strings.Join(p.anyOf, ", ")
	}
}

func matchRepositoryPattern(pattern, fullName, owner string) bool {
	if strings.Contains(pattern, "/") {
		if strings.EqualFold(pattern, fullName) {
			return true
		}
		ok, _ := path.Match(strings.ToLower(pattern), strings.ToLower(fullName))
		return ok
	}
	if strings.EqualFold(pattern, owner) {
		return true
	}
	ok, _ := path.Match(strings.ToLower(pattern), strings.ToLower(owner))
	return ok
}

func repositoryOwner(fullName string) string {
	return strings.SplitN(fullName, "/", 2)[0]
}

type subjectTypePredicate struct{ types []string }

func (p subjectTypePredicate) Match(e *Evidence) (bool, error) {
	for _, want := range p.types {
		if strings.EqualFold(want, e.Notification.Subject.Type) {
			return true, nil
		}
	}
	return false, nil
}

func (p subjectTypePredicate) Describe() string {
	return "subject type is " + strings.Join(p.types, " or ")
}

type reasonPredicate struct{ reasons []string }

func (p reasonPredicate) Match(e *Evidence) (bool, error) {
	for _, want := range p.reasons {
		if strings.EqualFold(want, e.Notification.Reason) {
			return true, nil
		}
	}
	return false, nil
}

func (p reasonPredicate) Describe() string {
	return "notification reason is " + strings.Join(p.reasons, " or ")
}

type statePredicate struct {
	state  string
	negate bool
}

func (p statePredicate) Match(e *Evidence) (bool, error) {
	subject, err := e.Subject()
	if err != nil {
		return false, err
	}
	subjectType := e.Notification.Subject.Type
	if !stateKnown(subjectType, subject) {
		return false, fmt.Errorf("subject state %q is unavailable or unsupported", subject.State)
	}
	matched := stateMatches(p.state, subjectType, subject)
	if p.negate {
		return !matched, nil
	}
	return matched, nil
}

func (p statePredicate) Describe() string {
	if p.negate {
		return fmt.Sprintf("subject state is not %q", p.state)
	}
	return fmt.Sprintf("subject state is %q", p.state)
}

func stateMatches(want, subjectType string, subject model.Resource) bool {
	switch strings.ToLower(want) {
	case "open":
		return stateIsOpen(subjectType, subject)
	case "closed":
		return stateIsClosed(subjectType, subject)
	case "locked":
		return subject.State == "locked"
	case "merged":
		return subjectType == "PullRequest" && subject.Merged
	case "draft":
		return subjectType == "PullRequest" && subject.Draft && subject.State == "open"
	case "answered":
		return subjectType == "Discussion" && subject.AnswerChosenAt != nil
	default:
		return false
	}
}

func stateKnown(subjectType string, subject model.Resource) bool {
	if subject.State == "open" || subject.State == "closed" {
		return true
	}
	return subjectType == "Discussion" && subject.State == "locked"
}

func stateIsOpen(subjectType string, subject model.Resource) bool {
	if subject.State == "open" {
		return true
	}
	// GitHub reports "locked" instead of open/closed for a locked Discussion.
	// state_reason stays nil while it is open and is set once it was closed.
	return subjectType == "Discussion" && subject.State == "locked" && subject.StateReason == nil
}

func stateIsClosed(subjectType string, subject model.Resource) bool {
	if subject.State == "closed" {
		return true
	}
	return subjectType == "Discussion" && subject.State == "locked" && subject.StateReason != nil
}

type assigneePredicate struct{ target string }

func (p assigneePredicate) Match(e *Evidence) (bool, error) {
	login := e.resolveLogin(p.target)
	if strings.EqualFold(p.target, "me") && e.Notification.Reason == "assign" {
		return true, nil
	}
	subjectType := e.Notification.Subject.Type
	if subjectType != "Issue" && subjectType != "PullRequest" {
		return false, nil
	}
	subject, err := e.Subject()
	if err != nil {
		return false, err
	}
	return containsUser(subject.Assignees, login), nil
}

func (p assigneePredicate) Describe() string {
	return fmt.Sprintf("assigned to %s", p.target)
}

type authorPredicate struct{ target string }

func (p authorPredicate) Match(e *Evidence) (bool, error) {
	login := e.resolveLogin(p.target)
	if strings.EqualFold(login, e.Identity.User) && e.Notification.Reason == "author" {
		return true, nil
	}
	subject, err := e.Subject()
	if err != nil {
		return false, err
	}
	return strings.EqualFold(resourceAuthor(subject), login), nil
}

func (p authorPredicate) Describe() string {
	return fmt.Sprintf("authored by %s", p.target)
}

type reviewRequestedPredicate struct{ target string }

func (p reviewRequestedPredicate) Match(e *Evidence) (bool, error) {
	if e.Notification.Subject.Type != "PullRequest" {
		return false, nil
	}
	login := e.resolveLogin(p.target)
	subject, err := e.Subject()
	if err != nil {
		return false, err
	}
	return containsUser(subject.RequestedReviewers, login), nil
}

func (p reviewRequestedPredicate) Describe() string {
	return fmt.Sprintf("review requested from %s", p.target)
}

type reviewRequestedTeamPredicate struct{ target string }

func (p reviewRequestedTeamPredicate) Match(e *Evidence) (bool, error) {
	if e.Notification.Subject.Type != "PullRequest" {
		return false, nil
	}
	teams := e.resolveTeams(p.target)
	if len(teams) == 0 {
		return false, nil
	}
	subject, err := e.Subject()
	if err != nil {
		return false, err
	}
	return len(matchingRequestedTeams(teams, subject.RequestedTeams, e.Notification.Repository.FullName)) > 0, nil
}

func (p reviewRequestedTeamPredicate) Describe() string {
	return fmt.Sprintf("review requested from team %s", p.target)
}

type mentionsPredicate struct {
	target string
	team   bool
	scope  []string
}

func (p mentionsPredicate) Match(e *Evidence) (bool, error) {
	var targets []string
	if p.team {
		targets = teamsForOwner(e.resolveTeams(p.target), e.Notification.Repository.FullName)
	} else {
		targets = []string{e.resolveLogin(p.target)}
	}
	if len(targets) == 0 {
		return false, nil
	}
	var bodies []string
	if p.searches("body") {
		subject, err := e.Subject()
		if err != nil {
			return false, err
		}
		bodies = append(bodies, subject.Body)
	}
	if p.searches("comments") {
		comments, err := e.Comments()
		if err != nil {
			return false, err
		}
		for _, comment := range comments {
			bodies = append(bodies, comment.Body)
		}
	}
	for _, target := range targets {
		if mentionsAny(target, bodies...) {
			return true, nil
		}
	}
	return false, nil
}

func (p mentionsPredicate) searches(scope string) bool {
	for _, entry := range p.scope {
		if entry == scope {
			return true
		}
	}
	return false
}

func (p mentionsPredicate) Describe() string {
	kind := "user"
	if p.team {
		kind = "team"
	}
	return fmt.Sprintf("%s %s mentioned in %s", kind, p.target, strings.Join(p.scope, " and "))
}

type agePredicate struct {
	olderThan time.Duration
	newerThan time.Duration
}

func (p agePredicate) Match(e *Evidence) (bool, error) {
	updated, err := time.Parse(time.RFC3339, e.Notification.UpdatedAt)
	if err != nil {
		return false, fmt.Errorf("notification update time %q is unavailable or unsupported", e.Notification.UpdatedAt)
	}
	age := now().Sub(updated)
	if p.olderThan > 0 && age <= p.olderThan {
		return false, nil
	}
	if p.newerThan > 0 && age >= p.newerThan {
		return false, nil
	}
	return true, nil
}

func (p agePredicate) Describe() string {
	switch {
	case p.olderThan > 0 && p.newerThan > 0:
		return fmt.Sprintf("age between %s and %s", p.newerThan, p.olderThan)
	case p.olderThan > 0:
		return fmt.Sprintf("older than %s", p.olderThan)
	default:
		return fmt.Sprintf("newer than %s", p.newerThan)
	}
}

func resourceAuthor(resource model.Resource) string {
	if resource.User.Login != "" {
		return resource.User.Login
	}
	return resource.Author.Login
}

func containsUser(users []model.User, login string) bool {
	if login == "" {
		return false
	}
	for _, user := range users {
		if strings.EqualFold(user.Login, login) {
			return true
		}
	}
	return false
}

func matchingRequestedTeams(configured []string, requested []model.Team, repository string) []string {
	var matches []string
	owner := repositoryOwner(repository)
	for _, team := range configured {
		parts := strings.SplitN(team, "/", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], owner) {
			continue
		}
		for _, requestedTeam := range requested {
			if strings.EqualFold(parts[1], requestedTeam.Slug) {
				matches = append(matches, team)
				break
			}
		}
	}
	return matches
}

// teamsForOwner keeps only configured org/team-slug entries whose organization
// matches the notification repository's owner, mirroring the owner scoping
// applied to team review requests.
func teamsForOwner(configured []string, repository string) []string {
	owner := repositoryOwner(repository)
	var scoped []string
	for _, team := range configured {
		parts := strings.SplitN(team, "/", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], owner) {
			continue
		}
		scoped = append(scoped, team)
	}
	return scoped
}

func mentionsAny(target string, bodies ...string) bool {
	if target == "" {
		return false
	}
	pattern := regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_.-])@` + regexp.QuoteMeta(target) + `([^A-Za-z0-9_.-]|$)`)
	for _, body := range bodies {
		if pattern.MatchString(body) {
			return true
		}
	}
	return false
}
