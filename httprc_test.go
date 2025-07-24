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
				r, err := httprc.NewResource[map[string]interface{}](srv.URL+"/json/hellomap", httprc.JSONTransformer[map[string]interface{}]())
				if err != nil {
					return nil, err
				}
				r.SetMinInterval(time.Second)
				return r, nil
			},
			Expected:  map[string]interface{}{"hello": "world"},
			Expected2: map[string]interface{}{"hello": "world2"},
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

			var dst interface{}
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

			var dst interface{}
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
		json.NewEncoder(w).Encode(map[string]interface{}{"count": count})
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
	for i := 0; i < 10; i++ {
		err = ctrl.Refresh(ctx, server.URL)
		require.NoError(t, err, "refresh should succeed on iteration %d", i)
		
		// Verify we can lookup the resource
		res, err := ctrl.Lookup(ctx, server.URL)
		require.NoError(t, err, "lookup should succeed")
		require.NotNil(t, res, "resource should not be nil")
	}
}
