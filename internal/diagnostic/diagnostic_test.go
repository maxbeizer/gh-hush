package diagnostic

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestLoggingIsDefaultOffAndQuotesControlCharacters(t *testing.T) {
	var output strings.Builder
	Log(context.Background(), "ignored", String("value", "secret"))
	if output.Len() != 0 {
		t.Fatalf("default-off output=%q", output.String())
	}

	ctx := WithLogger(context.Background(), New(&output))
	ctx = WithPhase(ctx, "classification")
	ctx = WithThread(ctx, "thread\n123")
	Log(ctx, "worker complete", String("value", "line one\nline two"))
	got := output.String()
	if strings.Count(got, "\n") != 1 || !strings.Contains(got, `thread_id="thread\n123"`) || !strings.Contains(got, `value="line one\nline two"`) {
		t.Fatalf("log record was not one safely encoded line: %q", got)
	}
}

func TestConcurrentRecordsDoNotInterleave(t *testing.T) {
	var output strings.Builder
	ctx := WithLogger(context.Background(), New(&output))
	const workers = 32
	var group sync.WaitGroup
	group.Add(workers)
	for index := range workers {
		go func() {
			defer group.Done()
			Log(ctx, "worker", Int("index", index), String("marker", strings.Repeat("x", 128)))
		}()
	}
	group.Wait()
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != workers {
		t.Fatalf("lines=%d want=%d output=%q", len(lines), workers, output.String())
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "debug event=worker index=") || !strings.HasSuffix(line, " marker="+strings.Repeat("x", 128)) {
			t.Fatalf("interleaved line=%q", line)
		}
	}
}
