package predicate

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/maxbeizer/gh-hush/internal/model"
	"gopkg.in/yaml.v3"
)

func TestDecodeRejectsMalformedConditions(t *testing.T) {
	tests := []struct{ name, document, want string }{
		{"scalar condition", `when: nonsense`, "must be a mapping"},
		{"empty mapping", "when: {}", "at least one predicate"},
		{"unknown predicate", "when:\n  nonsense: true", `unknown predicate "nonsense"`},
		{"any is not a list", "when:\n  any:\n    reason: mention", "must be a list"},
		{"empty list", "when:\n  all: []", "at least one condition"},
		{"unknown repository field", "when:\n  repository:\n    nonsense: github", `unknown repository field "nonsense"`},
		{"empty repository", "when:\n  repository: {}", "must set owner, owner_not, or any_of"},
		{"repository list entry is empty", "when:\n  repository: []", "must not be empty"},
		{"state is a list", "when:\n  state: [open, closed]", "must be a single value"},
		{"empty subject type", `when: {subject_type: ""}`, "must not be empty"},
		{"nested list value", "when:\n  subject_type:\n    - [Issue]", "list entries must be non-empty values"},
		{"age is not a mapping", "when:\n  age: 30d", "age must be a mapping"},
		{"empty age", "when:\n  age: {}", "must set older_than or newer_than"},
		{"unknown age field", "when:\n  age:\n    around: 30d", `unknown age field "around"`},
		{"invalid duration", "when:\n  age:\n    older_than: soon", `invalid duration "soon"`},
		{"negative day duration", "when:\n  age:\n    older_than: -3d", `invalid duration "-3d"`},
		{"unknown search scope", "when:\n  mentions_team: my_teams\n  search: [title]", `search scope "title" must be body or comments`},
		{"search is a mapping", "when:\n  mentions_user: me\n  search: {body: true}", "search"},
		{"unsupported state value", "when:\n  state: nonsense", "must be open, closed, locked, merged, draft, or answered"},
		{"search without a mention sibling", "when:\n  reason: mention\n  search: [comments]", "exactly one sibling"},
		{"non-string scalar", "when:\n  author: true", "must be a single value"},
		{"oversized day duration", "when:\n  age:\n    older_than: 999999999d", "too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decode(tt.document)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("decode error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEmptyNodeMatchesEverything(t *testing.T) {
	var node Node
	matched, err := node.Match(newTestEvidence(model.Notification{}, model.Resource{}, nil))
	if !node.Empty() || !matched || err != nil {
		t.Fatalf("empty=%v matched=%v err=%v", node.Empty(), matched, err)
	}
	if node.Describe() != "any notification" {
		t.Fatalf("Describe() = %q", node.Describe())
	}
	if _, err := node.MarshalYAML(); err == nil {
		t.Fatal("predicate nodes must not be marshaled back to YAML")
	}
}

func TestNodeDecodingSupportsAliasesAndImplicitAll(t *testing.T) {
	document := `
base: &base
  subject_type: [PullRequest]
when:
  <<: []
  any:
    - *base
`
	// Sibling keys of a mapping form an implicit all, and an alias reuses the
	// anchored condition rather than failing to decode.
	node, err := decode(strings.Replace(document, "  <<: []\n", "", 1))
	if err != nil {
		t.Fatalf("decode error = %v", err)
	}
	matched, err := node.Match(newTestEvidence(notification("github/repo", "PullRequest", "subscribed"), model.Resource{}, nil))
	if !matched || err != nil {
		t.Fatalf("matched=%v err=%v", matched, err)
	}
}

func TestPredicateMatching(t *testing.T) {
	item := notification("github/repo", "PullRequest", "subscribed")
	subject := model.Resource{
		State:              "open",
		Body:               "cc @octocat and @github/notifications",
		User:               model.User{Login: "hubot"},
		Assignees:          []model.User{{Login: "octocat"}},
		RequestedReviewers: []model.User{{Login: "octocat"}},
		RequestedTeams:     []model.Team{{Slug: "notifications"}},
	}
	tests := []struct {
		name      string
		condition string
		want      bool
	}{
		{"repository owner", "repository:\n  owner: github", true},
		{"repository owner mismatch", "repository:\n  owner: other", false},
		{"repository owner_not", "repository:\n  owner_not: github", false},
		{"repository owner glob", "repository: gith*", true},
		{"repository full name glob", "repository: [other/*, github/re*]", true},
		{"repository exact miss", "repository: github/other", false},
		{"subject type", "subject_type: [Issue, PullRequest]", true},
		{"reason", "reason: subscribed", true},
		{"reason miss", "reason: [mention, assign]", false},
		{"state", "state: open", true},
		{"state_not", "state_not: closed", true},
		{"assignee me", "assignee: me", true},
		{"assignee login", "assignee: hubot", false},
		{"author login", "author: hubot", true},
		{"author me", "author: me", false},
		{"review requested me", "review_requested: me", true},
		{"review requested team", "review_requested_team: my_teams", true},
		{"review requested unconfigured team", "review_requested_team: github/other", false},
		{"mentions user", "mentions_user: me\nsearch: [body]", true},
		{"mentions team", "mentions_team: my_teams\nsearch: [body]", true},
		{"mentions unknown team", "mentions_team: github/other\nsearch: [body]", false},
		{"not", "not:\n  reason: subscribed", false},
		{"any", "any:\n  - reason: mention\n  - state: open", true},
		{"all", "all:\n  - reason: mention\n  - state: open", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node, err := decode("when:\n  " + strings.ReplaceAll(tt.condition, "\n", "\n  "))
			if err != nil {
				t.Fatalf("decode error = %v", err)
			}
			matched, err := node.Match(newTestEvidence(item, subject, nil))
			if err != nil || matched != tt.want {
				t.Fatalf("matched=%v err=%v want=%v", matched, err, tt.want)
			}
			if node.Describe() == "" {
				t.Fatal("predicate did not describe itself")
			}
		})
	}
}

func TestPredicatesThatOnlyApplyToSomeSubjectTypes(t *testing.T) {
	subject := model.Resource{State: "open", Assignees: []model.User{{Login: "octocat"}}, RequestedReviewers: []model.User{{Login: "octocat"}}, RequestedTeams: []model.Team{{Slug: "notifications"}}}
	for _, tt := range []struct{ name, condition, subjectType string }{
		{"assignee", "assignee: me", "Discussion"},
		{"review requested", "review_requested: me", "Issue"},
		{"review requested team", "review_requested_team: my_teams", "Issue"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			node, err := decode("when:\n  " + tt.condition)
			if err != nil {
				t.Fatal(err)
			}
			matched, err := node.Match(newTestEvidence(notification("github/repo", tt.subjectType, "subscribed"), subject, nil))
			if matched || err != nil {
				t.Fatalf("matched=%v err=%v", matched, err)
			}
		})
	}
}

func TestReasonShortCircuitsAvoidFetchingEvidence(t *testing.T) {
	for _, tt := range []struct{ name, condition, reason string }{
		{"assign", "assignee: me", "assign"},
		{"author", "author: me", "author"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			node, err := decode("when:\n  " + tt.condition)
			if err != nil {
				t.Fatal(err)
			}
			evidence := NewEvidence(notification("github/repo", "Issue", tt.reason), testIdentity, nil, nil)
			matched, err := node.Match(evidence)
			if !matched || err != nil || evidence.SubjectFetched() {
				t.Fatalf("matched=%v err=%v fetched=%v", matched, err, evidence.SubjectFetched())
			}
		})
	}
}

