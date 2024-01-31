package httprc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/lestrrat-go/httpcc"
)

// ErrSink is an abstraction that allows users to consume errors
// produced while the cache queue is running.
type ErrSink interface {
	// Error accepts errors produced during the cache queue's execution.
	// The method should never block, otherwise the fetch loop may be
	// paused for a prolonged amount of time.
	Error(error)
}

type ErrSinkFunc func(err error)

func (f ErrSinkFunc) Error(err error) {
	f(err)
}

// Transformer is responsible for converting an HTTP response
// into an appropriate form of your choosing.
type Transformer interface {
	// Transform receives an HTTP response object, and should
	// return an appropriate object that suits your needs.
	//
	// If you happen to use the response body, you are responsible
	// for closing the body
	Transform(string, *http.Response) (interface{}, error)
}

type TransformFunc func(string, *http.Response) (interface{}, error)

func (f TransformFunc) Transform(u string, res *http.Response) (interface{}, error) {
	return f(u, res)
}

// BodyBytes is the default Transformer applied to all resources.
// It takes an *http.Response object and extracts the body
// of the response as `[]byte`
type BodyBytes struct{}

func (BodyBytes) Transform(_ string, res *http.Response) (interface{}, error) {
	buf, err := io.ReadAll(res.Body)
	defer res.Body.Close()
	if err != nil {
		return nil, fmt.Errorf(`failed to read response body: %w`, err)
	}

	return buf, nil
}

type rqentry struct {
	fireAt time.Time
	url    string
}

// queue is responsible for updating the contents of the storage
type queue struct {
	mu         sync.RWMutex
	registry   *registry
	windowSize time.Duration
	fetch      *fetcher
	fetchCond  *sync.Cond
	fetchQueue []*rqentry
	client     HTTPClient

	// list is a sorted list of urls to their expected fire time
	// when we get a new tick in the RQ loop, we process everything
	// that can be fired up to the point the tick was called
	list []*rqentry

	// clock is really only used by testing
	clock interface {
		Now() time.Time
	}
}

type clockFunc func() time.Time

func (cf clockFunc) Now() time.Time {
	return cf()
}

func newQueue(ctx context.Context, registry *registry, window time.Duration, fetch *fetcher, client HTTPClient, errSink ErrSink) *queue {
	if client == nil {
		client = http.DefaultClient
	}
	fetchLocker := &sync.Mutex{}
	rq := &queue{
		windowSize: window,
		fetch:      fetch,
		fetchCond:  sync.NewCond(fetchLocker),
		registry:   registry,
		clock:      clockFunc(time.Now),
		client:     client,
	}

	go rq.refreshLoop(ctx, errSink)

	return rq
}

func (q *queue) Register(u string, options ...RegisterOption) error {
	var refreshInterval time.Duration
	var client HTTPClient = q.client
	var wl Whitelist
	var transform Transformer = BodyBytes{}

	minRefreshInterval := 15 * time.Minute
	for _, option := range options {
		//nolint:forcetypeassert
		switch option.Ident() {
		case identHTTPClient{}:
			client = option.Value().(HTTPClient)
		case identRefreshInterval{}:
			refreshInterval = option.Value().(time.Duration)
		case identMinRefreshInterval{}:
			minRefreshInterval = option.Value().(time.Duration)
		case identTransformer{}:
			transform = option.Value().(Transformer)
		case identWhitelist{}:
			wl = option.Value().(Whitelist)
		}
	}

	q.mu.RLock()
	rWindow := q.windowSize
	q.mu.RUnlock()

	if refreshInterval > 0 && refreshInterval < rWindow {
		return fmt.Errorf(`refresh interval (%s) is smaller than refresh window (%s): this will not as expected`, refreshInterval, rWindow)
	}

	e := newEntry(minRefreshInterval, refreshInterval, transform, newFetchRequest(u, client, wl))
	q.registry.Register(u, e)
	return nil
}

func (q *queue) Unregister(u string) error {
	return q.registry.Unregister(u)
}

func (q *queue) IsRegistered(u string) bool {
	return q.registry.IsRegistered(u)
}

func (q *queue) getRegistered(u string) (*entry, bool) {
	return q.registry.getRegistered(u)
}

func (q *queue) fetchLoop(ctx context.Context, errSink ErrSink) {
	for {
		q.fetchCond.L.Lock()
		for len(q.fetchQueue) <= 0 {
			select {
			case <-ctx.Done():
				return
			default:
				q.fetchCond.Wait()
			}
		}
		list := make([]*rqentry, len(q.fetchQueue))
		copy(list, q.fetchQueue)
		q.fetchQueue = q.fetchQueue[:0]
		q.fetchCond.L.Unlock()

		for _, rq := range list {
			select {
			case <-ctx.Done():
				return
			default:
			}

			e, ok := q.registry.getRegistered(rq.url)
			if !ok {
				continue
			}
			if err := q.fetchAndStore(ctx, e); err != nil {
				if errSink != nil {
					errSink.Error(&RefreshError{
						URL: rq.url,
						Err: err,
					})
				}
			}
		}
	}
}

