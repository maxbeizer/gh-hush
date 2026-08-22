package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/maxbeizer/gh-hush/internal/model"
)

// Write renders an itemized human-readable preview report.
func Write(w io.Writer, decisions []model.Decision) error {
	if _, err := fmt.Fprintln(w, "gh-hush preview"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "No GitHub mutations were made while generating this preview: subscriptions, read state, and notification settings are unchanged."); err != nil {
		return err
	}
	if len(decisions) == 0 {
		_, err := fmt.Fprintln(w, "\nGitHub returned zero notifications.")
		return err
	}

	keepCount := 0
	unsubscribeCount := 0
	for index, decision := range decisions {
		if decision.Action == model.ActionKeep {
			keepCount++
		} else {
			unsubscribeCount++
		}

		if _, err := fmt.Fprintf(w, "\n%d. [%s] %s\n", index+1, strings.ToUpper(string(decision.Action)), decision.Thread.Subject.Title); err != nil {
			return err
		}
		lines := []struct {
			label string
			value string
		}{
			{"URL", decision.URL},
			{"Subject type", decision.Thread.Subject.Type},
			{"Repository", decision.Thread.Repository.FullName},
			{"Notification reason", decision.Thread.Reason},
			{"Proposed action", string(decision.Action)},
		}
		for _, line := range lines {
			if _, err := fmt.Fprintf(w, "   %s: %s\n", line.label, line.value); err != nil {
				return err
			}
		}
		if decision.EnrichmentError != "" {
			if _, err := fmt.Fprintf(w, "   Enrichment warning: %s\n", decision.EnrichmentError); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w, "   Matching rules:"); err != nil {
			return err
		}
		for _, rule := range decision.Rules {
			if _, err := fmt.Fprintf(w, "   - %s: %s\n", rule.ID, rule.Evidence); err != nil {
				return err
			}
		}
	}

	_, err := fmt.Fprintf(w, "\nSummary: %d keep, %d propose unsubscribe, %d total\n", keepCount, unsubscribeCount, len(decisions))
	return err
}
