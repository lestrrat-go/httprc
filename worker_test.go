package httprc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/httprc/v3/tracesink"
	"github.com/stretchr/testify/require"
)

func TestWorkerPoolBehavior(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var requestCount int64
	var mu sync.Mutex
	var requestTimes []time.Time

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requestTimes = append(requestTimes, time.Now())
		requestCount++
		count := requestCount
		mu.Unlock()

		// Simulate some work
		time.Sleep(10 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]int64{"count": count})
	}))
	t.Cleanup(srv.Close)

	t.Run("worker pool processes requests concurrently", func(t *testing.T) {
		t.Parallel()
		const numWorkers = 5
		traceDst := io.Discard
		if testing.Verbose() {
			traceDst = os.Stderr
		}

		cl := httprc.NewClient(
			httprc.WithTraceSink(tracesink.NewSlog(slog.New(slog.NewJSONHandler(traceDst, nil)))),
			httprc.WithWorkers(numWorkers),
		)
		ctrl, err := cl.Start(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { ctrl.Shutdown(time.Second) })

		// Add multiple resources that will be fetched simultaneously
		const numResources = numWorkers * 2
		for i := range numResources {
			resource, err := httprc.NewResource[map[string]int64](
				fmt.Sprintf("%s/worker-test-%d", srv.URL, i),
				httprc.JSONTransformer[map[string]int64](),
			)
			require.NoError(t, err, "worker stress test resource %d creation should succeed", i)
			require.NoError(t, ctrl.Add(ctx, resource), "adding worker stress test resource %d should succeed", i)
		}

		// Force refresh all resources simultaneously
		var wg sync.WaitGroup
		for i := range numResources {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				url := fmt.Sprintf("%s/worker-test-%d", srv.URL, i)

				tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()

				if err := ctrl.Refresh(tctx, url); err != nil {
					t.Errorf("worker: should refresh resource %d: %v", i, err)
				}
			}(i)
		}
		wg.Wait()

		mu.Lock()
		finalCount := requestCount
		mu.Unlock()
		require.Greater(t, finalCount, int64(numResources), "should have processed multiple requests")
	})

	t.Run("single worker processes requests sequentially", func(t *testing.T) {
		t.Parallel()
		// Reset counters
		mu.Lock()
		requestCount = 0
		requestTimes = requestTimes[:0]
		mu.Unlock()

		traceDst := io.Discard
		if testing.Verbose() {
			traceDst = os.Stderr
		}
		const numWorkers = 1
		cl := httprc.NewClient(
			httprc.WithTraceSink(tracesink.NewSlog(slog.New(slog.NewJSONHandler(traceDst, nil)))),
			httprc.WithWorkers(numWorkers),
		)
		ctrl, err := cl.Start(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { ctrl.Shutdown(time.Second) })

		// Add multiple resources
		const numResources = 3
		for i := range numResources {
			resource, err := httprc.NewResource[map[string]int64](
				fmt.Sprintf("%s/sequential-test-%d", srv.URL, i),
				httprc.JSONTransformer[map[string]int64](),
			)
			require.NoError(t, err, "sequential processing test resource %d creation should succeed", i)

			require.NoError(t, ctrl.Add(ctx, resource), "adding sequential processing test resource %d should succeed", i)
		}

		// Force refresh all resources
		var wg sync.WaitGroup
		for i := range numResources {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				url := fmt.Sprintf("%s/sequential-test-%d", srv.URL, i)

				tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()

				if err := ctrl.Refresh(tctx, url); err != nil {
					t.Errorf("sequential: should refresh resource %d: %v", i, err)
				}
			}(i)
		}
		wg.Wait()
	})
}

func TestPeriodicRefresh(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var requestCount int64
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requestCount++
		count := requestCount
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=1") // Short cache for testing
		json.NewEncoder(w).Encode(map[string]int64{"count": count})
	}))
	t.Cleanup(srv.Close)

	traceDst := io.Discard
	if testing.Verbose() {
		traceDst = os.Stderr
	}

	cl := httprc.NewClient(
		httprc.WithTraceSink(tracesink.NewSlog(slog.New(slog.NewJSONHandler(traceDst, nil)))),
	)
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	t.Run("resource refreshes automatically", func(t *testing.T) {
		t.Parallel()
		resource, err := httprc.NewResource[map[string]int64](
			srv.URL+"/periodic-test",
			httprc.JSONTransformer[map[string]int64](),
		)
		require.NoError(t, err)

		// Set short intervals for fast testing
		resource.SetMinInterval(10 * time.Millisecond)
		resource.SetMaxInterval(20 * time.Millisecond)

		require.NoError(t, ctrl.Add(ctx, resource), "should add resource to controller")

		// Get initial count
		var data1 map[string]int64
		require.NoError(t, resource.Get(&data1), "getting initial data should succeed")
		initialCount := data1["count"]

		time.Sleep(5 * time.Second)

		// Get updated count
		var data2 map[string]int64
		require.NoError(t, resource.Get(&data2), "getting updated data should succeed")
		newCount := data2["count"]

		require.Greater(t, newCount, initialCount, "resource should have been automatically refreshed")
	})

	/*
		t.Run("multiple resources refresh independently", func(t *testing.T) {
			// Reset counter
			mu.Lock()
			requestCount = 0
			mu.Unlock()

			const numResources = 3
			resources := make([]httprc.Resource, numResources)

			for i := 0; i < numResources; i++ {
				resource, err := httprc.NewResource[map[string]int64](
					fmt.Sprintf("%s/multi-periodic-%d", srv.URL, i),
					httprc.JSONTransformer[map[string]int64](),
				)
				require.NoError(t, err)

				// Set different intervals for each resource
				interval := time.Duration(50*(i+1)) * time.Millisecond
				resource.SetMinInterval(interval)
				resource.SetMaxInterval(interval * 2)

				resources[i] = resource
				require.NoError(t, ctrl.Add(ctx, resource), "adding interval test resource %d should succeed", i)
			}

			// Wait for multiple refresh cycles
			time.Sleep(400 * time.Millisecond)

			// Each resource should have been refreshed at least once
			for i, resource := range resources {
				var data map[string]int64
				err := resource.Get(&data)
				require.NoError(t, err, "resource %d should be accessible", i)
				require.Greater(t, data["count"], int64(0), "resource %d should have been refreshed", i)
			}

			mu.Lock()
			finalRequestCount := requestCount
			mu.Unlock()
			require.Greater(t, finalRequestCount, int64(numResources), "should have made multiple requests due to refreshes")
		})
	*/
}

