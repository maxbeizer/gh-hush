package application

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

var spinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type progress struct {
	output        io.Writer
	interactive   bool
	total         int
	frame         int
	previousWidth int
	live          bool
}

func newProgress(output io.Writer, interactive bool) *progress {
	return &progress{output: output, interactive: interactive}
}

func (p *progress) start(total int) {
	p.total = total
	if total == 0 {
		_, _ = fmt.Fprintln(p.output, "No notification updates to apply.")
		return
	}
	p.render(0)
}

func (p *progress) update(completed int) {
	if p.total == 0 {
		return
	}
	if p.interactive {
		p.frame = (p.frame + 1) % len(spinnerFrames)
		p.render(completed)
	} else if completed%25 == 0 && completed < p.total {
		p.render(completed)
	}
}

func (p *progress) finish(completed int, stopped bool) {
	if p.total == 0 {
		return
	}
	var line string
	if !stopped && completed == p.total {
		line = fmt.Sprintf("✓ Finished applying %d/%d %s (100%%)", completed, p.total, updateWord(p.total))
	} else {
		line = fmt.Sprintf("Stopped applying after %d/%d %s (%d%%)", completed, p.total, updateWord(p.total), percentage(completed, p.total))
	}
	if p.interactive {
		p.replaceLine(line)
		_, _ = fmt.Fprintln(p.output)
		p.live = false
	} else {
		_, _ = fmt.Fprintln(p.output, line)
	}
}

func (p *progress) render(completed int) {
	line := fmt.Sprintf("Applying %s (unsubscribe, mark Done, and revalidate)… %d/%d (%d%%)", updateWord(p.total), completed, p.total, percentage(completed, p.total))
	if p.interactive {
		p.replaceLine(spinnerFrames[p.frame] + " " + line)
	} else {
		_, _ = fmt.Fprintln(p.output, line)
	}
}

func (p *progress) replaceLine(line string) {
	width := utf8.RuneCountInString(line)
	padding := max(0, p.previousWidth-width)
	if p.live {
		_, _ = fmt.Fprint(p.output, "\r")
	}
	_, _ = fmt.Fprintf(p.output, "%s%s", line, strings.Repeat(" ", padding))
	p.previousWidth = width
	p.live = true
}

func percentage(completed, total int) int { return completed * 100 / total }

func updateWord(count int) string {
	if count == 1 {
		return "notification update"
	}
	return "notification updates"
}
