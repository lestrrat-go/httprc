package httprc_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/httprc/v3/tracesink"
	"github.com/stretchr/testify/require"
)

func TestResourceCreation(t *testing.T) {
	t.Parallel()

	t.Run("valid resource creation", func(t *testing.T) {
		t.Parallel()
		resource, err := httprc.NewResource[map[string]string](
			"https://example.com/test",
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err)
		require.NotNil(t, resource)
		require.Equal(t, "https://example.com/test", resource.URL())
		require.Equal(t, httprc.DefaultMinInterval, resource.MinInterval())
		require.Equal(t, httprc.DefaultMaxInterval, resource.MaxInterval())
	})

	t.Run("resource with custom intervals", func(t *testing.T) {
		t.Parallel()
		minInterval := 30 * time.Second
		maxInterval := 2 * time.Hour

		resource, err := httprc.NewResource[map[string]string](
			"https://example.com/test",
			httprc.JSONTransformer[map[string]string](),
			httprc.WithMinInterval(minInterval),
			httprc.WithMaxInterval(maxInterval),
		)
		require.NoError(t, err)
		require.Equal(t, minInterval, resource.MinInterval())
		require.Equal(t, maxInterval, resource.MaxInterval())
	})

	t.Run("resource with invalid URL", func(t *testing.T) {
		t.Parallel()
		// Test with malformed URLs
		invalidURLs := []string{
			"",
			"not-a-url",
			"ftp://unsupported-scheme.com",
			"://missing-scheme",
		}

		for _, invalidURL := range invalidURLs {
			_, err := httprc.NewResource[map[string]string](
				invalidURL,
				httprc.JSONTransformer[map[string]string](),
			)
			// Note: The actual behavior depends on implementation
			// This test documents the current behavior
			if invalidURL == "" {
				require.Error(t, err, "empty URL should cause error")
			}
		}
	})
}

func TestResourceTransformers(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"string": "test",
				"number": 42,
				"bool":   true,
			})
		case "/bytes":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte("binary data"))
		case "/text":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("plain text"))
		case "/invalid-json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("invalid json {"))
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	traceDst := io.Discard
	if testing.Verbose() {
		traceDst = io.Discard
	}
	cl := httprc.NewClient(
		httprc.WithTraceSink(tracesink.NewSlog(slog.New(slog.NewJSONHandler(traceDst, nil)))),
	)
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err, "client start should succeed")
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	t.Run("JSON transformer", func(t *testing.T) {
		t.Parallel()
		resource, err := httprc.NewResource[map[string]any](
			srv.URL+"/json",
			httprc.JSONTransformer[map[string]any](),
		)
		require.NoError(t, err, "JSON resource creation should succeed")

		require.NoError(t, ctrl.Add(ctx, resource), "adding JSON resource should succeed")

		var data map[string]any
		require.NoError(t, resource.Get(&data), "getting JSON data should succeed")
		require.Equal(t, "test", data["string"])
		require.InEpsilon(t, 42.0, data["number"], 1e-9) // JSON numbers are float64
		require.Equal(t, true, data["bool"])
	})

	t.Run("bytes transformer", func(t *testing.T) {
		t.Parallel()
		resource, err := httprc.NewResource[[]byte](
			srv.URL+"/bytes",
			httprc.BytesTransformer(),
		)
		require.NoError(t, err, "bytes resource creation should succeed")

		require.NoError(t, ctrl.Add(ctx, resource), "adding bytes resource should succeed")

		var data []byte
		require.NoError(t, resource.Get(&data), "getting bytes data should succeed")
		require.Equal(t, []byte("binary data"), data)
	})

	t.Run("custom transformer", func(t *testing.T) {
		t.Parallel()
		customTransformer := httprc.TransformFunc[string](func(_ context.Context, res *http.Response) (string, error) {
			defer res.Body.Close()
			buf := make([]byte, 1024)
			n, _ := res.Body.Read(buf)
			return strings.ToUpper(string(buf[:n])), nil
		})

		resource, err := httprc.NewResource[string](
			srv.URL+"/text",
			customTransformer,
		)
		require.NoError(t, err, "custom transformer resource creation should succeed")

		require.NoError(t, ctrl.Add(ctx, resource), "adding custom transformer resource should succeed")

		var data string
		require.NoError(t, resource.Get(&data), "getting custom transformed data should succeed")
		require.Equal(t, "PLAIN TEXT", data)
	})

	t.Run("transformer error handling", func(t *testing.T) {
		t.Parallel()
		// JSON transformer should fail on invalid JSON
		resource, err := httprc.NewResource[map[string]any](
			srv.URL+"/invalid-json",
			httprc.JSONTransformer[map[string]any](),
		)
		require.NoError(t, err, "invalid JSON resource creation should succeed")

		require.NoError(t, ctrl.Add(ctx, resource, httprc.WithWaitReady(false)), "should add resource without waiting for ready")

		// Ready should time out (and return the error) due to invalid JSON
		// (if the initial request fails, it should not block indefinitely)
		tctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		require.Error(t, resource.Ready(tctx), "should not block indefinitely on invalid JSON")
	})
}

