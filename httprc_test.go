package httprc_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/httprc/v3/tracesink"
	"github.com/stretchr/testify/require"
)

func TestResource(t *testing.T) {
	const dummy = "https://127.0.0.1:99999999"
	r, err := httprc.NewResource[[]byte](dummy, httprc.BytesTransformer())
	require.NoError(t, err, `NewResource should succeed`)
	require.Equal(t, httprc.DefaultMinInterval, r.MinInterval(), `r.MinInterval should return DefaultMinInterval`)
	require.Equal(t, httprc.DefaultMaxInterval, r.MaxInterval(), `r.MaxInterval should return DefaultMaxInterval`)

	r, err = httprc.NewResource[[]byte](dummy, httprc.BytesTransformer(), httprc.WithMinInterval(12*time.Second))
	require.NoError(t, err, `NewResource should succeed`)
	require.Equal(t, 12*time.Second, r.MinInterval(), `r.MinInterval should return expected value`)
	require.Equal(t, httprc.DefaultMaxInterval, r.MaxInterval(), `r.MaxInterval should return DefaultMaxInterval`)

	r, err = httprc.NewResource[[]byte](dummy, httprc.BytesTransformer(), httprc.WithMaxInterval(12*time.Second))
	require.NoError(t, err, `NewResource should succeed`)
	require.Equal(t, httprc.DefaultMinInterval, r.MinInterval(), `r.MinInterval should return DefaultMinInterval`)
	require.Equal(t, 12*time.Second, r.MaxInterval(), `r.MaxInterval should return expected value`)
}

func TestClient(t *testing.T) {
	type Hello struct {
		Hello string `json:"hello"`
	}

	start := time.Now()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=2")
		var version string
		if time.Since(start) > 2*time.Second {
			version = "2"
		}
		switch r.URL.Path {
		case "/json/helloptr", "/json/hello", "/json/hellomap":
			w.Header().Set("Content-Type", "application/json")
			switch version {
			case "2":
				w.Write([]byte(`{"hello":"world2"}`))
			default:
				w.Write([]byte(`{"hello":"world"}`))
			}
		case "/int":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(`42`))
		case "/string":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(`Lorem ipsum dolor sit amet`))
		case "/custom":
		}
	})

	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	options := []httprc.NewClientOption{
		//		httprc.WithTraceSink(tracesink.NewSlog(slog.New(slog.NewJSONHandler(os.Stdout, nil)))),
	}
	cl := httprc.NewClient(options...)
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err, `cl.Run should succeed`)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	testcases := []struct {
		URL       string
		Create    func() (httprc.Resource, error)
		Expected  any
		Expected2 any
	}{
		{
			URL: srv.URL + "/json/helloptr",
			Create: func() (httprc.Resource, error) {
				r, err := httprc.NewResource[*Hello](srv.URL+"/json/helloptr", httprc.JSONTransformer[*Hello]())
				if err != nil {
					return nil, err
				}
				r.SetMinInterval(time.Second)
				return r, nil
			},
			Expected:  &Hello{Hello: "world"},
			Expected2: &Hello{Hello: "world2"},
		},
		{
			URL: srv.URL + "/json/hello",
			Create: func() (httprc.Resource, error) {
				r, err := httprc.NewResource[Hello](srv.URL+"/json/hello", httprc.JSONTransformer[Hello]())
				if err != nil {
					return nil, err
				}
				r.SetMinInterval(time.Second)
				return r, nil
			},
			Expected:  Hello{Hello: "world"},
			Expected2: Hello{Hello: "world2"},
		},
		{
			URL: srv.URL + "/json/hellomap",
			Create: func() (httprc.Resource, error) {
				r, err := httprc.NewResource[map[string]any](srv.URL+"/json/hellomap", httprc.JSONTransformer[map[string]any]())
				if err != nil {
					return nil, err
				}
				r.SetMinInterval(time.Second)
				return r, nil
			},
			Expected:  map[string]any{"hello": "world"},
			Expected2: map[string]any{"hello": "world2"},
		},
		{
			URL: srv.URL + "/int",
			Create: func() (httprc.Resource, error) {
				return httprc.NewResource[int](srv.URL+"/int", httprc.TransformFunc[int](func(_ context.Context, res *http.Response) (int, error) {
					buf, err := io.ReadAll(res.Body)
					if err != nil {
						return 0, err
					}
					return strconv.Atoi(string(buf))
				}))
			},
			Expected: 42,
		},
		{
			URL: srv.URL + "/string",
			Create: func() (httprc.Resource, error) {
				return httprc.NewResource[string](srv.URL+"/string", httprc.TransformFunc[string](func(_ context.Context, res *http.Response) (string, error) {
					buf, err := io.ReadAll(res.Body)
					if err != nil {
						return "", err
					}
					return string(buf), nil
				}))
			},
			Expected: "Lorem ipsum dolor sit amet",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.URL, func(t *testing.T) {
			r, err := tc.Create()
			require.NoError(t, err, `NewResource should succeed`)

			require.NoError(t, ctrl.Add(ctx, r), `ctrl.Add should succeed`)
			require.NoError(t, r.Ready(ctx), `r.Ready should succeed`)

			var dst any
			require.NoError(t, r.Get(&dst), `r.Get should succeed`)

			require.Equal(t, tc.Expected, dst, `r.Get should return expected value`)
		})
	}

	time.Sleep(6 * time.Second)
	for _, tc := range testcases {
		t.Run("Lookup "+tc.URL, func(t *testing.T) {
			r, err := ctrl.Lookup(ctx, tc.URL)
			require.NoError(t, err, `ctrl.Lookup should succeed`)
			require.Equal(t, tc.URL, r.URL(), `r.URL should return expected value`)

			var dst any
			require.NoError(t, r.Get(&dst), `r.Get should succeed`)

			expected := tc.Expected2
			if expected == nil {
				expected = tc.Expected
			}
			require.Equal(t, expected, dst, `r.Resource should return expected value`)
		})
	}
}