func TestStateIsIndeterminateWhenUnavailable(t *testing.T) {
	node, err := decode("when:\n  state: open")
	if err != nil {
		t.Fatal(err)
	}
	matched, err := node.Match(newTestEvidence(notification("github/repo", "PullRequest", "subscribed"), model.Resource{}, nil))
	if matched || err == nil || !strings.Contains(err.Error(), "unavailable or unsupported") {
		t.Fatalf("matched=%v err=%v", matched, err)
	}
}

func TestDiscussionLockedStateReflectsStateReason(t *testing.T) {
	for _, tt := range []struct {
		name        string
		stateReason *string
		condition   string
		want        bool
	}{
		{"locked and never closed is open", nil, "state: open", true},
		{"locked and closed is closed", stringPointer("resolved"), "state: closed", true},
		{"locked and closed is not open", stringPointer("resolved"), "state: open", false},
		{"locked matches locked", nil, "state: locked", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			node, err := decode("when:\n  " + tt.condition)
			if err != nil {
				t.Fatal(err)
			}
			subject := model.Resource{State: "locked", StateReason: tt.stateReason}
			matched, err := node.Match(newTestEvidence(notification("github/repo", "Discussion", "subscribed"), subject, nil))
			if err != nil || matched != tt.want {
				t.Fatalf("matched=%v err=%v want=%v", matched, err, tt.want)
			}
		})
	}
}

