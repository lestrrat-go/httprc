package httprc_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/httprc"
	"github.com/stretchr/testify/assert"
)

type dummyErrSink struct {
	mu     sync.RWMutex
	errors []error
}

func (d *dummyErrSink) Error(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.errors = append(d.errors, err)
}

func (d *dummyErrSink) getErrors() []error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.errors
}

func TestCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var muCalled sync.Mutex
	var called int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		muCalled.Lock()
		called++
		muCalled.Unlock()
		w.Header().Set(`Cache-Control`, fmt.Sprintf(`max-age=%d`, 3))
		w.WriteHeader(http.StatusOK)
	}))

	errSink := &dummyErrSink{}
	c := httprc.NewCache(ctx,
		httprc.WithRefreshWindow(time.Second),
		httprc.WithErrSink(errSink),
	)

	c.Register(srv.URL, httprc.WithHTTPClient(srv.Client()), httprc.WithMinRefreshInterval(time.Second))
	if !assert.True(t, c.IsRegistered(srv.URL)) {
		return
	}

	for i := 0; i < 3; i++ {
		v, err := c.Get(ctx, srv.URL)
		if !assert.NoError(t, err, `c.Get should succeed`) {
			return
		}
		if !assert.IsType(t, []byte(nil), v, `c.Get should return []byte`) {
			return
		}
	}
	muCalled.Lock()
	if !assert.Equal(t, 1, called, `there should only be one fetch request`) {
		return
	}
	muCalled.Unlock()

	time.Sleep(4 * time.Second)
	for i := 0; i < 3; i++ {
		_, err := c.Get(ctx, srv.URL)
		if !assert.NoError(t, err, `c.Get should succeed`) {
			return
		}
	}

	muCalled.Lock()
	if !assert.Equal(t, 2, called, `there should only be one fetch request`) {
		return
	}
	muCalled.Unlock()

	if !assert.True(t, len(errSink.errors) == 0) {
		return
	}

	c.Register(srv.URL,
		httprc.WithHTTPClient(srv.Client()),
		httprc.WithMinRefreshInterval(time.Second),
		httprc.WithTransformer(httprc.TransformFunc(func(_ string, _ *http.Response) (interface{}, error) {
			return nil, fmt.Errorf(`dummy error`)
		})),
	)

	_, _ = c.Get(ctx, srv.URL)
	time.Sleep(3 * time.Second)
	cancel()

	if !assert.True(t, len(errSink.getErrors()) > 0) {
		return
	}
}