func TestEdgeCases(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	t.Run("empty response body", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Return empty body
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		cl := httprc.NewClient()
		ctrl, err := cl.Start(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { ctrl.Shutdown(time.Second) })

		resource, err := httprc.NewResource[[]byte](
			srv.URL,
			httprc.BytesTransformer(),
		)
		require.NoError(t, err, "empty response test resource creation should succeed")

		require.NoError(t, ctrl.Add(ctx, resource), "adding empty response test resource should succeed")

		var data []byte
		require.NoError(t, resource.Get(&data), "getting empty data should succeed")
		require.Empty(t, data)
	})

	t.Run("very large response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Generate a large response
			data := make(map[string]string)
			for i := range 10000 {
				data[fmt.Sprintf("key_%d", i)] = fmt.Sprintf("value_%d_with_lots_of_data_to_make_it_large", i)
			}
			json.NewEncoder(w).Encode(data)
		}))
		defer srv.Close()

		cl := httprc.NewClient()
		ctrl, err := cl.Start(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { ctrl.Shutdown(time.Second) })

		resource, err := httprc.NewResource[map[string]string](
			srv.URL,
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err, "large response test resource creation should succeed")

		require.NoError(t, ctrl.Add(ctx, resource), "adding large response test resource should succeed")

		var data map[string]string
		require.NoError(t, resource.Get(&data), "getting large response data should succeed")
		require.Len(t, data, 10000)
	})

	t.Run("rapid add and remove", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		}))
		defer srv.Close()

		cl := httprc.NewClient()
		ctrl, err := cl.Start(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { ctrl.Shutdown(time.Second) })

		// Rapidly add and remove resources
		for i := range 100 {
			testURL := fmt.Sprintf("%s/rapid-test-%d", srv.URL, i)

			resource, err := httprc.NewResource[map[string]string](
				testURL,
				httprc.JSONTransformer[map[string]string](),
			)
			require.NoError(t, err, "rapid add/remove test resource %d creation should succeed", i)

			require.NoError(t, ctrl.Add(ctx, resource), "adding rapid add/remove test resource %d should succeed", i)

			// Immediately remove it
			require.NoError(t, ctrl.Remove(ctx, testURL), "removing rapid add/remove test resource %d should succeed", i)
		}
	})

	t.Run("invalid JSON with fallback", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("invalid json {"))
		}))
		defer srv.Close()

		cl := httprc.NewClient()
		ctrl, err := cl.Start(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { ctrl.Shutdown(time.Second) })

		// Use bytes transformer instead of JSON for invalid JSON
		resource, err := httprc.NewResource[[]byte](
			srv.URL,
			httprc.BytesTransformer(),
		)
		require.NoError(t, err, "invalid JSON fallback test resource creation should succeed")

		require.NoError(t, ctrl.Add(ctx, resource), "adding invalid JSON fallback test resource should succeed")

		var data []byte
		require.NoError(t, resource.Get(&data), "getting invalid JSON fallback data should succeed")
		require.Equal(t, []byte("invalid json {"), data)
	})
}

func TestResourceLifecycle(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var requestPhases []string
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requestPhases = append(requestPhases, "request_received")
		mu.Unlock()

		time.Sleep(10 * time.Millisecond) // Simulate processing
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

		mu.Lock()
		requestPhases = append(requestPhases, "response_sent")
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)

	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	t.Run("full lifecycle", func(t *testing.T) {
		t.Parallel()
		resource, err := httprc.NewResource[map[string]string](
			srv.URL+"/lifecycle-test",
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err, "lifecycle test resource creation should succeed")

		// Initially, resource should not be ready
		require.False(t, resource.IsBusy())

		// Add resource (this will trigger the first fetch)
		require.NoError(t, ctrl.Add(ctx, resource), "adding lifecycle test resource should succeed")

		// Resource should now be available
		var data map[string]string
		require.NoError(t, resource.Get(&data), "getting lifecycle test data should succeed")
		require.Equal(t, "ok", data["status"])

		// Lookup should find the resource
		found, err := ctrl.Lookup(ctx, srv.URL+"/lifecycle-test")
		require.NoError(t, err)
		require.Equal(t, resource.URL(), found.URL())

		// Refresh should work
		require.NoError(t, ctrl.Refresh(ctx, srv.URL+"/lifecycle-test"), "refreshing lifecycle test resource should succeed")

		// Remove should work
		require.NoError(t, ctrl.Remove(ctx, srv.URL+"/lifecycle-test"), "removing lifecycle test resource should succeed")

		// Lookup should now fail
		_, err = ctrl.Lookup(ctx, srv.URL+"/lifecycle-test")
		require.Error(t, err)

		mu.Lock()
		phases := make([]string, len(requestPhases))
		copy(phases, requestPhases)
		mu.Unlock()

		require.NotEmpty(t, phases, "should have recorded request phases")
	})
}