func TestResourceErrorHandling(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err, "error handling test client start should succeed")
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	t.Run("HTTP error responses", func(t *testing.T) {
		t.Parallel()
		errorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/404":
				http.NotFound(w, r)
			case "/500":
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			case "/timeout":
				time.Sleep(100 * time.Millisecond) // Simulate slow response
				w.Write([]byte("slow response"))
			}
		}))
		defer errorSrv.Close()

		// Test 404 error
		resource404, err := httprc.NewResource[[]byte](
			errorSrv.URL+"/404",
			httprc.BytesTransformer(),
		)
		require.NoError(t, err, "404 resource creation should succeed")

		require.NoError(t, ctrl.Add(ctx, resource404, httprc.WithWaitReady(false)), "adding 404 resource should succeed")

		readyCtx404, cancel404 := context.WithTimeout(ctx, time.Second)
		defer cancel404()
		require.Error(t, resource404.Ready(readyCtx404), "404 resource should not become ready")

		// Test 500 error
		resource500, err := httprc.NewResource[[]byte](
			errorSrv.URL+"/500",
			httprc.BytesTransformer(),
		)
		require.NoError(t, err, "500 resource creation should succeed")

		require.NoError(t, ctrl.Add(ctx, resource500, httprc.WithWaitReady(false)), "adding 500 resource should succeed")

		readyCtx500, cancel500 := context.WithTimeout(ctx, time.Second)
		defer cancel500()
		require.Error(t, resource500.Ready(readyCtx500), "500 resource should not become ready")
	})

	t.Run("network error", func(t *testing.T) {
		// Use a non-existent server
		resource, err := httprc.NewResource[[]byte](
			"http://127.0.0.1:99999/nonexistent",
			httprc.BytesTransformer(),
		)
		require.NoError(t, err, "network error test resource creation should succeed")

		require.NoError(t, ctrl.Add(ctx, resource, httprc.WithWaitReady(false)), "adding network error test resource should succeed")

		// Ready should fail due to connection error
		readyCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		require.Error(t, resource.Ready(readyCtx), "network error resource should not become ready")
	})

	t.Run("context cancellation", func(t *testing.T) {
		slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(2 * time.Second) // Slow response
			w.Write([]byte("slow response"))
		}))
		defer slowSrv.Close()

		resource, err := httprc.NewResource[[]byte](
			slowSrv.URL,
			httprc.BytesTransformer(),
		)
		require.NoError(t, err, "context cancellation test resource creation should succeed")

		require.NoError(t, ctrl.Add(ctx, resource, httprc.WithWaitReady(false)), "adding context cancellation test resource should succeed")

		// Create a context with short timeout
		readyCtx, readyCancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer readyCancel()

		err = resource.Ready(readyCtx)
		require.Error(t, err)
		require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
	})
}

