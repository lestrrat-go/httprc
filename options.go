package httprc

import (
	"time"

	"github.com/lestrrat-go/option"
)

type NewClientOption interface {
	option.Interface
	newClientOption()
}

type newClientOption struct {
	option.Interface
}

func (newClientOption) newClientOption() {}

type identWorkers struct{}

// WithWorkers specifies the number of concurrent workers to use for the client.
// If n is less than or equal to 0, the client will use a single worker.
func WithWorkers(n int) NewClientOption {
	return newClientOption{option.New(identWorkers{}, n)}
}

type identErrorSink struct{}

// WithErrorSink specifies the error sink to use for the client.
// If not specified, the client will use a NopErrorSink.
func WithErrorSink(sink ErrorSink) NewClientOption {
	return newClientOption{option.New(identErrorSink{}, sink)}
}

type identTraceSink struct{}

// WithTraceSink specifies the trace sink to use for the client.
// If not specified, the client will use a NopTraceSink.
func WithTraceSink(sink TraceSink) NewClientOption {
	return newClientOption{option.New(identTraceSink{}, sink)}
}

type NewResourceOption interface {
	option.Interface
	newResourceOption()
}

type newResourceOption struct {
	option.Interface
}

func (newResourceOption) newResourceOption() {}

type identHTTPClient struct{}

// WithHTTPClient specifies the HTTP client to use for the client.
// If not specified, the client will use http.DefaultClient.
func WithHTTPClient(cl HTTPClient) NewResourceOption {
	return newResourceOption{option.New(identHTTPClient{}, cl)}
}

type identMinimumInterval struct{}

// WithMinimumInterval specifies the minimum interval between fetches.
//
// Normally the interval between fetches is determined by the response's Cache-Control
// or Expires headers, but if the interval calculated by these headers is less than
// the value specified here, the interval will be adjusted to this value.
func WithMinimumInterval(d time.Duration) NewResourceOption {
	return newResourceOption{option.New(identMinimumInterval{}, d)}
}

type identConstantInterval struct{}

// WithConstantInterval specifies the interval between fetches. When you
// specify this option, the client will fetch the resource at the specified
// intervals, regardless of the response's Cache-Control or Expires headers.
//
// By default this option is disabled.
func WithConstantInterval(d time.Duration) NewResourceOption {
	return newResourceOption{option.New(identConstantInterval{}, d)}
}