// This loop is responsible for periodically updating the cached content
func (q *queue) refreshLoop(ctx context.Context, errSink ErrSink) {
	// Tick every q.windowSize duration.
	ticker := time.NewTicker(q.windowSize)

	go q.fetchLoop(ctx, errSink)
	defer q.fetchCond.Signal()

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			t = t.Round(time.Second)
			// To avoid getting stuck here, we just copy the relevant
			// items, and release the lock within this critical section
			var list []*rqentry
			q.mu.Lock()
			var max int
			for i, r := range q.list {
				if r.fireAt.Before(t) || r.fireAt.Equal(t) {
					max = i
					list = append(list, r)
					continue
				}
				break
			}

			if len(list) > 0 {
				q.list = q.list[max+1:]
			}
			q.mu.Unlock() // release lock

			if len(list) > 0 {
				// Now we need to fetch these, but do this elsewhere so
				// that we don't block this main loop
				q.fetchCond.L.Lock()
				q.fetchQueue = append(q.fetchQueue, list...)
				q.fetchCond.L.Unlock()
				q.fetchCond.Signal()
			}
		}
	}
}

func (q *queue) fetchAndStore(ctx context.Context, e *entry) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// synchronously go fetch
	e.lastFetch = time.Now()
	res, err := q.fetch.fetch(ctx, e.request)
	if err != nil {
		// Even if the request failed, we need to queue the next fetch
		q.enqueueNextFetch(nil, e)
		return fmt.Errorf(`failed to fetch %q: %w`, e.request.url, err)
	}

	q.enqueueNextFetch(res, e)

	data, err := e.transform.Transform(e.request.url, res)
	if err != nil {
		return fmt.Errorf(`failed to transform HTTP response for %q: %w`, e.request.url, err)
	}
	e.data = data

	return nil
}

func (q *queue) Enqueue(u string, interval time.Duration) error {
	fireAt := q.clock.Now().Add(interval).Round(time.Second)

	q.mu.Lock()
	defer q.mu.Unlock()

	list := q.list

	ll := len(list)
	if ll == 0 || list[ll-1].fireAt.Before(fireAt) {
		list = append(list, &rqentry{
			fireAt: fireAt,
			url:    u,
		})
	} else {
		for i := 0; i < ll; i++ {
			if i == ll-1 || list[i].fireAt.After(fireAt) {
				// insert here
				list = append(list[:i+1], list[i:]...)
				list[i] = &rqentry{fireAt: fireAt, url: u}
				break
			}
		}
	}

	q.list = list
	return nil
}

func (q *queue) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(`{"list":[`)
	q.mu.RLock()
	for i, e := range q.list {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, `{"fire_at":%q,"url":%q}`, e.fireAt.Format(time.RFC3339), e.url)
	}
	q.mu.RUnlock()
	buf.WriteString(`]}`)
	return buf.Bytes(), nil
}

func (q *queue) enqueueNextFetch(res *http.Response, e *entry) {
	dur := calculateRefreshDuration(res, e)
	// TODO send to error sink
	_ = q.Enqueue(e.request.url, dur)
}

func calculateRefreshDuration(res *http.Response, e *entry) time.Duration {
	if e.refreshInterval > 0 {
		return e.refreshInterval
	}

	if res != nil {
		if v := res.Header.Get(`Cache-Control`); v != "" {
			dir, err := httpcc.ParseResponse(v)
			if err == nil {
				maxAge, ok := dir.MaxAge()
				if ok {
					resDuration := time.Duration(maxAge) * time.Second
					if resDuration > e.minRefreshInterval {
						return resDuration
					}
					return e.minRefreshInterval
				}
				// fallthrough
			}
			// fallthrough
		}

		if v := res.Header.Get(`Expires`); v != "" {
			expires, err := http.ParseTime(v)
			if err == nil {
				resDuration := time.Until(expires)
				if resDuration > e.minRefreshInterval {
					return resDuration
				}
				return e.minRefreshInterval
			}
			// fallthrough
		}
	}

	// Previous fallthroughs are a little redandunt, but hey, it's all good.
	return e.minRefreshInterval
}

type SnapshotEntry struct {
	URL         string      `json:"url"`
	Data        interface{} `json:"data"`
	LastFetched time.Time   `json:"last_fetched"`
}
type Snapshot struct {
	Entries []SnapshotEntry `json:"entries"`
}

// Snapshot returns the contents of the cache at the given moment.
func (q *queue) snapshot() *Snapshot {
	return q.registry.Snapshot()
}