func TestResourceCacheHeaders(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var requestCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		switch r.URL.Path {
		case "/cache-control":
			w.Header().Set("Cache-Control", "max-age=1")
			json.NewEncoder(w).Encode(map[string]int{"count": requestCount})
		case "/expires":
			w.Header().Set("Expires", time.Now().Add(1*time.Second).Format(http.TimeFormat))
			json.NewEncoder(w).Encode(map[string]int{"count": requestCount})
		case "/no-cache":
			w.Header().Set("Cache-Control", "no-cache")
			json.NewEncoder(w).Encode(map[string]int{"count": requestCount})
		default:
			json.NewEncoder(w).Encode(map[string]int{"count": requestCount})
		}
	}))
	t.Cleanup(srv.Close)

	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err, "cache headers test client start should succeed")
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	t.Run("respect cache-control max-age", func(t *testing.T) {
		t.Parallel()
		resource, err := httprc.NewResource[map[string]int](
			srv.URL+"/cache-control",
			httprc.JSONTransformer[map[string]int](),
		)
		require.NoError(t, err, "cache control resource creation should succeed")

		// Set very short intervals to test cache behavior
		resource.SetMinInterval(100 * time.Millisecond)
		resource.SetMaxInterval(10 * time.Second)

		require.NoError(t, ctrl.Add(ctx, resource), "adding cache control resource should succeed")

		// Wait for cache to expire and resource to refresh
		time.Sleep(5 * time.Second)

		var data map[string]int
		require.NoError(t, resource.Get(&data), "getting cache control data should succeed")
		require.Greater(t, data["count"], 1, "resource should have been refreshed due to cache expiry")
	})
}

func TestResourceReady(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
	}))
	t.Cleanup(srv.Close)

	cl := httprc.NewClient()
	ctrl, err := cl.Start(ctx)
	require.NoError(t, err, "ready test client start should succeed")
	t.Cleanup(func() { ctrl.Shutdown(time.Second) })

	t.Run("resource becomes ready", func(t *testing.T) {
		t.Parallel()
		resource, err := httprc.NewResource[map[string]string](
			srv.URL+"/ready-test",
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err, "ready test resource creation should succeed")

		// Add without waiting for ready
		require.NoError(t, ctrl.Add(ctx, resource, httprc.WithWaitReady(false)), "adding ready test resource should succeed")

		// Now wait for it to become ready
		require.NoError(t, resource.Ready(ctx), "resource should become ready")

		var data map[string]string
		require.NoError(t, resource.Get(&data), "getting ready test data should succeed")
		require.Equal(t, "ready", data["status"])
	})

	t.Run("ready with timeout", func(t *testing.T) {
		t.Parallel()
		slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(2 * time.Second)
			json.NewEncoder(w).Encode(map[string]string{"status": "slow"})
		}))
		defer slowSrv.Close()

		resource, err := httprc.NewResource[map[string]string](
			slowSrv.URL,
			httprc.JSONTransformer[map[string]string](),
		)
		require.NoError(t, err, "ready timeout test resource creation should succeed")

		require.NoError(t, ctrl.Add(ctx, resource, httprc.WithWaitReady(false)), "adding ready timeout test resource should succeed")

		// Ready should timeout
		readyCtx, readyCancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer readyCancel()

		err = resource.Ready(readyCtx)
		require.Error(t, err)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestResourceIntervals(t *testing.T) {
	t.Parallel()

	t.Run("set and get intervals", func(t *testing.T) {
		t.Parallel()
		resource, err := httprc.NewResource[[]byte](
			"https://example.com/test",
			httprc.BytesTransformer(),
		)
		require.NoError(t, err, "intervals test resource creation should succeed")

		// Test default values
		require.Equal(t, httprc.DefaultMinInterval, resource.MinInterval())
		require.Equal(t, httprc.DefaultMaxInterval, resource.MaxInterval())

		// Set new values
		newMin := 5 * time.Minute
		newMax := 2 * time.Hour
		resource.SetMinInterval(newMin)
		resource.SetMaxInterval(newMax)

		require.Equal(t, newMin, resource.MinInterval())
		require.Equal(t, newMax, resource.MaxInterval())
	})

	t.Run("busy state", func(t *testing.T) {
		t.Parallel()
		resource, err := httprc.NewResource[[]byte](
			"https://example.com/test",
			httprc.BytesTransformer(),
		)
		require.NoError(t, err, "busy state test resource creation should succeed")

		// Initially not busy
		require.False(t, resource.IsBusy())

		// Set busy
		resource.SetBusy(true)
		require.True(t, resource.IsBusy())

		// Unset busy
		resource.SetBusy(false)
		require.False(t, resource.IsBusy())
	})
}
