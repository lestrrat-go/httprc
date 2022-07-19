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

type dummyFetcher struct {
	srv *httptest.Server
}

func (f *dummyFetcher) Fetch(_ context.Context, _ string, _ ...FetchOption) (*http.Response, error) {
	panic("unimplemented")
}

// URLs must be for f.srv
func (f *dummyFetcher) fetch(_ context.Context, fr *fetchRequest) (*http.Response, error) {
	//nolint:noctx
	return f.srv.Client().Get(fr.url)
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

	q := newQueue(ctx, 15*time.Minute, &dummyFetcher{srv: srv}, &noErrorSink{t: t})

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
