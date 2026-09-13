package cmd

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

var progressSpinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type phaseProgress struct {
	output        io.Writer
	interactive   bool
	emptyMessage  string
	status        func(completed, total int) string
	finished      func(completed, total int, stopped bool) string
	total         int
	frame         int
	previousWidth int
	live          bool
}

func newPhaseProgress(
	output io.Writer,
	interactive bool,
	emptyMessage string,
	status func(completed, total int) string,
	finished func(completed, total int, stopped bool) string,
) *phaseProgress {
	return &phaseProgress{
		output:       output,
		interactive:  interactive,
		emptyMessage: emptyMessage,
		status:       status,
		finished:     finished,
	}
}

func (p *phaseProgress) start(total int) {
	p.total = total
	if total == 0 {
		_, _ = fmt.Fprintln(p.output, p.emptyMessage)
		return
	}
	p.renderStatus(0)
}

func (p *phaseProgress) update(completed int) {
	if p.total == 0 {
		return
	}
	if p.interactive {
		p.frame = (p.frame + 1) % len(progressSpinnerFrames)
		p.renderStatus(completed)
		return
	}
	if completed%25 == 0 && completed < p.total {
		p.renderStatus(completed)
	}
}

// finish replaces an interactive live line before writing its newline. Durable
// diagnostics can therefore be printed afterward without being overwritten.
func (p *phaseProgress) finish(completed int, stopped bool) {
	if p.total == 0 {
		return
	}
	line := p.finished(completed, p.total, stopped)
	if p.interactive {
		p.replaceLine(line)
		_, _ = fmt.Fprintln(p.output)
		p.live = false
		return
	}
	_, _ = fmt.Fprintln(p.output, line)
}

func (p *phaseProgress) renderStatus(completed int) {
	line := p.status(completed, p.total)
	if p.interactive {
		line = progressSpinnerFrames[p.frame] + " " + line
		p.replaceLine(line)
		return
	}
	_, _ = fmt.Fprintln(p.output, line)
}

func (p *phaseProgress) replaceLine(line string) {
	width := utf8.RuneCountInString(line)
	padding := max(0, p.previousWidth-width)
	if p.live {
		_, _ = fmt.Fprint(p.output, "\r")
	}
	_, _ = fmt.Fprintf(p.output, "%s%s", line, strings.Repeat(" ", padding))
	p.previousWidth = width
	p.live = true
}

type listingProgress struct {
	output        io.Writer
	interactive   bool
	count         int
	lastReported  int
	frame         int
	previousWidth int
	live          bool
}

func newListingProgress(output io.Writer, interactive bool) *listingProgress {
	return &listingProgress{output: output, interactive: interactive}
}

func (p *listingProgress) start() { p.render(0) }

func (p *listingProgress) update(count int) {
	p.count = count
	if p.interactive {
		p.frame = (p.frame + 1) % len(progressSpinnerFrames)
		p.render(count)
		return
	}
	// Keep redirected output useful without producing a line for every page of
	// a very large inbox.
	if count-p.lastReported >= 500 {
		p.render(count)
		p.lastReported = count
	}
}

func (p *listingProgress) finish(succeeded bool) {
	if !succeeded {
		if p.interactive && p.live {
			_, _ = fmt.Fprintln(p.output)
			p.live = false
		}
		return
	}
	line := fmt.Sprintf("✓ Fetched %d unread %s", p.count, notificationWord(p.count))
	if p.interactive {
		p.replaceLine(line)
		_, _ = fmt.Fprintln(p.output)
		p.live = false
		return
	}
	_, _ = fmt.Fprintln(p.output, line)
}

func (p *listingProgress) render(count int) {
	line := fmt.Sprintf("Fetching unread notifications (read-only)… %d found", count)
	if p.interactive {
		p.replaceLine(progressSpinnerFrames[p.frame] + " " + line)
		return
	}
	_, _ = fmt.Fprintln(p.output, line)
}

func (p *listingProgress) replaceLine(line string) {
	width := utf8.RuneCountInString(line)
	padding := max(0, p.previousWidth-width)
	if p.live {
		_, _ = fmt.Fprint(p.output, "\r")
	}
	_, _ = fmt.Fprintf(p.output, "%s%s", line, strings.Repeat(" ", padding))
	p.previousWidth = width
	p.live = true
}

type classificationProgress struct {
	phase *phaseProgress
}

func newClassificationProgress(output io.Writer, interactive bool) *classificationProgress {
	return &classificationProgress{phase: newPhaseProgress(
		output,
		interactive,
		"No unread notifications to classify.",
		func(completed, total int) string {
			return fmt.Sprintf("Classifying unread %s (read-only)… %d/%d (%d%%)", notificationWord(total), completed, total, percentage(completed, total))
		},
		func(_, total int, _ bool) string {
			return fmt.Sprintf("✓ Classified %d/%d unread %s (100%%)", total, total, notificationWord(total))
		},
	)}
}

func (p *classificationProgress) start(total int)      { p.phase.start(total) }
func (p *classificationProgress) update(completed int) { p.phase.update(completed) }
func (p *classificationProgress) finish()              { p.phase.finish(p.phase.total, false) }

func percentage(completed, total int) int {
	return completed * 100 / total
}

func notificationWord(count int) string {
	if count == 1 {
		return "notification"
	}
	return "notifications"
}