func TestRefresh(t *testing.T) {
	count := 0
	var mu sync.Mutex
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		count++
		json.NewEncoder(w).Encode(map[string]any{"count": count})
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	options := []httprc.NewClientOption{
		httprc.WithWhitelist(httprc.NewInsecureWhitelist()),
	}
	cl := httprc.NewClient(options...)
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err, `cl.Run should succeed`)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	r, err := httprc.NewResource[map[string]int](srv.URL, httprc.JSONTransformer[map[string]int]())
	require.NoError(t, err, `NewResource should succeed`)

	require.NoError(t, ctrl.Add(ctx, r), `ctrl.Add should succeed`)

	require.NoError(t, r.Ready(ctx), `r.Ready should succeed`)

	for i := 1; i <= 5; i++ {
		m := r.Resource()
		require.Equal(t, i, m["count"], `r.Resource should return expected value`)
		require.NoError(t, ctrl.Refresh(ctx, srv.URL), `r.Refresh should succeed`)
	}
}

func Test_gh74(t *testing.T) {
	// Test server that returns simple JSON data
	testData := `{"test": "data"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testData))
	}))
	t.Cleanup(func() { server.Close() })

	// Create httprc client with trace logging
	client := httprc.NewClient(
		httprc.WithTraceSink(
			tracesink.NewSlog(slog.New(slog.NewJSONHandler(os.Stdout, nil)))),
	)

	// Create a resource that transforms bytes
	resource, err := httprc.NewResource[[]byte](server.URL, httprc.BytesTransformer())
	require.NoError(t, err, "failed to create resource")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	// Start the client to get a controller
	ctrl, err := client.Start(ctx)
	require.NoError(t, err, "failed to start client")
	defer func() {
		_ = ctrl.Shutdown(time.Second)
	}()

	// Test the original issue: Add with WithWaitReady(false) followed by Refresh calls
	// This would block before the fix
	require.NoError(t, ctrl.Add(ctx, resource, httprc.WithWaitReady(false)), "Add should succeed")

	// These refresh calls would block indefinitely before the fix
	for i := range 10 {
		err = ctrl.Refresh(ctx, server.URL)
		require.NoError(t, err, "refresh should succeed on iteration %d", i)

		// Verify we can lookup the resource
		res, err := ctrl.Lookup(ctx, server.URL)
		require.NoError(t, err, "lookup should succeed")
		require.NotNil(t, res, "resource should not be nil")
	}
}

// TestAdd_returns_err_not_ready_when_ready_fails tests that ErrNotReady is returned
// when Ready() fails but registration succeeded
func TestAdd_returns_err_not_ready_when_ready_fails(t *testing.T) {
	t.Parallel()

	// Setup: Create mock HTTP server that returns invalid JSON
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{invalid json`))
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	resource, err := httprc.NewResource[map[string]any](
		server.URL,
		httprc.JSONTransformer[map[string]any](),
	)
	require.NoError(t, err)

	// Act: Add with timeout (will fail on Ready due to invalid JSON)
	addCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	err = ctrl.Add(addCtx, resource)

	// Assert: Should return ErrNotReady
	require.Error(t, err, "expected error when Ready() fails")
	require.ErrorIs(t, err, httprc.ErrNotReady(), "expected ErrNotReady")

	// Verify resource IS in backend
	existing, lookupErr := ctrl.Lookup(ctx, server.URL)
	require.NoError(t, lookupErr, "resource should be in backend after ErrNotReady")
	require.NotNil(t, existing, "resource should exist in backend")
}

