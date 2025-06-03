package errsink_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/lestrrat-go/httprc/v3/errsink"
)

func TestNop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{
			name: "nil error",
			err:  nil,
		},
		{
			name: "simple error",
			err:  errors.New("test error"),
		},
		{
			name: "wrapped error",
			err:  errors.Join(errors.New("base error"), errors.New("wrapped error")),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sink := errsink.NewNop()

			// Should not panic or do anything
			ctx := context.Background()
			sink.Put(ctx, tt.err)
		})
	}
}

func TestNopZeroValue(t *testing.T) {
	t.Parallel()

	// Test that zero value can be used directly
	var sink errsink.Nop
	ctx := context.Background()
	err := errors.New("test error")

	// Should not panic
	sink.Put(ctx, err)
}

type mockSlogger struct {
	logs []logEntry
}

type logEntry struct {
	ctx   context.Context
	level slog.Level
	msg   string
	args  []any
}

func (m *mockSlogger) Log(ctx context.Context, level slog.Level, msg string, args ...any) {
	m.logs = append(m.logs, logEntry{
		ctx:   ctx,
		level: level,
		msg:   msg,
		args:  args,
	})
}

func TestSlogSink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantMsg  string
		wantArgs int
	}{
		{
			name:     "simple error",
			err:      errors.New("test error"),
			wantMsg:  "test error",
			wantArgs: 0,
		},
		{
			name:     "error with formatting",
			err:      errors.New("error with %d number"),
			wantMsg:  "error with %d number",
			wantArgs: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := &mockSlogger{}
			sink := errsink.NewSlog(logger)

			ctx := context.Background()
			sink.Put(ctx, tt.err)

			if len(logger.logs) != 1 {
				t.Errorf("expected 1 log entry, got %d", len(logger.logs))
				return
			}

			entry := logger.logs[0]

			if entry.ctx != ctx {
				t.Error("context not passed correctly")
			}

			if entry.level != slog.LevelError {
				t.Errorf("expected level %v, got %v", slog.LevelError, entry.level)
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

func TestSlogSinkWithNilError(t *testing.T) {
	t.Parallel()

	logger := &mockSlogger{}
	sink := errsink.NewSlog(logger)

	ctx := context.Background()

	// This should panic because nil error cannot call Error() method
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when putting nil error to slog sink")
		}
	}()

	sink.Put(ctx, nil)
}

func TestSlogSinkMultipleErrors(t *testing.T) {
	t.Parallel()

	logger := &mockSlogger{}
	sink := errsink.NewSlog(logger)

	ctx := context.Background()

	errors := []error{
		errors.New("first error"),
		errors.New("second error"),
		errors.New("third error"),
	}

	for _, err := range errors {
		sink.Put(ctx, err)
	}

	if len(logger.logs) != len(errors) {
		t.Errorf("expected %d log entries, got %d", len(errors), len(logger.logs))
		return
	}

	for i, err := range errors {
		if logger.logs[i].msg != err.Error() {
			t.Errorf("log entry %d: expected message %q, got %q", i, err.Error(), logger.logs[i].msg)
		}
	}
}

func TestInterface(t *testing.T) {
	t.Parallel()

	// Ensure types implement the interface
	var _ errsink.Interface = (*errsink.Nop)(nil)
	var _ errsink.Interface = errsink.NewNop()
	var _ errsink.Interface = errsink.NewSlog(&mockSlogger{})
}
