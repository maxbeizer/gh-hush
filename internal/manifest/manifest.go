package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/maxbeizer/gh-hush/internal/model"
)

const schemaVersion = 1

// Manifest is a review-required snapshot of proposed unsubscribe operations.
type Manifest struct {
	SchemaVersion     int      `json:"schema_version"`
	GeneratedAt       string   `json:"generated_at"`
	AuthenticatedUser string   `json:"authenticated_user"`
	ConfigSHA256      string   `json:"config_sha256"`
	Reviewed          bool     `json:"reviewed"`
	Actions           []Action `json:"actions"`
}

// Action is one future mutation target selected during a dry run.
type Action struct {
	ThreadID           string       `json:"thread_id"`
	URL                string       `json:"url"`
	Repository         string       `json:"repository"`
	SubjectType        string       `json:"subject_type"`
	NotificationReason string       `json:"notification_reason"`
	Action             model.Action `json:"action"`
	MatchingRules      []model.Rule `json:"matching_rules"`
}

// Build creates a manifest containing only explicit unsubscribe decisions.
func Build(user, configHash string, decisions []model.Decision) Manifest {
	result := Manifest{
		SchemaVersion:     schemaVersion,
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
		AuthenticatedUser: user,
		ConfigSHA256:      configHash,
		Reviewed:          false,
		Actions:           make([]Action, 0),
	}
	for _, decision := range decisions {
		if decision.Action != model.ActionUnsubscribe {
			continue
		}
		result.Actions = append(result.Actions, Action{
			ThreadID:           decision.Thread.ID,
			URL:                decision.URL,
			Repository:         decision.Thread.Repository.FullName,
			SubjectType:        decision.Thread.Subject.Type,
			NotificationReason: decision.Thread.Reason,
			Action:             decision.Action,
			MatchingRules:      decision.Rules,
		})
	}
	return result
}

// WriteNew writes a private manifest without overwriting an existing review artifact.
func WriteNew(path string, manifest Manifest) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// ValidateForFutureApply documents invariants an eventual mutation path must enforce.
func ValidateForFutureApply(manifest Manifest, authenticatedUser string) error {
	if manifest.SchemaVersion != schemaVersion {
		return fmt.Errorf("unsupported manifest schema_version %d", manifest.SchemaVersion)
	}
	if !manifest.Reviewed {
		return fmt.Errorf("manifest must be explicitly marked reviewed")
	}
	if manifest.AuthenticatedUser != authenticatedUser {
		return fmt.Errorf("manifest user %q does not match authenticated user %q", manifest.AuthenticatedUser, authenticatedUser)
	}
	for index, action := range manifest.Actions {
		if action.ThreadID == "" || action.Action != model.ActionUnsubscribe {
			return fmt.Errorf("manifest action %d is not an explicit unsubscribe target", index)
		}
	}
	return nil
}
