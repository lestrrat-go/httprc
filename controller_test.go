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
	"github.com/stretchr/testify/require"
)

func TestControllerAdd(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	t.Cleanup(srv.Close)

	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	t.Run("add new resource", func(t *testing.T) {
		t.Parallel()
		resource, err := httprc.NewResource[map[string]string](
			srv.URL+"/test1",
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err)

		require.NoError(t, ctrl.Add(ctx, resource), "adding new resource to controller should succeed")

		// Should be able to lookup the resource
		found, err := ctrl.Lookup(ctx, srv.URL+"/test1")
		require.NoError(t, err)
		require.Equal(t, srv.URL+"/test1", found.URL())
	})

	t.Run("add duplicate resource should fail", func(t *testing.T) {
		t.Parallel()
		resource1, err := httprc.NewResource[map[string]string](
			srv.URL+"/test2",
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err)

		// First add should succeed
		require.NoError(t, ctrl.Add(ctx, resource1), "first resource addition should succeed")

		resource2, err := httprc.NewResource[map[string]string](
			srv.URL+"/test2", // Same URL
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err)

		// Second add should fail
		err = ctrl.Add(ctx, resource2)
		require.Error(t, err)
	})

	t.Run("add with canceled context", func(t *testing.T) {
		t.Parallel()
		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		resource, err := httprc.NewResource[map[string]string](
			srv.URL+"/test3",
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err)

		err = ctrl.Add(canceledCtx, resource)
		require.Error(t, err)
		require.Equal(t, context.Canceled, err)
	})
}

func TestControllerLookup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path})
	}))
	t.Cleanup(srv.Close)

	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	t.Run("lookup existing resource", func(t *testing.T) {
		t.Parallel()
		testURL := srv.URL + "/lookup-test"
		resource, err := httprc.NewResource[map[string]string](
			testURL,
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err)

		require.NoError(t, ctrl.Add(ctx, resource), "adding resource for lookup test should succeed")

		found, err := ctrl.Lookup(ctx, testURL)
		require.NoError(t, err)
		require.Equal(t, testURL, found.URL())
	})

	t.Run("lookup non-existent resource", func(t *testing.T) {
		t.Parallel()
		nonExistentURL := srv.URL + "/does-not-exist"
		_, err := ctrl.Lookup(ctx, nonExistentURL)
		require.Error(t, err)
	})

	t.Run("lookup with canceled context", func(t *testing.T) {
		t.Parallel()
		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		_, err := ctrl.Lookup(canceledCtx, srv.URL+"/any")
		require.Error(t, err)
		require.Equal(t, context.Canceled, err)
	})
}

func TestControllerRemove(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	t.Cleanup(srv.Close)

	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	t.Run("remove existing resource", func(t *testing.T) {
		t.Parallel()
		testURL := srv.URL + "/remove-test"
		resource, err := httprc.NewResource[map[string]string](
			testURL,
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err)

		// Add the resource
		require.NoError(t, ctrl.Add(ctx, resource), "adding resource for removal test should succeed")

		// Verify it exists
		_, err = ctrl.Lookup(ctx, testURL)
		require.NoError(t, err, "resource should be found after adding")

		// Remove it
		require.NoError(t, ctrl.Remove(ctx, testURL), "removing existing resource should succeed")

		// Verify it's gone
		_, err = ctrl.Lookup(ctx, testURL)
		require.Error(t, err)
	})

	t.Run("remove non-existent resource", func(t *testing.T) {
		t.Parallel()
		nonExistentURL := srv.URL + "/does-not-exist"
		err := ctrl.Remove(ctx, nonExistentURL)
		require.Error(t, err)
	})

	t.Run("remove with canceled context", func(t *testing.T) {
		t.Parallel()
		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		err := ctrl.Remove(canceledCtx, srv.URL+"/any")
		require.Error(t, err)
		require.Equal(t, context.Canceled, err)
	})
}

