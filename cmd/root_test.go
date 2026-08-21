package cmd

import (
	"io"
	"strings"
	"testing"
)

func TestApplyManifestIsUnavailable(t *testing.T) {
	command := NewRootCommand(io.Discard, io.Discard)
	command.SetArgs([]string{"--apply-manifest", "reviewed.json"})

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "intentionally unavailable in v1") {
		t.Fatalf("Execute() error = %v, want unavailable-in-v1 error", err)
	}
}

func TestNoImplicitOperation(t *testing.T) {
	command := NewRootCommand(io.Discard, io.Discard)
	command.SetArgs(nil)

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "no operation selected") {
		t.Fatalf("Execute() error = %v, want explicit operation error", err)
	}
}