func TestEvidenceIsFetchedAtMostOnceAndFailuresPropagate(t *testing.T) {
	node, err := decode("when:\n  all:\n    - state: open\n    - author: hubot\n    - mentions_user: me")
	if err != nil {
		t.Fatal(err)
	}
	subjectCalls, commentCalls := 0, 0
	evidence := NewEvidence(notification("github/repo", "Discussion", "subscribed"), testIdentity,
		func() (model.Resource, error) {
			subjectCalls++
			return model.Resource{State: "open", Author: model.User{Login: "hubot"}}, nil
		},
		func() ([]model.Resource, error) {
			commentCalls++
			return []model.Resource{{Body: "hello @octocat"}}, nil
		})
	matched, err := node.Match(evidence)
	if !matched || err != nil || subjectCalls != 1 || commentCalls != 1 {
		t.Fatalf("matched=%v err=%v subject=%d comments=%d", matched, err, subjectCalls, commentCalls)
	}

	failure := errors.New("comments unavailable")
	failing := NewEvidence(notification("github/repo", "Discussion", "subscribed"), testIdentity,
		func() (model.Resource, error) { return model.Resource{}, nil },
		func() ([]model.Resource, error) { return nil, failure })
	commentsNode, err := decode("when:\n  mentions_team: my_teams\n  search: [comments]")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commentsNode.Match(failing); !errors.Is(err, failure) {
		t.Fatalf("err=%v want %v", err, failure)
	}

	// any reports a failure only when no branch could match conclusively.
	anyNode, err := decode("when:\n  any:\n    - mentions_team: my_teams\n      search: [comments]\n    - reason: subscribed")
	if err != nil {
		t.Fatal(err)
	}
	matched, err = anyNode.Match(NewEvidence(notification("github/repo", "Discussion", "subscribed"), testIdentity,
		func() (model.Resource, error) { return model.Resource{}, nil },
		func() ([]model.Resource, error) { return nil, failure }))
	if !matched || err != nil {
		t.Fatalf("matched=%v err=%v", matched, err)
	}
}

func TestAgeComparesAgainstTheNotificationUpdateTime(t *testing.T) {
	original := now
	now = func() time.Time { return time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = original })

	for _, tt := range []struct {
		name      string
		condition string
		updatedAt string
		want      bool
		wantErr   bool
	}{
		{"older than days", "age:\n  older_than: 30d", "2024-01-01T00:00:00Z", true, false},
		{"not older than days", "age:\n  older_than: 30d", "2024-04-30T00:00:00Z", false, false},
		{"newer than hours", "age:\n  newer_than: 12h", "2024-04-30T18:00:00Z", true, false},
		{"not newer than hours", "age:\n  newer_than: 12h", "2024-01-01T00:00:00Z", false, false},
		{"between", "age:\n  older_than: 7d\n  newer_than: 30d", "2024-04-05T00:00:00Z", true, false},
		{"unparsable update time", "age:\n  older_than: 7d", "", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			node, err := decode("when:\n  " + strings.ReplaceAll(tt.condition, "\n", "\n  "))
			if err != nil {
				t.Fatal(err)
			}
			item := notification("github/repo", "Issue", "subscribed")
			item.UpdatedAt = tt.updatedAt
			matched, err := node.Match(newTestEvidence(item, model.Resource{}, nil))
			if matched != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("matched=%v err=%v", matched, err)
			}
			if node.Describe() == "" {
				t.Fatal("age predicate did not describe itself")
			}
		})
	}
}

