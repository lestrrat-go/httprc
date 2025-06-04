package tracesink

import (
	"context"
	"log/slog"
)

type Interface interface {
	Put(context.Context, string)
}

// Nop is an ErrorSink that does nothing. It does not require
// any initialization, so the zero value can be used.
type Nop struct{}

// NewNop returns a new NopTraceSink object. The constructor
// is provided for consistency.
func NewNop() Interface {
	return Nop{}
}

// Put for NopTraceSink does nothing.
func (Nop) Put(context.Context, string) {}

type slogSink struct {
	level  slog.Level
	logger SlogLogger
}

type SlogLogger interface {
	Log(context.Context, slog.Level, string, ...any)
}

// NewSlog returns a new ErrorSink that logs errors using the provided slog.Logger
func NewSlog(l SlogLogger) Interface {
	return &slogSink{
		level:  slog.LevelInfo,
		logger: l,
	}
}

func (s *slogSink) Put(ctx context.Context, v string) {
	s.logger.Log(ctx, s.level, v)
}

// FuncSink is a TraceSink that calls a function with the trace message.
type FuncSink struct {
	fn func(context.Context, string)
}

// NewFunc returns a new FuncSink that calls the provided function with trace messages.
func NewFunc(fn func(context.Context, string)) Interface {
	return &FuncSink{fn: fn}
}

// Put calls the function with the trace message.
func (f *FuncSink) Put(ctx context.Context, msg string) {
	if f.fn != nil {
		f.fn(ctx, msg)
	}
}
