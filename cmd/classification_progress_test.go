package cmd

import (
	"strings"
	"testing"
)

func TestListingProgressInteractive(t *testing.T) {
	var output strings.Builder
	progress := newListingProgress(&output, true)
	progress.start()
	progress.update(100)
	progress.update(175)
	progress.finish(true)

	got := output.String()
	for _, text := range []string{
		"⠋ Fetching unread notifications (read-only)… 0 found",
		"⠙ Fetching unread notifications (read-only)… 100 found",
		"⠹ Fetching unread notifications (read-only)… 175 found",
		"✓ Fetched 175 unread notifications",
	} {
		if !strings.Contains(got, text) {
			t.Errorf("output missing %q: %q", text, got)
		}
	}
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("interactive progress must leave exactly one completed line: %q", got)
	}
}

func TestListingProgressNonInteractiveIsThrottled(t *testing.T) {
	var output strings.Builder
	progress := newListingProgress(&output, false)
	progress.start()
	for count := 100; count <= 1200; count += 100 {
		progress.update(count)
	}
	progress.finish(true)

	want := "Fetching unread notifications (read-only)… 0 found\n" +
		"Fetching unread notifications (read-only)… 500 found\n" +
		"Fetching unread notifications (read-only)… 1000 found\n" +
		"✓ Fetched 1200 unread notifications\n"
	if got := output.String(); got != want {
		t.Fatalf("output=%q want=%q", got, want)
	}
}

func TestClassificationProgressInteractiveUpdatesInPlaceAndFinishesCleanly(t *testing.T) {
	var output strings.Builder
	progress := newClassificationProgress(&output, true)

	progress.start(2)
	progress.update(1)
	progress.update(2)
	progress.finish()

	got := output.String()
	for _, want := range []string{
		"⠋ Classifying unread notifications (read-only)… 0/2 (0%)",
		"⠙ Classifying unread notifications (read-only)… 1/2 (50%)",
		"⠹ Classifying unread notifications (read-only)… 2/2 (100%)",
		"✓ Classified 2/2 unread notifications (100%)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q: %q", want, got)
		}
	}
	if strings.HasPrefix(got, "\r") {
		t.Fatalf("first interactive render must not start with a carriage return: %q", got)
	}
	if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
		t.Fatalf("interactive progress must leave exactly one completed line: %q", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("interactive renderer does not need ANSI cursor controls: %q", got)
	}
}

func TestClassificationProgressNonInteractiveIsLineOriented(t *testing.T) {
	var output strings.Builder
	progress := newClassificationProgress(&output, false)

	progress.start(30)
	for completed := 1; completed <= 30; completed++ {
		progress.update(completed)
	}
	progress.finish()

	want := "" +
		"Classifying unread notifications (read-only)… 0/30 (0%)\n" +
		"Classifying unread notifications (read-only)… 25/30 (83%)\n" +
		"✓ Classified 30/30 unread notifications (100%)\n"
	if got := output.String(); got != want {
		t.Fatalf("output mismatch\n got: %q\nwant: %q", got, want)
	}
	if strings.ContainsAny(output.String(), "\r\x1b") {
		t.Fatalf("non-interactive output contains terminal controls: %q", output.String())
	}
}

func TestClassificationProgressHandlesEmptyAndSingleNotification(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		var output strings.Builder
		progress := newClassificationProgress(&output, true)
		progress.start(0)
		progress.finish()
		if got, want := output.String(), "No unread notifications to classify.\n"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("single non-interactive", func(t *testing.T) {
		var output strings.Builder
		progress := newClassificationProgress(&output, false)
		progress.start(1)
		progress.update(1)
		progress.finish()
		if got := output.String(); !strings.Contains(got, "unread notification (read-only)… 0/1 (0%)") ||
			!strings.Contains(got, "Classified 1/1 unread notification (100%)") {
			t.Fatalf("singular output is not sensible: %q", got)
		}
	})
}
