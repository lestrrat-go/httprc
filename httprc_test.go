package httprc_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/httprc/v3"
	"github.com/stretchr/testify/require"
)

func TestClient(t *testing.T) {
	type Hello struct {
		Hello string `json:"hello"`
	}

	start := time.Now()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=1")
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
	defer ctrl.Shutdown(time.Second)

	testcases := []struct {
		URL       string
		Create    func() (httprc.Resource, error)
		Expected  any
		Expected2 any
	}{
		{
			URL: srv.URL + "/json/helloptr",
			Create: func() (httprc.Resource, error) {
				return httprc.NewResource[*Hello](srv.URL+"/json/helloptr", httprc.JSONTransformer[*Hello]())
			},
			Expected:  &Hello{Hello: "world"},
			Expected2: &Hello{Hello: "world2"},
		},
		{
			URL: srv.URL + "/json/hello",
			Create: func() (httprc.Resource, error) {
				return httprc.NewResource[Hello](srv.URL+"/json/hello", httprc.JSONTransformer[Hello]())
			},
			Expected:  Hello{Hello: "world"},
			Expected2: Hello{Hello: "world2"},
		},
		{
			URL: srv.URL + "/json/hellomap",
			Create: func() (httprc.Resource, error) {
				return httprc.NewResource[map[string]interface{}](srv.URL+"/json/hellomap", httprc.JSONTransformer[map[string]interface{}]())
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

	time.Sleep(5 * time.Second)
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
			require.Equal(t, dst, expected, `r.Resource should return expected value`)
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
	defer ctrl.Shutdown(time.Second)

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
