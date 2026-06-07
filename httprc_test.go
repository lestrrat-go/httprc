package httprc_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/httprc/v2"
	"github.com/stretchr/testify/require"
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
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	require.True(t, c.IsRegistered(srv.URL))

	for i := 0; i < 3; i++ {
		v, err := c.Get(ctx, srv.URL)
		require.NoError(t, err, `c.Get should succeed`)
		require.IsType(t, []byte(nil), v, `c.Get should return []byte`)
	}
	muCalled.Lock()
	require.Equal(t, 1, called, `there should only be one fetch request`)
	muCalled.Unlock()

	// Wait for a background refresh to fire once the entry expires, instead
	// of racing a fixed sleep against the (second-rounded) refresh schedule.
	require.Eventually(t, func() bool {
		muCalled.Lock()
		defer muCalled.Unlock()
		return called >= 2
	}, 10*time.Second, 100*time.Millisecond, `a background refresh should fire after the entry expires`)

	// Gets continue to succeed and are served from the cache.
	for i := 0; i < 3; i++ {
		_, err := c.Get(ctx, srv.URL)
		require.NoError(t, err, `c.Get should succeed`)
	}

	require.Empty(t, errSink.getErrors())

	c.Register(srv.URL,
		httprc.WithHTTPClient(srv.Client()),
		httprc.WithMinRefreshInterval(time.Second),
		httprc.WithTransformer(httprc.TransformFunc(func(_ string, _ *http.Response) (interface{}, error) {
			return nil, errors.New(`dummy error`)
		})),
	)

	// The synchronous Get returns the transform error to the caller; the
	// error sink is only fed by background refreshes. Wait for one to fire
	// rather than racing a fixed sleep against the refresh schedule.
	_, _ = c.Get(ctx, srv.URL)
	require.Eventually(t, func() bool {
		return len(errSink.getErrors()) > 0
	}, 10*time.Second, 100*time.Millisecond, `error sink should receive a background refresh error`)
	cancel()
}