// TestAdd_does_not_return_err_not_ready_when_registration_fails tests that ErrNotReady
// is NOT returned when registration fails (before Ready() is called)
func TestAdd_does_not_return_err_not_ready_when_registration_fails(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	// Add first resource
	resource1, err := httprc.NewResource[map[string]string](
		server.URL,
		httprc.JSONTransformer[map[string]string](),
	)
	require.NoError(t, err)
	require.NoError(t, ctrl.Add(ctx, resource1, httprc.WithWaitReady(false)))

	// Try to add duplicate
	resource2, err := httprc.NewResource[map[string]string](
		server.URL, // Same URL
		httprc.JSONTransformer[map[string]string](),
	)
	require.NoError(t, err)

	err = ctrl.Add(ctx, resource2)

	// Assert: Should NOT be ErrNotReady (registration failed before Ready)
	require.Error(t, err, "expected error for duplicate URL")
	require.NotErrorIs(t, err, httprc.ErrNotReady(), "should not return ErrNotReady for registration failure")
}

// TestAdd_with_wait_ready_false_never_returns_err_not_ready tests that WithWaitReady(false)
// never returns ErrNotReady because Ready() is not called
func TestAdd_with_wait_ready_false_never_returns_err_not_ready(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{invalid json`))
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	resource, err := httprc.NewResource[map[string]any](
		server.URL,
		httprc.JSONTransformer[map[string]any](),
	)
	require.NoError(t, err)

	// Act: Add with WithWaitReady(false)
	err = ctrl.Add(ctx, resource, httprc.WithWaitReady(false))

	// Assert: Should succeed (no error) because we didn't wait for Ready
	require.NoError(t, err, "expected no error with WithWaitReady(false)")

	// Verify resource is in backend
	existing, lookupErr := ctrl.Lookup(ctx, server.URL)
	require.NoError(t, lookupErr, "resource should be in backend")
	require.NotNil(t, existing, "expected resource in backend")

	// Verify Ready() will fail separately
	readyCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	readyErr := resource.Ready(readyCtx)
	require.Error(t, readyErr, "expected Ready() to fail with invalid JSON")
}

// TestErrNotReady_wraps_underlying_error tests that ErrNotReady wraps the
// underlying error using Go 1.20+ multi-error wrapping
func TestErrNotReady_wraps_underlying_error(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second) // Longer than context timeout
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	resource, err := httprc.NewResource[map[string]string](
		server.URL,
		httprc.JSONTransformer[map[string]string](),
	)
	require.NoError(t, err)

	// Add with short timeout
	addCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	err = ctrl.Add(addCtx, resource)

	// Assert: Should be ErrNotReady wrapping DeadlineExceeded
	require.ErrorIs(t, err, httprc.ErrNotReady(), "expected ErrNotReady")
	require.ErrorIs(t, err, context.DeadlineExceeded, "expected wrapped DeadlineExceeded")
}

// TestAdd_whitelist_blocked_not_err_not_ready tests that whitelist blocking
// returns a non-ErrNotReady error
func TestAdd_whitelist_blocked_not_err_not_ready(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cl := httprc.NewClient(httprc.WithWhitelist(httprc.WhitelistFunc(func(_ string) bool {
		return false // Block everything
	})))
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	resource, err := httprc.NewResource[map[string]any](
		"https://example.com",
		httprc.JSONTransformer[map[string]any](),
	)
	require.NoError(t, err)

	err = ctrl.Add(ctx, resource)

	// Assert: Should return whitelist error, not ErrNotReady
	require.Error(t, err, "expected whitelist error")
	require.NotErrorIs(t, err, httprc.ErrNotReady(), "whitelist blocking should not return ErrNotReady")
	require.ErrorIs(t, err, httprc.ErrBlockedByWhitelist(), "expected ErrBlockedByWhitelist")
}

// TestAdd_context_cancelled_before_send_backend_not_err_not_ready tests that context
// cancellation before registration completes returns non-ErrNotReady
func TestAdd_context_cancelled_before_send_backend_not_err_not_ready(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	cl := httprc.NewClient()
	ctrl, err := cl.Start(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	resource, err := httprc.NewResource[map[string]any](
		"https://example.com",
		httprc.JSONTransformer[map[string]any](),
	)
	require.NoError(t, err)

	err = ctrl.Add(ctx, resource)

	// Assert: Should return context error, not ErrNotReady
	require.Error(t, err, "expected context error")
	require.NotErrorIs(t, err, httprc.ErrNotReady(), "context cancellation should not return ErrNotReady")
	require.ErrorIs(t, err, context.Canceled, "expected context.Canceled")
}

// TestAdd_retry_logic_distinguishes_errors tests that retry logic can properly
// distinguish between registration failures and ErrNotReady
func TestAdd_retry_logic_distinguishes_errors(t *testing.T) {
	t.Parallel()

	// Server returns valid data after 2 seconds
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		if callCount == 1 {
			time.Sleep(2 * time.Second) // First call times out
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	// First attempt: times out
	resource1, err := httprc.NewResource[map[string]string](
		server.URL,
		httprc.JSONTransformer[map[string]string](),
	)
	require.NoError(t, err)

	ctx1, cancel1 := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel1()

	err1 := ctrl.Add(ctx1, resource1)
	require.ErrorIs(t, err1, httprc.ErrNotReady(), "first attempt should return ErrNotReady")

	// Verify resource is in backend - should NOT retry Add()
	existing, lookupErr := ctrl.Lookup(ctx, server.URL)
	require.NoError(t, lookupErr, "resource should be in backend")
	require.NotNil(t, existing)

	// Second attempt: should fail with duplicate URL (correct behavior)
	resource2, err := httprc.NewResource[map[string]string](
		server.URL,
		httprc.JSONTransformer[map[string]string](),
	)
	require.NoError(t, err)

	err2 := ctrl.Add(ctx, resource2)
	require.Error(t, err2, "second Add should fail with duplicate URL")
	require.NotErrorIs(t, err2, httprc.ErrNotReady(), "duplicate URL should not return ErrNotReady")

	// Instead, wait for existing resource to be ready
	ctx3, cancel3 := context.WithTimeout(ctx, 5*time.Second)
	defer cancel3()

	err3 := existing.Ready(ctx3)
	require.NoError(t, err3, "existing resource should become ready")
}

// controlledHandler implements a deterministic HTTP handler for testing
// retry logic. It uses an atomic counter to control exactly when to
// transition from failure (invalid JSON) to success (valid JSON).
// See DESIGN_SYNC_TEST.md for detailed design rationale.
type controlledHandler struct {
	failuresRemaining atomic.Int32
}

func (h *controlledHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)

	// Atomically decrement and check if we should still fail
	if remaining := h.failuresRemaining.Add(-1); remaining >= 0 {
		// Still have failures remaining, return invalid JSON
		w.Write([]byte(`{invalid json`))
	} else {
		// No more failures needed, return valid JSON
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// TestIntegration_invalid_json_background_retry tests the end-to-end flow
// when a server initially returns invalid JSON, then valid JSON on retry
func TestIntegration_invalid_json_background_retry(t *testing.T) {
	t.Parallel()

	// Test configuration
	const (
		minInterval = 100 * time.Millisecond
		timeout     = 500 * time.Millisecond
	)

	// Validate preconditions
	require.Greater(t, timeout, minInterval,
		"timeout must be greater than minInterval to allow at least one fetch")

	// Setup controlled server
	// Calculate failures needed with 2x safety margin to guarantee timeout
	// Formula: (timeout / minInterval) * 2
	// Note: Integer division truncates, making this calculation conservative
	// Example: (500ms / 100ms) * 2 = 5 * 2 = 10 failures
	failuresNeeded := int32((timeout / minInterval) * 2)

	handler := &controlledHandler{}
	handler.failuresRemaining.Store(failuresNeeded)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	ctx := context.Background()
	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	resource, err := httprc.NewResource[map[string]string](
		server.URL,
		httprc.JSONTransformer[map[string]string](),
		httprc.WithMinInterval(minInterval),
	)
	require.NoError(t, err)

	// Add resource with timeout
	// Server is configured to fail enough times that timeout will fire
	addCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	err = ctrl.Add(addCtx, resource)

	// Assert ErrNotReady returned
	// Guaranteed because server will keep failing until we change the counter
	require.ErrorIs(t, err, httprc.ErrNotReady(), "expected ErrNotReady")

	// Verify resource is in backend (registration succeeded)
	existing, lookupErr := ctrl.Lookup(ctx, server.URL)
	require.NoError(t, lookupErr, "resource should be in backend")
	require.NotNil(t, existing, "resource should exist")

	// Allow server to succeed on next fetch
	// Safe to modify counter now because:
	// - Previous fetch failed (we got ErrNotReady)
	// - Next fetch won't start until minInterval elapses
	// - No concurrent access to the counter at this moment
	handler.failuresRemaining.Store(0)

	// Wait for background retry to make resource ready
	// The next fetch attempt will succeed and close the ready channel
	readyCtx, readyCancel := context.WithTimeout(ctx, 5*time.Second)
	defer readyCancel()

	err = resource.Ready(readyCtx)
	require.NoError(t, err, "resource should become ready after background retry")

	// Verify data is available
	var data map[string]string
	err = resource.Get(&data)
	require.NoError(t, err, "should be able to get data")
	require.Equal(t, "ok", data["status"], "data should have correct status")
}

// TestIntegration_multiple_ready_calls_after_err_not_ready tests that multiple
// Ready() calls work correctly after ErrNotReady
func TestIntegration_multiple_ready_calls_after_err_not_ready(t *testing.T) {
	t.Parallel()

	// Setup: Server returns invalid JSON first, then valid JSON
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		if callCount == 1 {
			w.Write([]byte(`{invalid json`)) // First call: invalid
		} else {
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		}
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	resource, err := httprc.NewResource[map[string]string](
		server.URL,
		httprc.JSONTransformer[map[string]string](),
		httprc.WithMinInterval(100*time.Millisecond),
	)
	require.NoError(t, err)

	// First Add() - returns ErrNotReady
	ctx1, cancel1 := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel1()

	err1 := ctrl.Add(ctx1, resource)
	require.ErrorIs(t, err1, httprc.ErrNotReady(), "expected ErrNotReady")

	// Verify resource is in backend
	existing, lookupErr := ctrl.Lookup(ctx, server.URL)
	require.NoError(t, lookupErr, "resource should be in backend")
	require.NotNil(t, existing)

	// Call Ready() again - should eventually succeed after backend retries
	ctx2, cancel2 := context.WithTimeout(ctx, 5*time.Second)
	defer cancel2()

	err2 := resource.Ready(ctx2)
	require.NoError(t, err2, "second Ready() should succeed after backend retry")

	// Verify data is now available
	var data1 map[string]string
	err = resource.Get(&data1)
	require.NoError(t, err, "should be able to get data")
	require.Equal(t, "ok", data1["status"])

	// Subsequent Ready() calls should succeed immediately
	ctx3, cancel3 := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel3()

	err3 := existing.Ready(ctx3)
	require.NoError(t, err3, "subsequent Ready() should succeed immediately")

	// Data should still be available
	var data2 map[string]string
	err = existing.Get(&data2)
	require.NoError(t, err, "should still be able to get data")
	require.Equal(t, "ok", data2["status"])
}
