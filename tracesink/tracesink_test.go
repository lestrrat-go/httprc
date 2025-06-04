package tracesink_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/lestrrat-go/httprc/v3/tracesink"
)

func TestNop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  string
	}{
		{
			name: "empty string",
			msg:  "",
		},
		{
			name: "simple message",
			msg:  "test trace message",
		},
		{
			name: "multiline message",
			msg:  "line 1\nline 2\nline 3",
		},
		{
			name: "message with special characters",
			msg:  "test with %s formatting and special chars: 日本語",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sink := tracesink.NewNop()

			// Should not panic or do anything
			ctx := context.Background()
			sink.Put(ctx, tt.msg)
		})
	}
}

func TestNopZeroValue(t *testing.T) {
	t.Parallel()

	// Test that zero value can be used directly
	var sink tracesink.Nop
	ctx := context.Background()
	msg := "test trace message"

	// Should not panic
	sink.Put(ctx, msg)
}

type mockSlogger struct {
	logs []logEntry
}

type logEntry struct {
	level slog.Level
	msg   string
	args  []any
}

func (m *mockSlogger) Log(_ context.Context, level slog.Level, msg string, args ...any) {
	m.logs = append(m.logs, logEntry{
		level: level,
		msg:   msg,
		args:  args,
	})
}

func TestSlogSink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		msg      string
		wantMsg  string
		wantArgs int
	}{
		{
			name:     "simple message",
			msg:      "test trace message",
			wantMsg:  "test trace message",
			wantArgs: 0,
		},
		{
			name:     "message with formatting chars",
			msg:      "trace with %d number",
			wantMsg:  "trace with %d number",
			wantArgs: 0,
		},
		{
			name:     "empty message",
			msg:      "",
			wantMsg:  "",
			wantArgs: 0,
		},
		{
			name:     "multiline message",
			msg:      "line 1\nline 2",
			wantMsg:  "line 1\nline 2",
			wantArgs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := &mockSlogger{}
			sink := tracesink.NewSlog(logger)

			ctx := context.Background()
			sink.Put(ctx, tt.msg)

			if len(logger.logs) != 1 {
				t.Errorf("expected 1 log entry, got %d", len(logger.logs))
				return
			}

			entry := logger.logs[0]

			if entry.level != slog.LevelInfo {
				t.Errorf("expected level %v, got %v", slog.LevelInfo, entry.level)
			}

			if entry.msg != tt.wantMsg {
				t.Errorf("expected message %q, got %q", tt.wantMsg, entry.msg)
			}

			if len(entry.args) != tt.wantArgs {
				t.Errorf("expected %d args, got %d", tt.wantArgs, len(entry.args))
			}
		})
	}
}

func TestSlogSinkMultipleMessages(t *testing.T) {
	t.Parallel()

	logger := &mockSlogger{}
	sink := tracesink.NewSlog(logger)

	ctx := context.Background()

	messages := []string{
		"first trace message",
		"second trace message",
		"third trace message",
	}

	for _, msg := range messages {
		sink.Put(ctx, msg)
	}

	if len(logger.logs) != len(messages) {
		t.Errorf("expected %d log entries, got %d", len(messages), len(logger.logs))
		return
	}

	for i, msg := range messages {
		if logger.logs[i].msg != msg {
			t.Errorf("log entry %d: expected message %q, got %q", i, msg, logger.logs[i].msg)
		}

		if logger.logs[i].level != slog.LevelInfo {
			t.Errorf("log entry %d: expected level %v, got %v", i, slog.LevelInfo, logger.logs[i].level)
		}
	}
}

func TestInterface(t *testing.T) {
	t.Parallel()

	// Ensure types implement the interface
	var _ tracesink.Interface = (*tracesink.Nop)(nil)
	var _ tracesink.Interface = tracesink.NewNop()
	var _ tracesink.Interface = tracesink.NewSlog(&mockSlogger{})
}
