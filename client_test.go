package httprc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/httprc/v3/errsink"
	"github.com/lestrrat-go/httprc/v3/tracesink"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	t.Parallel()

	t.Run("default client", func(t *testing.T) {
		cl := httprc.NewClient()
		require.NotNil(t, cl)
	})

	t.Run("with custom options", func(t *testing.T) {
		// Test with custom worker count
		cl := httprc.NewClient(httprc.WithWorkers(10))
		require.NotNil(t, cl)

		// Test with custom HTTP client
		customHTTPClient := &http.Client{Timeout: 5 * time.Second}
		cl = httprc.NewClient(httprc.WithHTTPClient(customHTTPClient))
		require.NotNil(t, cl)

		// Test with custom error sink
		cl = httprc.NewClient(httprc.WithErrorSink(errsink.NewNop()))
		require.NotNil(t, cl)

		// Test with custom trace sink
		cl = httprc.NewClient(httprc.WithTraceSink(tracesink.NewNop()))
		require.NotNil(t, cl)

		// Test with whitelist
		cl = httprc.NewClient(httprc.WithWhitelist(httprc.NewInsecureWhitelist()))
		require.NotNil(t, cl)
	})

	t.Run("with zero workers", func(t *testing.T) {
		// Should default to 1 worker when 0 is specified
		cl := httprc.NewClient(httprc.WithWorkers(0))
		require.NotNil(t, cl)
	})

	t.Run("with negative workers", func(t *testing.T) {
		// Should default to 1 worker when negative is specified
		cl := httprc.NewClient(httprc.WithWorkers(-1))
		require.NotNil(t, cl)
	})
}

func TestClientStart(t *testing.T) {
	t.Parallel()

	t.Run("successful start", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		cl := httprc.NewClient()
		ctrl, err := cl.Start(ctx)
		require.NoError(t, err)
		require.NotNil(t, ctrl)
		defer ctrl.Shutdown(time.Second)
	})

	t.Run("start twice should fail", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		cl := httprc.NewClient()
		ctrl1, err := cl.Start(ctx)
		require.NoError(t, err)
		require.NotNil(t, ctrl1)
		defer ctrl1.Shutdown(time.Second)

		// Second start should fail
		ctrl2, err := cl.Start(ctx)
		require.Error(t, err)
		require.Nil(t, ctrl2)
	})

	t.Run("start with canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		cl := httprc.NewClient()
		ctrl, err := cl.Start(ctx)
		require.NoError(t, err) // Start should succeed even with canceled context
		require.NotNil(t, ctrl)
		ctrl.Shutdown(time.Second)
	})
}

func TestClientConcurrentStart(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cl := httprc.NewClient()

	const numGoroutines = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	var successCount, errorCount int
	var successCtrl httprc.Controller

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctrl, err := cl.Start(ctx)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errorCount++
			} else {
				successCount++
				if successCtrl == nil {
					successCtrl = ctrl
				} else {
					// If we somehow got multiple successes, clean up
					ctrl.Shutdown(time.Second)
				}
			}
		}()
	}

	wg.Wait()

	// Exactly one should succeed, others should fail
	require.Equal(t, 1, successCount, "exactly one start should succeed")
	require.Equal(t, numGoroutines-1, errorCount, "all other starts should fail")
	require.NotNil(t, successCtrl, "should have one successful controller")

	successCtrl.Shutdown(time.Second)
}

func TestClientWithCustomSinks(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create mock sinks to capture messages
	var errorMessages []string
	var traceMessages []string
	var mu sync.Mutex

	errorSink := errsink.NewFunc(func(ctx context.Context, err error) {
		mu.Lock()
		defer mu.Unlock()
		errorMessages = append(errorMessages, err.Error())
	})

	traceSink := tracesink.NewFunc(func(ctx context.Context, msg string) {
		mu.Lock()
		defer mu.Unlock()
		traceMessages = append(traceMessages, msg)
	})

	cl := httprc.NewClient(
		httprc.WithErrorSink(errorSink),
		httprc.WithTraceSink(traceSink),
	)

	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	defer ctrl.Shutdown(time.Second)

	// Add a resource to generate some trace messages
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"test": "data"})
	}))
	defer srv.Close()

	resource, err := httprc.NewResource[map[string]string](
		srv.URL,
		httprc.JSONTransformer[map[string]string](),
	)
	require.NoError(t, err, "custom sinks test resource creation should succeed")

	require.NoError(t, ctrl.Add(ctx, resource), "adding custom sinks test resource should succeed")

	// Wait a bit for traces to be generated
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// Should have some trace messages
	require.Greater(t, len(traceMessages), 0, "should have received trace messages")

	// Error messages might be empty if no errors occurred, which is fine
	// but we test that the sink was properly set up
}

func TestClientMultipleResources(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create multiple test servers
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"server": "1"})
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"server": "2"})
	}))
	defer srv2.Close()

	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"server": "3"})
	}))
	defer srv3.Close()

	cl := httprc.NewClient(httprc.WithWorkers(3))
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	defer ctrl.Shutdown(time.Second)

	// Create multiple resources
	resources := make([]httprc.Resource, 0, 3)
	servers := []string{srv1.URL, srv2.URL, srv3.URL}

	for i, serverURL := range servers {
		resource, err := httprc.NewResource[map[string]string](
			serverURL,
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err, "creating resource %d", i)
		resources = append(resources, resource)

		require.NoError(t, ctrl.Add(ctx, resource), "adding resource %d", i)
	}

	// Verify all resources are working
	for i, resource := range resources {
		require.NoError(t, resource.Ready(ctx), "resource %d should be ready", i)

		var data map[string]string
		require.NoError(t, resource.Get(&data), "getting data from resource %d", i)
		require.Equal(t, fmt.Sprintf("%d", i+1), data["server"], "resource %d should return correct server ID", i)
	}
}
