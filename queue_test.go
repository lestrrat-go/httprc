package httprc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Sanity check for the queue portion

type dummyClient struct {
	srv *httptest.Server
}

func (c *dummyClient) Do(req *http.Request) (*http.Response, error) {
	return c.srv.Client().Do(req)
}

type noErrorSink struct {
	t *testing.T
}

func (s *noErrorSink) Error(err error) {
	s.t.Errorf(`unexpected error: %s`, err)
}

func TestQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	f := newFetcher(ctx, 0, InsecureWhitelist{})
	q := newQueue(ctx, newRegistry(), 15*time.Minute, f, &noErrorSink{t: t})

	base := time.Now()
	q.clock = clockFunc(func() time.Time {
		return base
	})

	// 0..4
	for i := 0; i < 4; i++ {
		q.Enqueue(fmt.Sprintf("%s/%d", srv.URL, i), time.Duration(i)*time.Second)
	}

	// 0..4, 6..9
	for i := 7; i < 10; i++ {
		q.Enqueue(fmt.Sprintf("%s/%d", srv.URL, i), time.Duration(i)*time.Second)
	}

	// 0...9
	for i := 4; i < 7; i++ {
		q.Enqueue(fmt.Sprintf("%s/%d", srv.URL, i), time.Duration(i)*time.Second)
	}

	var prevTm time.Time
	for i, rqe := range q.list {
		require.True(t, prevTm.Before(rqe.fireAt), `entry %d must have fireAt before %s (got %s)`, i, prevTm, rqe.fireAt)
		u := fmt.Sprintf("%s/%d", srv.URL, i)
		require.Equal(t, u, rqe.url, `entry %d must have url same as %s (got %s)`, i, u, rqe.url)

		prevTm = rqe.fireAt
	}
}
