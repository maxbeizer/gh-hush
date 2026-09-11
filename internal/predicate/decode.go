package predicate

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// maxConditionDepth bounds predicate-tree nesting so adversarial or accidental
// deep documents cannot exhaust the stack during decoding.
const maxConditionDepth = 64

// maxDurationDays is the largest whole-day duration representable as a
// time.Duration without overflowing its int64 nanosecond count.
const maxDurationDays = 106751

// decodeContext carries per-document decode state: the set of mapping nodes on
// the active path (for alias-cycle detection) and the current nesting depth.
type decodeContext struct {
	active map[*yaml.Node]bool
	depth  int
}

// UnmarshalYAML decodes a predicate node, preserving document order so that
// cheap predicates can be authored before evidence-fetching ones and evaluated
// first. The zero node (an omitted mapping) matches unconditionally.
func (n *Node) UnmarshalYAML(value *yaml.Node) error {
	pred, err := decodeNode(value, &decodeContext{active: map[*yaml.Node]bool{}})
	if err != nil {
		return err
	}
	n.pred = pred
	return nil
}

// MarshalYAML is intentionally unsupported: predicate trees are authored in
// YAML and never re-encoded, so a decoded Node round-trips only through the
// original document text.
func (n Node) MarshalYAML() (any, error) {
	return nil, fmt.Errorf("predicate nodes are read-only and cannot be marshaled")
}

func decodeNode(value *yaml.Node, ctx *decodeContext) (Predicate, error) {
	if value == nil {
		return nil, nil
	}
	if value.Kind == yaml.AliasNode {
		if value.Alias == nil || ctx.active[value.Alias] {
			return nil, fmt.Errorf("a rule condition contains a recursive YAML alias")
		}
		return decodeNode(value.Alias, ctx)
	}
	if value.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("a rule condition must be a mapping, got %s", kindName(value.Kind))
	}
	if ctx.active[value] {
		return nil, fmt.Errorf("a rule condition contains a recursive YAML alias")
	}
	if ctx.depth >= maxConditionDepth {
		return nil, fmt.Errorf("a rule condition is nested too deeply (limit %d)", maxConditionDepth)
	}
	ctx.active[value] = true
	ctx.depth++
	defer func() {
		delete(ctx.active, value)
		ctx.depth--
	}()
	preds, err := decodeMapping(value, ctx)
	if err != nil {
		return nil, err
	}
	if len(preds) == 1 {
		return preds[0], nil
	}
	return allPredicate{preds: preds}, nil
}

// decodeMapping decodes each key of a mapping into a predicate, preserving key
// order. Sibling keys form an implicit "all". The "search" key is a modifier
// consumed by the mentions predicates and is never a standalone predicate.
func decodeMapping(value *yaml.Node, ctx *decodeContext) ([]Predicate, error) {
	search, hasSearch, err := extractSearch(value)
	if err != nil {
		return nil, err
	}
	var preds []Predicate
	mentions := 0
	for i := 0; i < len(value.Content); i += 2 {
		key := value.Content[i].Value
		child := value.Content[i+1]
		if key == "search" {
			continue
		}
		if key == "mentions_user" || key == "mentions_team" {
			mentions++
		}
		pred, err := decodeKey(key, child, search, ctx)
		if err != nil {
			return nil, err
		}
		preds = append(preds, pred)
	}
	if len(preds) == 0 {
		return nil, fmt.Errorf("a rule condition must contain at least one predicate")
	}
	if hasSearch && mentions != 1 {
		return nil, fmt.Errorf("search requires exactly one sibling mentions_user or mentions_team predicate")
	}
	return preds, nil
}

func extractSearch(value *yaml.Node) ([]string, bool, error) {
	for i := 0; i < len(value.Content); i += 2 {
		if value.Content[i].Value != "search" {
			continue
		}
		scope, err := decodeStringList(value.Content[i+1])
		if err != nil {
			return nil, true, fmt.Errorf("search: %w", err)
		}
		for _, entry := range scope {
			if entry != "body" && entry != "comments" {
				return nil, true, fmt.Errorf("search scope %q must be body or comments", entry)
			}
		}
		return scope, true, nil
	}
	return nil, false, nil
}

