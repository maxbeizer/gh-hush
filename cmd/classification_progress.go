package cmd

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

var classificationSpinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type classificationProgress struct {
	output        io.Writer
	interactive   bool
	total         int
	frame         int
	previousWidth int
}

func newClassificationProgress(output io.Writer, interactive bool) *classificationProgress {
	return &classificationProgress{output: output, interactive: interactive}
}

func (p *classificationProgress) start(total int) {
	p.total = total
	if total == 0 {
		_, _ = fmt.Fprintln(p.output, "No unread notifications to classify.")
		return
	}
	if p.interactive {
		p.replaceLine(p.statusLine(0))
		return
	}
	_, _ = fmt.Fprintln(p.output, p.statusLine(0))
}

func (p *classificationProgress) update(completed int) {
	if p.total == 0 {
		return
	}
	if p.interactive {
		p.frame = (p.frame + 1) % len(classificationSpinnerFrames)
		p.replaceLine(p.statusLine(completed))
		return
	}
	if completed%25 == 0 && completed < p.total {
		_, _ = fmt.Fprintln(p.output, p.statusLine(completed))
	}
}

func (p *classificationProgress) finish() {
	if p.total == 0 {
		return
	}
	line := fmt.Sprintf("✓ Classified %d/%d unread %s (100%%)", p.total, p.total, notificationWord(p.total))
	if p.interactive {
		p.replaceLine(line)
		_, _ = fmt.Fprintln(p.output)
		return
	}
	_, _ = fmt.Fprintln(p.output, line)
}

func (p *classificationProgress) statusLine(completed int) string {
	percentage := completed * 100 / p.total
	prefix := "Classifying"
	if p.interactive {
		prefix = classificationSpinnerFrames[p.frame] + " Classifying"
	}
	return fmt.Sprintf("%s unread %s (read-only)… %d/%d (%d%%)", prefix, notificationWord(p.total), completed, p.total, percentage)
}

func (p *classificationProgress) replaceLine(line string) {
	width := utf8.RuneCountInString(line)
	padding := max(0, p.previousWidth-width)
	_, _ = fmt.Fprintf(p.output, "\r%s%s", line, strings.Repeat(" ", padding))
	p.previousWidth = width
}

func notificationWord(count int) string {
	if count == 1 {
		return "notification"
	}
	return "notifications"
}
