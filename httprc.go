package httprc

import (
	"context"
	"net/http"
	"time"
)

// HTTPClient is an interface that abstracts a "net/http".Client, so that
// users can provide their own implementation of the HTTP client, if need be.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Transformer is used to convert the body of an HTTP response into an appropriate
// object of type T.
type Transformer[T any] interface {
	Transform(context.Context, *http.Response) (T, error)
}

// TransformFunc is a function type that implements the Transformer interface.
type TransformFunc[T any] func(context.Context, *http.Response) (T, error)

func (f TransformFunc[T]) Transform(ctx context.Context, res *http.Response) (T, error) {
	return f(ctx, res)
}

// Resource is a single resource that can be retrieved via HTTP, and (possibly) transformed
// into an arbitrary object type. See ResourceBase for a generic implementation.
type Resource interface {
	Get(any) error
	Next() time.Time
	URL() string
	Sync(context.Context) error
	ConstantInterval() time.Duration
	MinimumInterval() time.Duration
	IsBusy() bool
	SetBusy(bool)
	Ready(context.Context) error
}
