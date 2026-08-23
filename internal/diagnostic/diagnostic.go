// Package diagnostic provides opt-in, line-oriented debug logging.
package diagnostic

import (
	"context"
	"io"
	"strconv"
	"strings"
	"sync"
)

type contextKey int

const (
	loggerKey contextKey = iota
	phaseKey
	threadKey
)

// Logger serializes complete debug lines. Callers supply only explicitly safe
// metadata; the API intentionally has no facility for headers or bodies.
type Logger struct {
	output io.Writer
	mu     sync.Mutex
}

type Field struct {
	key, value string
}

func New(output io.Writer) *Logger { return &Logger{output: output} }

// Write lets other stderr output share the logger's serialization lock.
func (l *Logger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.output.Write(p)
}

func String(key, value string) Field  { return Field{key: key, value: value} }
func Int(key string, value int) Field { return Field{key: key, value: strconv.Itoa(value)} }
func Bool(key string, value bool) Field {
	return Field{key: key, value: strconv.FormatBool(value)}
}

func WithLogger(ctx context.Context, logger *Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

func WithPhase(ctx context.Context, phase string) context.Context {
	return context.WithValue(ctx, phaseKey, phase)
}

func WithThread(ctx context.Context, threadID string) context.Context {
	return context.WithValue(ctx, threadKey, threadID)
}

func Enabled(ctx context.Context) bool { return From(ctx) != nil }
func From(ctx context.Context) *Logger {
	logger, _ := ctx.Value(loggerKey).(*Logger)
	return logger
}

// Log writes one intact record. Values are quoted when necessary so every
// record remains one physical line even if a value contains control characters.
func Log(ctx context.Context, event string, fields ...Field) {
	logger := From(ctx)
	if logger == nil {
		return
	}
	all := make([]Field, 0, len(fields)+2)
	if phase, _ := ctx.Value(phaseKey).(string); phase != "" {
		all = append(all, String("phase", phase))
	}
	if thread, _ := ctx.Value(threadKey).(string); thread != "" {
		all = append(all, String("thread_id", thread))
	}
	all = append(all, fields...)
	logger.log(event, all)
}

func (l *Logger) log(event string, fields []Field) {
	var line strings.Builder
	line.WriteString("debug event=")
	line.WriteString(encode(event))
	for _, field := range fields {
		if field.key == "" || field.value == "" {
			continue
		}
		line.WriteByte(' ')
		line.WriteString(field.key)
		line.WriteByte('=')
		line.WriteString(encode(field.value))
	}
	line.WriteByte('\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.output, line.String())
}

func encode(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\r\n\"\\=") {
		return value
	}
	return strconv.Quote(value)
}
