package httprc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/httprc/v3/errsink"
	"github.com/lestrrat-go/httprc/v3/tracesink"
	"github.com/stretchr/testify/require"
)

func TestErrorSinkIntegration(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var capturedErrors []error
	var mu sync.Mutex

	errorSink := errsink.NewFunc(func(_ context.Context, err error) {
		mu.Lock()
		defer mu.Unlock()
		capturedErrors = append(capturedErrors, err)
	})

	// Create a server that returns errors
	errorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Server Error", http.StatusInternalServerError)
	}))
	defer errorSrv.Close()

	cl := httprc.NewClient(httprc.WithErrorSink(errorSink))
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	resource, err := httprc.NewResource[[]byte](
		errorSrv.URL,
		httprc.BytesTransformer(),
	)
	require.NoError(t, err)

	// Add resource without waiting for ready (to avoid test blocking)
	err = ctrl.Add(ctx, resource, httprc.WithWaitReady(false))
	require.NoError(t, err)

	// Wait a bit for error to be captured
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	errorCount := len(capturedErrors)
	mu.Unlock()

	require.Positive(t, errorCount, "should have captured at least one error")
}

func TestTraceSinkIntegration(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var capturedTraces []string
	var mu sync.Mutex

	traceSink := tracesink.Func(func(_ context.Context, msg string) {
		mu.Lock()
		defer mu.Unlock()
		capturedTraces = append(capturedTraces, msg)
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	t.Cleanup(func() { srv.Close() })

	cl := httprc.NewClient(httprc.WithTraceSink(traceSink))
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)

	resource, err := httprc.NewResource[map[string]string](
		srv.URL,
		httprc.JSONTransformer[map[string]string](),
	)
	require.NoError(t, err)
	require.NoError(t, ctrl.Add(ctx, resource), "adding trace test resource should succeed")

	// Wait a bit for traces to be generated
	time.Sleep(time.Second)
	ctrl.Shutdown(time.Second)

	mu.Lock()
	traceCount := len(capturedTraces)
	mu.Unlock()

	require.Positive(t, traceCount, "should have captured trace messages")

	// Check for expected trace messages
	mu.Lock()
	traces := make([]string, len(capturedTraces))
	copy(traces, capturedTraces)
	mu.Unlock()

	foundControllerStart := false
	foundResourceAdded := false
	for _, trace := range traces {
		t.Logf("Captured trace: %q", trace)
		switch trace {
		case "httprc controller: starting main controller loop":
			foundControllerStart = true
		case fmt.Sprintf("httprc controller: added resource %q", srv.URL):
			foundResourceAdded = true
		}
	}

	require.True(t, foundControllerStart, "should have trace for controller start")
	require.True(t, foundResourceAdded, "should have trace for resource addition")
}

func TestConcurrentResourceAccess(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var requestCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		json.NewEncoder(w).Encode(map[string]int64{"count": count})
	}))
	defer srv.Close()

	cl := httprc.NewClient(httprc.WithWorkers(10))
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	// Add multiple resources
	const numResources = 5
	resources := make([]httprc.Resource, numResources)

	for i := range numResources {
		resource, err := httprc.NewResource[map[string]int64](
			fmt.Sprintf("%s/resource-%d", srv.URL, i),
			httprc.JSONTransformer[map[string]int64](),
		)
		require.NoError(t, err)

		resources[i] = resource
		err = ctrl.Add(ctx, resource)
		require.NoError(t, err)
	}

	// Concurrent access to resources
	const numGoroutines = 20
	const numOperations = 10

	var wg sync.WaitGroup
	for i := range numGoroutines {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := range numOperations {
				resourceIdx := (workerID + j) % numResources
				resource := resources[resourceIdx]

				var data map[string]int64
				err := resource.Get(&data)
				if err != nil {
					t.Errorf("worker %d: failed to get data from resource %d: %v", workerID, resourceIdx, err)
					return
				}

				if data["count"] <= 0 {
					t.Errorf("worker %d: invalid count %d from resource %d", workerID, data["count"], resourceIdx)
					return
				}

				// Trigger refresh occasionally
				if j%3 == 0 {
					err := ctrl.Refresh(ctx, resource.URL())
					if err != nil {
						t.Errorf("worker %d: failed to refresh resource %d: %v", workerID, resourceIdx, err)
						return
					}
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify final state
	for i, resource := range resources {
		var data map[string]int64
		err := resource.Get(&data)
		require.NoError(t, err, "resource %d should be accessible", i)
		require.Positive(t, data["count"], "resource %d should have valid count", i)
	}
}

func TestResourceLeaks(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer srv.Close()

	// Test that adding and removing many resources doesn't cause leaks
	const cycles = 10
	const resourcesPerCycle = 20

	for cycle := range cycles {
		cl := httprc.NewClient()
		ctrl, err := cl.Start(ctx)
		require.NoError(t, err)

		// Add many resources
		urls := make([]string, resourcesPerCycle)
		for i := range resourcesPerCycle {
			testURL := fmt.Sprintf("%s/leak-test-%d-%d", srv.URL, cycle, i)
			urls[i] = testURL

			resource, err := httprc.NewResource[map[string]string](
				testURL,
				httprc.JSONTransformer[map[string]string](),
			)
			require.NoError(t, err)

			err = ctrl.Add(ctx, resource)
			require.NoError(t, err)
		}

		// Remove half of them
		for i := range resourcesPerCycle / 2 {
			err := ctrl.Remove(ctx, urls[i])
			require.NoError(t, err)
		}

		// Shutdown cleanly
		err = ctrl.Shutdown(time.Second)
		require.NoError(t, err)
	}
}

func TestWhitelistIntegration(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	t.Cleanup(srv.Close)

	t.Run("insecure whitelist allows all", func(t *testing.T) {
		t.Parallel()
		cl := httprc.NewClient(httprc.WithWhitelist(httprc.NewInsecureWhitelist()))
		ctrl, err := cl.Start(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { ctrl.Shutdown(time.Second) })

		// Should allow any URL
		resource, err := httprc.NewResource[map[string]string](
			srv.URL,
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err)

		err = ctrl.Add(ctx, resource)
		require.NoError(t, err)
	})

	// Note: Testing restrictive whitelists would require implementing
	// a custom whitelist type, which is outside the scope of this test
}