var testIdentity = Identity{User: "octocat", Organization: "github", Teams: []string{"github/notifications"}}

func TestStatePredicateSupportsPullRequestAndDiscussionSubStates(t *testing.T) {
	answered := "2024-01-01T00:00:00Z"
	for _, tt := range []struct {
		name        string
		condition   string
		subjectType string
		subject     model.Resource
		want        bool
	}{
		{"merged pull request", "state: merged", "PullRequest", model.Resource{State: "closed", Merged: true}, true},
		{"closed but unmerged", "state: merged", "PullRequest", model.Resource{State: "closed"}, false},
		{"merged only matches pull requests", "state: merged", "Issue", model.Resource{State: "closed", Merged: true}, false},
		{"draft pull request", "state: draft", "PullRequest", model.Resource{State: "open", Draft: true}, true},
		{"draft excludes closed", "state: draft", "PullRequest", model.Resource{State: "closed", Draft: true}, false},
		{"answered discussion", "state: answered", "Discussion", model.Resource{State: "open", AnswerChosenAt: &answered}, true},
		{"unanswered discussion", "state: answered", "Discussion", model.Resource{State: "open"}, false},
		{"state_not merged keeps open work", "state_not: merged", "PullRequest", model.Resource{State: "open"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			node, err := decode("when:\n  " + tt.condition)
			if err != nil {
				t.Fatal(err)
			}
			matched, err := node.Match(newTestEvidence(notification("github/repo", tt.subjectType, "subscribed"), tt.subject, nil))
			if err != nil || matched != tt.want {
				t.Fatalf("matched=%v err=%v want=%v", matched, err, tt.want)
			}
		})
	}
}

func TestSubjectValueTracksTheLastFetchedSubject(t *testing.T) {
	evidence := NewEvidence(notification("github/repo", "Issue", "subscribed"), testIdentity,
		func() (model.Resource, error) { return model.Resource{HTMLURL: "https://example.test/1"}, nil }, nil)
	if evidence.SubjectFetched() || evidence.SubjectValue().HTMLURL != "" {
		t.Fatalf("subject value populated before any fetch: %#v", evidence.SubjectValue())
	}
	if _, err := evidence.Subject(); err != nil {
		t.Fatal(err)
	}
	if !evidence.SubjectFetched() || evidence.SubjectValue().HTMLURL != "https://example.test/1" {
		t.Fatalf("subject value did not track the fetched subject: %#v", evidence.SubjectValue())
	}
}

func TestDecodeRejectsRecursiveAliasesWithoutCrashing(t *testing.T) {
	for _, tt := range []struct{ name, document string }{
		{"direct self alias", "when: &c {not: *c}"},
		{"indirect alias cycle", "when: &c\n  all:\n    - not: *c"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decode(tt.document)
			if err == nil || !strings.Contains(err.Error(), "recursive YAML alias") {
				t.Fatalf("decode error = %v, want recursive alias rejection", err)
			}
		})
	}
}

func TestDecodeRejectsExcessiveNesting(t *testing.T) {
	document := "when:\n"
	indent := "  "
	for i := 0; i < 200; i++ {
		document += indent + "not:\n"
		indent += "  "
	}
	document += indent + "reason: mention\n"
	if _, err := decode(document); err == nil || !strings.Contains(err.Error(), "nested too deeply") {
		t.Fatalf("decode error = %v, want depth-limit rejection", err)
	}
}

