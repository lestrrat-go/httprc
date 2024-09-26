package httprc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/lestrrat-go/httpcc"
)

const ReadBufferSize = 1024 * 1024 * 10  // 10MB
const MaxBufferSize = 1024 * 1024 * 1000 // 1GB

// ResourceBase is a generic Resouce type
type ResourceBase[T any] struct {
	mu          sync.RWMutex
	u           string
	httpcl      HTTPClient
	t           Transformer[T]
	r           T
	next        time.Time
	interval    time.Duration
	minInterval time.Duration
	busy        bool
}

// NewResource creates a new Resource object which after fetching the
// resource from the URL, will transform the response body using the
// provided Transformer to an object of type T.
//
// This function will return an error if the URL is not a valid URL
// (i.e. it cannot be parsed by url.Parse), or if the transformer is nil.
func NewResource[T any](s string, transformer Transformer[T], options ...NewResourceOption) (*ResourceBase[T], error) {
	var httpcl HTTPClient = http.DefaultClient
	var interval time.Duration
	minInterval := 15 * time.Minute
	//nolint:forcetypeassert
	for _, option := range options {
		switch option.Ident() {
		case identHTTPClient{}:
			httpcl = option.Value().(HTTPClient)
		case identMinimumInterval{}:
			minInterval = option.Value().(time.Duration)
		case identConstantInterval{}:
			interval = option.Value().(time.Duration)
		}
	}
	if transformer == nil {
		return nil, fmt.Errorf(`httprc.NewResource: transformer is required`)
	}

	if _, err := url.Parse(s); err != nil {
		return nil, fmt.Errorf(`httprc.NewResource: %w`, err)
	}
	return &ResourceBase[T]{
		u:           s,
		httpcl:      httpcl,
		t:           transformer,
		next:        time.Unix(0, 0), // initially, it should be fetched immediately
		interval:    interval,
		minInterval: minInterval,
	}, nil
}

// URL returns the URL of the resource.
func (r *ResourceBase[T]) URL() string {
	return r.u
}

// Resource returns the last fetched resource. If the resource has not been
// fetched yet, this will return the zero value of type T.
func (r *ResourceBase[T]) Resource() T {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.r
}

func (r *ResourceBase[T]) Next() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.next
}

func (r *ResourceBase[T]) ConstantInterval() time.Duration {
	return r.interval
}

func (r *ResourceBase[T]) MinimumInterval() time.Duration {
	return r.minInterval
}

func (r *ResourceBase[T]) SetBusy(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.busy = v
}

func (r *ResourceBase[T]) IsBusy() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.busy
}

// limitedBody is a wrapper around an io.Reader that will only read up to
// MaxBufferSize bytes. This is provided to prevent the user from accidentally
// reading a huge response body into memory
type limitedBody struct {
	rdr   io.Reader
	close func() error
}

func (l *limitedBody) Read(p []byte) (n int, err error) {
	return l.rdr.Read(p)
}

func (l *limitedBody) Close() error {
	return l.close()
}

func (r *ResourceBase[T]) Sync(ctx context.Context) error {
	req, err := http.NewRequest(http.MethodGet, r.u, nil)
	if err != nil {
		return fmt.Errorf(`httprc.Resource.Sync: failed to create request: %w`, err)
	}
	res, err := r.httpcl.Do(req)
	if err != nil {
		return fmt.Errorf(`httprc.Resource.Sync: failed to execute HTTP request: %w`, err)
	}
	defer res.Body.Close()

	r.mu.Lock()
	r.next = calculateNextRefreshTime(res, r.interval, r.minInterval)
	r.mu.Unlock()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf(`httprc.Resource.Sync: unexpected HTTP status code: %d`, res.StatusCode)
	}

	// replace the body of the response with a limited reader that
	// will only read up to MaxBufferSize bytes
	res.Body = &limitedBody{
		rdr:   &io.LimitedReader{R: res.Body, N: MaxBufferSize},
		close: res.Body.Close,
	}
	v, err := r.transform(ctx, res)
	if err != nil {
		return fmt.Errorf(`httprc.Resource.Sync: failed to transform response body: %w`, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.r = v
	return nil
}

func (r *ResourceBase[T]) transform(ctx context.Context, res *http.Response) (ret T, gerr error) {
	// Protect the call to Transform with a defer/recover block, so that even
	// if the Transform method panics, we can recover from it and return an error
	defer func() {
		if recovered := recover(); recovered != nil {
			gerr = fmt.Errorf(`httprc.Resource.transform: recovered from panic: %v`, recovered)
		}
	}()
	return r.t.Transform(ctx, res)
}

func calculateNextRefreshTime(res *http.Response, interval, minInterval time.Duration) time.Time {
	now := time.Now()
	if interval > 0 {
		return now.Add(interval)
	}

	if res != nil {
		if v := res.Header.Get(`Cache-Control`); v != "" {
			dir, err := httpcc.ParseResponse(v)
			if err == nil {
				maxAge, ok := dir.MaxAge()
				if ok {
					resDuration := time.Duration(maxAge) * time.Second
					if resDuration > minInterval {
						return now.Add(resDuration)
					}
					return now.Add(minInterval)
				}
				// fallthrough
			}
			// fallthrough
		}

		if v := res.Header.Get(`Expires`); v != "" {
			expires, err := http.ParseTime(v)
			if err == nil {
				resDuration := time.Until(expires)
				if resDuration > minInterval {
					return now.Add(resDuration)
				}
				return now.Add(minInterval)
			}
			// fallthrough
		}
	}

	// Previous fallthroughs are a little redandunt, but hey, it's all good.
	return now.Add(minInterval)
}
