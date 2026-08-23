package application

import (
	"strings"
	"testing"
)

func TestProgressInteractiveUpdatesInPlaceAndFinishesCleanly(t *testing.T) {
	var output strings.Builder
	progress := newProgress(&output, true)
	progress.start(2)
	progress.update(1)
	progress.update(2)
	progress.finish(2, false)

	got := output.String()
	for _, want := range []string{
		"⠋ Applying notification updates (unsubscribe, mark Done, and revalidate)… 0/2 (0%)",
		"⠙ Applying notification updates (unsubscribe, mark Done, and revalidate)… 1/2 (50%)",
		"⠹ Applying notification updates (unsubscribe, mark Done, and revalidate)… 2/2 (100%)",
		"✓ Finished applying 2/2 notification updates (100%)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q: %q", want, got)
		}
	}
	if strings.HasPrefix(got, "\r") || strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
		t.Fatalf("interactive progress must be one clean completed line: %q", got)
	}
}

func TestProgressNonInteractiveIsDeterministicAndLineOriented(t *testing.T) {
	var output strings.Builder
	progress := newProgress(&output, false)
	progress.start(30)
	for completed := 1; completed <= 30; completed++ {
		progress.update(completed)
	}
	progress.finish(30, false)

	want := "Applying notification updates (unsubscribe, mark Done, and revalidate)… 0/30 (0%)\n" +
		"Applying notification updates (unsubscribe, mark Done, and revalidate)… 25/30 (83%)\n" +
		"✓ Finished applying 30/30 notification updates (100%)\n"
	if got := output.String(); got != want {
		t.Fatalf("output mismatch\n got: %q\nwant: %q", got, want)
	}
	if strings.ContainsAny(output.String(), "\r\x1b") {
		t.Fatalf("non-interactive output contains terminal controls: %q", output.String())
	}
}

func TestProgressHandlesEmptySingleAndStopped(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		var output strings.Builder
		progress := newProgress(&output, true)
		progress.start(0)
		progress.finish(0, false)
		if got, want := output.String(), "No notification updates to apply.\n"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("single", func(t *testing.T) {
		var output strings.Builder
		progress := newProgress(&output, false)
		progress.start(1)
		progress.update(1)
		progress.finish(1, false)
		if got := output.String(); !strings.Contains(got, "Applying notification update ") ||
			!strings.Contains(got, "Finished applying 1/1 notification update (100%)") {
			t.Fatalf("singular output is not sensible: %q", got)
		}
	})
	t.Run("stopped", func(t *testing.T) {
		var output strings.Builder
		progress := newProgress(&output, false)
		progress.start(4)
		progress.update(1)
		progress.finish(1, true)
		if got := output.String(); !strings.Contains(got, "Stopped applying after 1/4 notification updates (25%)") {
			t.Fatalf("partial completion is misleading: %q", got)
		}
	})
}
