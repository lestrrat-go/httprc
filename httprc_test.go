package httprc_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/httprc"
	"github.com/stretchr/testify/assert"
)

func TestCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var called int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		t.Logf("HTTP handler")
		called++
		w.Header().Set(`Cache-Control`, fmt.Sprintf(`max-age=%d`, 3))
		w.WriteHeader(http.StatusOK)
	}))
	c := httprc.New(ctx, httprc.WithRefreshWindow(time.Second))

	c.Register(srv.URL, httprc.WithHTTPClient(srv.Client()), httprc.WithMinRefreshInterval(time.Second))
	if !assert.True(t, c.IsRegistered(srv.URL)) {
		return
	}

	for i := 0; i < 3; i++ {
		_, err := c.Get(ctx, srv.URL)
		if !assert.NoError(t, err, `c.Get should succeed`) {
			return
		}
	}
	if !assert.Equal(t, 1, called, `there should only be one fetch request`) {
		return
	}

	time.Sleep(4 * time.Second)
	for i := 0; i < 3; i++ {
		_, err := c.Get(ctx, srv.URL)
		if !assert.NoError(t, err, `c.Get should succeed`) {
			return
		}
	}
	if !assert.Equal(t, 2, called, `there should only be one fetch request`) {
		return
	}

	cancel()
}