func decodeKey(key string, child *yaml.Node, search []string, ctx *decodeContext) (Predicate, error) {
	switch key {
	case "all":
		preds, err := decodeSequence(child, ctx)
		if err != nil {
			return nil, fmt.Errorf("all: %w", err)
		}
		return allPredicate{preds: preds}, nil
	case "any":
		preds, err := decodeSequence(child, ctx)
		if err != nil {
			return nil, fmt.Errorf("any: %w", err)
		}
		return anyPredicate{preds: preds}, nil
	case "not":
		pred, err := decodeNode(child, ctx)
		if err != nil {
			return nil, fmt.Errorf("not: %w", err)
		}
		return notPredicate{pred: pred}, nil
	case "repository":
		return decodeRepository(child)
	case "subject_type":
		types, err := decodeStringList(child)
		if err != nil {
			return nil, fmt.Errorf("subject_type: %w", err)
		}
		return subjectTypePredicate{types: types}, nil
	case "reason":
		reasons, err := decodeStringList(child)
		if err != nil {
			return nil, fmt.Errorf("reason: %w", err)
		}
		return reasonPredicate{reasons: reasons}, nil
	case "state":
		state, err := decodeState(child)
		if err != nil {
			return nil, fmt.Errorf("state: %w", err)
		}
		return statePredicate{state: state}, nil
	case "state_not":
		state, err := decodeState(child)
		if err != nil {
			return nil, fmt.Errorf("state_not: %w", err)
		}
		return statePredicate{state: state, negate: true}, nil
	case "assignee":
		target, err := decodeString(child)
		if err != nil {
			return nil, fmt.Errorf("assignee: %w", err)
		}
		return assigneePredicate{target: target}, nil
	case "author":
		target, err := decodeString(child)
		if err != nil {
			return nil, fmt.Errorf("author: %w", err)
		}
		return authorPredicate{target: target}, nil
	case "review_requested":
		target, err := decodeString(child)
		if err != nil {
			return nil, fmt.Errorf("review_requested: %w", err)
		}
		return reviewRequestedPredicate{target: target}, nil
	case "review_requested_team":
		target, err := decodeString(child)
		if err != nil {
			return nil, fmt.Errorf("review_requested_team: %w", err)
		}
		return reviewRequestedTeamPredicate{target: target}, nil
	case "mentions_user":
		target, err := decodeString(child)
		if err != nil {
			return nil, fmt.Errorf("mentions_user: %w", err)
		}
		return mentionsPredicate{target: target, team: false, scope: defaultScope(search)}, nil
	case "mentions_team":
		target, err := decodeString(child)
		if err != nil {
			return nil, fmt.Errorf("mentions_team: %w", err)
		}
		return mentionsPredicate{target: target, team: true, scope: defaultScope(search)}, nil
	case "age":
		return decodeAge(child)
	default:
		return nil, fmt.Errorf("unknown predicate %q", key)
	}
}

func defaultScope(search []string) []string {
	if len(search) > 0 {
		return search
	}
	return []string{"body", "comments"}
}

func decodeSequence(value *yaml.Node, ctx *decodeContext) ([]Predicate, error) {
	if value.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("must be a list of conditions, got %s", kindName(value.Kind))
	}
	preds := make([]Predicate, 0, len(value.Content))
	for _, item := range value.Content {
		pred, err := decodeNode(item, ctx)
		if err != nil {
			return nil, err
		}
		preds = append(preds, pred)
	}
	if len(preds) == 0 {
		return nil, fmt.Errorf("must contain at least one condition")
	}
	return preds, nil
}

