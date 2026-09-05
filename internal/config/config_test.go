package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
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
	Type                 string                  `json:"type"`
	Description          string                  `json:"description"`
	Properties           map[string]schemaObject `json:"properties"`
	Required             []string                `json:"required"`
	AdditionalProperties *bool                   `json:"additionalProperties"`
	Items                *schemaObject           `json:"items"`
}

func assertSchemaMatchesType(t *testing.T, path string, schema schemaObject, typ reflect.Type) {
	t.Helper()
	assertSchemaType(t, path, schema, typ)
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
		assertSchemaType(t, path+"."+name, property, fieldType)
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

func assertSchemaType(t *testing.T, path string, schema schemaObject, typ reflect.Type) {
	t.Helper()
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	var want string
	switch typ.Kind() {
	case reflect.String:
		want = "string"
	case reflect.Bool:
		want = "boolean"
	case reflect.Struct:
		want = "object"
	case reflect.Slice:
		want = "array"
		if schema.Items == nil {
			t.Errorf("%s must define array items", path)
		} else {
			assertSchemaType(t, path+"[]", *schema.Items, typ.Elem())
		}
	default:
		t.Fatalf("%s uses unsupported Go type %s", path, typ)
	}
	if schema.Type != want {
		t.Errorf("%s schema type = %q, want %q for Go type %s", path, schema.Type, want, typ)
	}
}

func TestPublishedSchemaEnforcesRuntimeConstraints(t *testing.T) {
	path := filepath.Join("..", "..", "config.schema.json")
	schema, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatalf("compile config.schema.json: %v", err)
	}
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"valid", validYAML, true},
		{"unknown field", validYAML + "unexpected: true\n", false},
		{"missing required field", strings.Replace(validYAML, "  authored_by_user: true\n", "", 1), false},
		{"hush must be true", strings.Replace(validYAML, "all_other_notifications: true", "all_other_notifications: false", 1), false},
		{"invalid login", strings.Replace(validYAML, "user: octocat", "user: octo--cat", 1), false},
		{"wrong boolean type", strings.Replace(validYAML, "personally_mentioned: true", "personally_mentioned: enabled", 1), false},
		{"malformed team", strings.Replace(validYAML, "github/notifications", "github", 1), false},
		{"duplicate team", strings.Replace(validYAML, "  - github/notifications\n", "  - github/notifications\n  - github/notifications\n", 1), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, runtimeErr := Parse([]byte(tt.input))
			if tt.valid && runtimeErr != nil {
				t.Fatalf("runtime rejected valid config: %v", runtimeErr)
			}
			if !tt.valid && runtimeErr == nil {
				t.Fatal("runtime accepted invalid config")
			}

			var document any
			if err := yaml.Unmarshal([]byte(tt.input), &document); err != nil {
				t.Fatal(err)
			}
			schemaErr := schema.Validate(document)
			if tt.valid && schemaErr != nil {
				t.Fatalf("schema rejected valid config: %v", schemaErr)
			}
			if !tt.valid && schemaErr == nil {
				t.Fatal("schema accepted invalid config")
			}
		})
	}
}

func TestDecodeErrorPreservesAllProblemsAndAddsGuidance(t *testing.T) {
	input := strings.Replace(validYAML, "team_slugs:", "discussion_team_slugs:", 1)
	input = strings.Replace(input, "personally_mentioned: true", "personally_mentioned: enabled", 1)
	input += "unexpected: true\n"

	_, err := Parse([]byte(input))
	if err == nil {
		t.Fatal("expected error")
	}
	message := err.Error()
	for _, want := range []string{
		`"discussion_team_slugs" was renamed to "team_slugs" in v0.2.0`,
		`cannot unmarshal !!str`,
		`unknown configuration field "unexpected"`,
		configSchemaURL,
	} {
		if !strings.Contains(message, want) {
			t.Errorf("error %q does not contain %q", message, want)
		}
	}
	if strings.Contains(message, "type config.Config") {
		t.Errorf("error exposes Go implementation type: %q", message)
	}
}

func TestParseValidationAndHardSchemaBreak(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"valid", validYAML, ""},
		{"v0.2 migration guidance", strings.Replace(validYAML, "team_slugs:", "discussion_team_slugs:", 1), `"discussion_team_slugs" was renamed to "team_slugs" in v0.2.0`},
		{"old unsubscribe rejected", strings.Replace(validYAML, "hush:", "unsubscribe:", 1), `"unsubscribe" was replaced by "hush"`},
		{"run mode removed", validYAML + "run_mode: ad_hoc\n", `"run_mode" is no longer supported`},
		{"output removed", validYAML + "output:\n  default_mode: dry_run\n", `"output" is no longer supported`},
		{"unknown", validYAML + "unexpected: true\n", `unknown configuration field "unexpected"`},
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
