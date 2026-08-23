package cmd

import (
	"fmt"
	"time"
)

// now is the clock seam used for phase timings. Tests override it so duration
// output is deterministic without sleeping.
var now = time.Now

// formatDuration renders a concise, human-readable duration with sensible
// precision: whole milliseconds under one second, one decimal of seconds under
// one minute, and minutes with one-decimal seconds above that. Negative inputs
// (which timing seams should never produce) render as 0ms.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		minutes := int(d / time.Minute)
		seconds := (d % time.Minute).Seconds()
		return fmt.Sprintf("%dm%04.1fs", minutes, seconds)
	}
}