func decodeRepository(value *yaml.Node) (Predicate, error) {
	// A scalar or list is shorthand for repository.any_of.
	if value.Kind == yaml.ScalarNode || value.Kind == yaml.SequenceNode {
		anyOf, err := decodeStringList(value)
		if err != nil {
			return nil, fmt.Errorf("repository: %w", err)
		}
		return repositoryPredicate{anyOf: anyOf}, nil
	}
	if value.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("repository must be a mapping, got %s", kindName(value.Kind))
	}
	var pred repositoryPredicate
	for i := 0; i < len(value.Content); i += 2 {
		key := value.Content[i].Value
		child := value.Content[i+1]
		switch key {
		case "owner":
			owner, err := decodeString(child)
			if err != nil {
				return nil, fmt.Errorf("repository.owner: %w", err)
			}
			pred.owner = owner
		case "owner_not":
			owner, err := decodeString(child)
			if err != nil {
				return nil, fmt.Errorf("repository.owner_not: %w", err)
			}
			pred.ownerNot = owner
		case "any_of":
			anyOf, err := decodeStringList(child)
			if err != nil {
				return nil, fmt.Errorf("repository.any_of: %w", err)
			}
			pred.anyOf = anyOf
		default:
			return nil, fmt.Errorf("unknown repository field %q", key)
		}
	}
	if pred.owner == "" && pred.ownerNot == "" && len(pred.anyOf) == 0 {
		return nil, fmt.Errorf("repository must set owner, owner_not, or any_of")
	}
	return pred, nil
}

func decodeAge(value *yaml.Node) (Predicate, error) {
	if value.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("age must be a mapping, got %s", kindName(value.Kind))
	}
	var pred agePredicate
	for i := 0; i < len(value.Content); i += 2 {
		key := value.Content[i].Value
		raw, err := decodeString(value.Content[i+1])
		if err != nil {
			return nil, fmt.Errorf("age.%s: %w", key, err)
		}
		duration, err := parseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("age.%s: %w", key, err)
		}
		switch key {
		case "older_than":
			pred.olderThan = duration
		case "newer_than":
			pred.newerThan = duration
		default:
			return nil, fmt.Errorf("unknown age field %q", key)
		}
	}
	if pred.olderThan == 0 && pred.newerThan == 0 {
		return nil, fmt.Errorf("age must set older_than or newer_than")
	}
	return pred, nil
}

// parseDuration accepts Go durations plus a day suffix, e.g. 30d or 12h.
func parseDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err != nil || days < 0 {
			return 0, fmt.Errorf("invalid duration %q", raw)
		}
		if days > maxDurationDays {
			return 0, fmt.Errorf("duration %q is too large", raw)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid duration %q", raw)
	}
	return d, nil
}

// decodeState decodes a state scalar and rejects values outside the supported
// open/closed/locked vocabulary so runtime and schema agree.
func decodeState(value *yaml.Node) (string, error) {
	state, err := decodeString(value)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(state) {
	case "open", "closed", "locked", "merged", "draft", "answered":
		return state, nil
	default:
		return "", fmt.Errorf("must be open, closed, locked, merged, draft, or answered")
	}
}

func decodeString(value *yaml.Node) (string, error) {
	if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
		return "", fmt.Errorf("must be a single value, got %s", kindName(value.Kind))
	}
	if value.Value == "" {
		return "", fmt.Errorf("must not be empty")
	}
	return value.Value, nil
}

func decodeStringList(value *yaml.Node) ([]string, error) {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Tag != "!!str" || value.Value == "" {
			return nil, fmt.Errorf("must not be empty")
		}
		return []string{value.Value}, nil
	case yaml.SequenceNode:
		if len(value.Content) == 0 {
			return nil, fmt.Errorf("must not be empty")
		}
		values := make([]string, 0, len(value.Content))
		for _, item := range value.Content {
			if item.Kind != yaml.ScalarNode || item.Tag != "!!str" || item.Value == "" {
				return nil, fmt.Errorf("list entries must be non-empty values")
			}
			values = append(values, item.Value)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("must be a value or a list, got %s", kindName(value.Kind))
	}
}

func kindName(kind yaml.Kind) string {
	switch kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	case yaml.ScalarNode:
		return "a value"
	default:
		return "an unsupported node"
	}
}