func TestControllerRefresh(t *testing.T) {
	t.Parallel()

	var requestCount int
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requestCount++
		count := requestCount
		mu.Unlock()

		json.NewEncoder(w).Encode(map[string]int{"count": count})
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	t.Run("refresh existing resource", func(t *testing.T) {
		t.Parallel()
		testURL := srv.URL + "/refresh-test"
		resource, err := httprc.NewResource[map[string]int](
			testURL,
			httprc.JSONTransformer[map[string]int](),
		)
		require.NoError(t, err)

		// Add the resource (this will trigger the first request)
		require.NoError(t, ctrl.Add(ctx, resource), "adding resource for refresh test should succeed")

		// Get initial count
		var data1 map[string]int
		require.NoError(t, resource.Get(&data1), "getting initial data should succeed")
		initialCount := data1["count"]

		// Force refresh
		require.NoError(t, ctrl.Refresh(ctx, testURL), "refreshing resource should succeed")

		// Get updated count
		var data2 map[string]int
		require.NoError(t, resource.Get(&data2), "getting updated data should succeed")
		newCount := data2["count"]

		require.Greater(t, newCount, initialCount, "count should have increased after refresh")
	})

	t.Run("refresh non-existent resource", func(t *testing.T) {
		t.Parallel()
		nonExistentURL := srv.URL + "/does-not-exist"
		err := ctrl.Refresh(ctx, nonExistentURL)
		require.Error(t, err)
	})

	t.Run("refresh with canceled context", func(t *testing.T) {
		t.Parallel()
		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		err := ctrl.Refresh(canceledCtx, srv.URL+"/any")
		require.Error(t, err)
		require.Equal(t, context.Canceled, err)
	})
}

func TestControllerShutdown(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	t.Cleanup(srv.Close)

	t.Run("shutdown with timeout", func(t *testing.T) {
		t.Parallel()
		cl := httprc.NewClient()
		ctrl, err := cl.Start(ctx)
		require.NoError(t, err)

		// Add a resource
		resource, err := httprc.NewResource[map[string]string](
			srv.URL,
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err)
		require.NoError(t, ctrl.Add(ctx, resource), "adding resource for shutdown test should succeed")

		// Shutdown should complete within timeout
		require.NoError(t, ctrl.Shutdown(5*time.Second), "shutdown with timeout should succeed")
	})

	t.Run("shutdown with context", func(t *testing.T) {
		t.Parallel()
		cl := httprc.NewClient()
		ctrl, err := cl.Start(ctx)
		require.NoError(t, err)

		// Add a resource
		resource, err := httprc.NewResource[map[string]string](
			srv.URL,
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err)
		require.NoError(t, ctrl.Add(ctx, resource), "adding resource for shutdown context test should succeed")

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()

		require.NoError(t, ctrl.ShutdownContext(shutdownCtx), "shutdown with context should succeed")
	})

	t.Run("shutdown with canceled context", func(t *testing.T) {
		t.Parallel()
		cl := httprc.NewClient()
		ctrl, err := cl.Start(ctx)
		require.NoError(t, err)

		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		err = ctrl.ShutdownContext(canceledCtx)
		require.Error(t, err)
		require.Equal(t, context.Canceled, err)

		// Clean shutdown with proper context
		require.NoError(t, ctrl.Shutdown(time.Second), "clean shutdown should succeed")
	})
}

func TestControllerConcurrentOperations(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"url": r.URL.Path})
	}))
	defer srv.Close()

	cl := httprc.NewClient(httprc.WithWorkers(5))
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	const numGoroutines = 10
	const numOperationsPerGoroutine = 5

	var wg sync.WaitGroup
	var addedURLs sync.Map

	// Concurrent adds
	for i := range numGoroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range numOperationsPerGoroutine {
				testURL := fmt.Sprintf("%s/concurrent-test-%d-%d", srv.URL, i, j)
				resource, err := httprc.NewResource[map[string]string](
					testURL,
					httprc.JSONTransformer[map[string]string](),
				)
				if err != nil {
					t.Errorf("failed to create resource: %v", err)
					return
				}

				err = ctrl.Add(ctx, resource)
				if err != nil {
					t.Errorf("failed to add resource %s: %v", testURL, err)
					return
				}

				addedURLs.Store(testURL, true)
			}
		}(i)
	}

	wg.Wait()

	// Verify all resources can be looked up
	addedURLs.Range(func(key, _ any) bool {
		testURL := key.(string)
		_, err := ctrl.Lookup(ctx, testURL)
		require.NoError(t, err, "should be able to lookup %s", testURL)
		return true
	})

	// Concurrent lookups and refreshes
	wg = sync.WaitGroup{}
	for i := range numGoroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range numOperationsPerGoroutine {
				testURL := fmt.Sprintf("%s/concurrent-test-%d-%d", srv.URL, i, j)

				// Lookup
				_, err := ctrl.Lookup(ctx, testURL)
				if err != nil {
					t.Errorf("failed to lookup resource %s: %v", testURL, err)
					return
				}

				// Refresh
				err = ctrl.Refresh(ctx, testURL)
				if err != nil {
					t.Errorf("failed to refresh resource %s: %v", testURL, err)
					return
				}
			}
		}(i)
	}

	wg.Wait()
}
