package httprc_test

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/httprc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	flag.Parse() // need to be called for us to use testing.Verbose()
	if testing.Verbose() {
		log.Printf("github.com/lestrrat-go/httprc: Because these tests deal with caching and timing,")
		log.Printf("   they may take a while to run. Please be patient.")
	}
	os.Exit(m.Run())
}

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

// This test is taken from https://gist.github.com/TheJokr/d5b836cca484d4a00967504c553987cf
// It reproduces a panic that could occur when a refresh is scheduled while a fetch worker is busy.
func TestGH30(t *testing.T) {
	t.Parallel()

	// Simulate slow endpoint with 2s response latency
	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer slowServer.Close()

	// Even slower endpoint to make fetch worker busy
	blockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer blockServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slowURL := slowServer.URL
	blockURL := blockServer.URL

	// Refresh quickly to make sure scheduled refresh and
	// foreground refresh are queued while blockUrl is fetching
	refreshWindow := 1 * time.Second
	refreshInterval := 2 * time.Second
	// Limit to 1 worker to make queueing requests easier
	workerCount := 1

	cache := httprc.NewCache(ctx,
		httprc.WithRefreshWindow(refreshWindow),
		httprc.WithFetcherWorkerCount(workerCount))

	require.NoError(t, cache.Register(blockURL, httprc.WithRefreshInterval(time.Hour)), `register should succeed`)
	require.NoError(t, cache.Register(slowURL, httprc.WithRefreshInterval(refreshInterval)), `register should succeed`)

	// Step 1: Fetch slowUrl once to schedule refresh
	_, err := cache.Get(ctx, slowURL)
	require.NoError(t, err, `get should succeed`)

	// Step 2: Fetch blockUrl in a separate goroutine
	// to make sure our single fetch worker is busy
	running := make(chan struct{})
	go func() {
		close(running)
		_, err := cache.Get(ctx, blockURL)
		require.NoError(t, err, `get (block url) should succeed`)
	}()

	// Step 3: Wait for blockUrl to start fetching
	<-running

	// Step 4: Queue foreground refresh
	// By the time the blockUrl fetch finishes, both this Refresh(...)
	// and the scheduled refresh for slowUrl will be queued. The second
	// of those will cause the panic.
	_, err = cache.Refresh(ctx, slowURL)
	require.NoError(t, err, `get (slow url) should succeed`)

	// Step 5: Wait for panic
	<-ctx.Done()
}
