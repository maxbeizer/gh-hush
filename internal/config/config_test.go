package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const validYAML = `
user: octocat
github_organization: github
team_slugs:
  - github/notifications
keep:
  external_organization_issues: true
  personally_mentioned: true
  personally_assigned: true
  individually_review_requested: true
  active_team_review_requested_pull_requests: true
  authored_by_user: true
  team_mentioned_discussions: true
hush:
  all_other_notifications: true
`

func TestDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	got, err := DefaultPath()
	if err != nil || got != filepath.Join(home, "gh-hush", "config.yml") {
		t.Fatalf("DefaultPath() = %q, %v", got, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if _, err := DefaultPath(); err == nil {
		t.Fatal("expected relative path error")
	}
}

func TestPublishedSchemaMatchesConfigTypes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema schemaObject
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("parse config.schema.json: %v", err)
	}
	assertSchemaMatchesType(t, "config", schema, reflect.TypeOf(Config{}))
}

type schemaObject struct {
	Description          string                  `json:"description"`
	Properties           map[string]schemaObject `json:"properties"`
	Required             []string                `json:"required"`
	AdditionalProperties *bool                   `json:"additionalProperties"`
}

func assertSchemaMatchesType(t *testing.T, path string, schema schemaObject, typ reflect.Type) {
	t.Helper()
	if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Errorf("%s must reject additional properties", path)
	}
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.Split(field.Tag.Get("yaml"), ",")[0]
		fields = append(fields, name)
		property, ok := schema.Properties[name]
		if !ok {
			t.Errorf("%s.%s is missing from config.schema.json", path, name)
			continue
		}
		if property.Description == "" {
			t.Errorf("%s.%s has no schema description", path, name)
		}
		fieldType := field.Type
		if fieldType.Kind() == reflect.Struct {
			assertSchemaMatchesType(t, path+"."+name, property, fieldType)
		}
	}
	sort.Strings(fields)
	schemaFields := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		schemaFields = append(schemaFields, name)
	}
	sort.Strings(schemaFields)
	sort.Strings(schema.Required)
	if !reflect.DeepEqual(schemaFields, fields) {
		t.Errorf("%s schema fields = %v, Go fields = %v", path, schemaFields, fields)
	}
	if !reflect.DeepEqual(schema.Required, fields) {
		t.Errorf("%s required fields = %v, want %v", path, schema.Required, fields)
	}
}

func TestParseValidationAndHardSchemaBreak(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"valid", validYAML, ""},
		{"old unsubscribe rejected", strings.Replace(validYAML, "hush:", "unsubscribe:", 1), "field unsubscribe not found"},
		{"run mode removed", validYAML + "run_mode: ad_hoc\n", "field run_mode not found"},
		{"output removed", validYAML + "output:\n  default_mode: dry_run\n", "field output not found"},
		{"unknown", validYAML + "unexpected: true\n", "field unexpected not found"},
		{"required keep flag", strings.Replace(validYAML, "  authored_by_user: true\n", "", 1), "keep.authored_by_user is required"},
		{"required active team PR flag", strings.Replace(validYAML, "  active_team_review_requested_pull_requests: true\n", "", 1), "keep.active_team_review_requested_pull_requests is required"},
		{"required hush", strings.Replace(validYAML, "  all_other_notifications: true\n", "", 1), "hush.all_other_notifications is required"},
		{"hush must be true", strings.Replace(validYAML, "all_other_notifications: true", "all_other_notifications: false", 1), "hush.all_other_notifications must be true"},
		{"bad user", strings.Replace(validYAML, "user: octocat", "user: octo--cat", 1), "valid GitHub login"},
		{"bad team", strings.Replace(validYAML, "github/notifications", "other/notifications", 1), "must belong"},
		{"duplicate", strings.Replace(validYAML, "  - github/notifications\n", "  - github/notifications\n  - GITHUB/notifications\n", 1), "duplicate"},
		{"multiple documents", validYAML + "---\nuser: other\n", "exactly one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.input))
			if tt.want == "" && err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("Parse() error = %v, want %q", err, tt.want)
			}
		})
	}
}