func TestAllReportsErrorOnlyWhenNoChildConclusivelyFails(t *testing.T) {
	// A conclusive non-match makes the conjunction false regardless of order,
	// even when a sibling predicate cannot fetch its evidence.
	for _, condition := range []string{
		"all:\n  - state: open\n  - reason: mention",
		"all:\n  - reason: mention\n  - state: open",
	} {
		node, err := decode("when:\n  " + strings.ReplaceAll(condition, "\n", "\n  "))
		if err != nil {
			t.Fatalf("decode error = %v", err)
		}
		evidence := NewEvidence(notification("github/repo", "Issue", "subscribed"), testIdentity,
			func() (model.Resource, error) { return model.Resource{}, errors.New("state unavailable") }, nil)
		matched, err := node.Match(evidence)
		if matched || err != nil {
			t.Fatalf("condition %q: matched=%v err=%v, want conclusive non-match", condition, matched, err)
		}
	}

	// With no conclusive non-match, an indeterminate child surfaces the error.
	node, err := decode("when:\n  all:\n    - reason: subscribed\n    - state: open")
	if err != nil {
		t.Fatalf("decode error = %v", err)
	}
	evidence := NewEvidence(notification("github/repo", "Issue", "subscribed"), testIdentity,
		func() (model.Resource, error) { return model.Resource{}, errors.New("state unavailable") }, nil)
	if _, err := node.Match(evidence); err == nil {
		t.Fatal("expected indeterminate error when no child conclusively fails")
	}
}

func TestMentionsTeamIsScopedToNotificationOwner(t *testing.T) {
	node, err := decode("when:\n  mentions_team: my_teams\n  search: [body]")
	if err != nil {
		t.Fatal(err)
	}
	subject := model.Resource{Body: "cc @github/notifications"}
	// Same team slug, but the notification lives in another organization.
	matched, err := node.Match(newTestEvidence(notification("other/repo", "Discussion", "subscribed"), subject, nil))
	if matched || err != nil {
		t.Fatalf("cross-organization mention matched=%v err=%v", matched, err)
	}
	matched, err = node.Match(newTestEvidence(notification("github/repo", "Discussion", "subscribed"), subject, nil))
	if !matched || err != nil {
		t.Fatalf("same-organization mention matched=%v err=%v", matched, err)
	}
}

func TestAgeThresholdsAreExclusiveAtTheBoundary(t *testing.T) {
	original := now
	now = func() time.Time { return time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = original })
	node, err := decode("when:\n  age:\n    older_than: 30d")
	if err != nil {
		t.Fatal(err)
	}
	item := notification("github/repo", "Issue", "subscribed")
	item.UpdatedAt = time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	matched, err := node.Match(newTestEvidence(item, model.Resource{}, nil))
	if matched || err != nil {
		t.Fatalf("exactly 30d old matched=%v err=%v, want exclusive boundary", matched, err)
	}
}

// decode parses a single-key document whose "when" value is the condition under
// test, exercising the same path the configuration loader uses.
func decode(document string) (Node, error) {
	var wrapper struct {
		When Node `yaml:"when"`
	}
	if err := yaml.Unmarshal([]byte(document), &wrapper); err != nil {
		return Node{}, err
	}
	return wrapper.When, nil
}

func newTestEvidence(item model.Notification, subject model.Resource, comments []model.Resource) *Evidence {
	return NewEvidence(item, testIdentity,
		func() (model.Resource, error) { return subject, nil },
		func() ([]model.Resource, error) { return comments, nil })
}

func notification(repository, subjectType, reason string) model.Notification {
	return model.Notification{Reason: reason, Repository: model.Repository{FullName: repository}, Subject: model.Subject{Type: subjectType, URL: "https://api.github.test/subject"}}
}

func stringPointer(value string) *string { return &value }
