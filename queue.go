package httprc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"time"
)

type rqentry struct {
	fireAt time.Time
	url    string
}

// entry represents a resource to be fetched over HTTP,
// long with optional specifications such as the *http.Client
// object to use.
type entry struct {
	sem chan struct{}

	lastFetch time.Time

	// Interval between refreshes are calculated two ways.
	// 1) You can set an explicit refresh interval by using WithRefreshInterval().
	//    In this mode, it doesn't matter what the HTTP response says in its
	//    Cache-Control or Expires headers
	// 2) You can let us calculate the time-to-refresh based on the key's
	//    Cache-Control or Expires headers.
	//    First, the user provides us the absolute minimum interval before
	//    refreshes. We will never check for refreshes before this specified
	//    amount of time.
	//
	//    Next, max-age directive in the Cache-Control header is consulted.
	//    If `max-age` is not present, we skip the following section, and
	//    proceed to the next option.
	//    If `max-age > user-supplied minimum interval`, then we use the max-age,
	//    otherwise the user-supplied minimum interval is used.
	//
	//    Next, the value specified in Expires header is consulted.
	//    If the header is not present, we skip the following seciont and
	//    proceed to the next option.
	//    We take the time until expiration `expires - time.Now()`, and
	//    if `time-until-expiration > user-supplied minimum interval`, then
	//    we use the expires value, otherwise the user-supplied minimum interval is used.
	//
	//    If all of the above fails, we used the user-supplied minimum interval
	refreshInterval time.Duration

	request *FetchRequest
	data    interface{}
}

func (e *entry) acquireSem() {
	e.sem <- struct{}{}
}

func (e *entry) releaseSem() {
	<-e.sem
}

func (e *entry) hasBeenFetched() bool {
	return !e.lastFetch.IsZero()
}

// Queue is responsible for updating the contents of the storage
type Queue struct {
	mu         sync.RWMutex
	registry   map[string]*entry
	windowSize time.Duration
	fetch      Fetcher
	fetchCond  *sync.Cond
	fetchQueue []*rqentry

	// list is a sorted list of urls to their expected fire time
	// when we get a new tick in the RQ loop, we process everything
	// that can be fired up to the point the tick was called
	list []*rqentry
}

func NewQueue(ctx context.Context, window time.Duration, fetch Fetcher) *Queue {
	fetchLocker := &sync.Mutex{}
	rq := &Queue{
		windowSize: window,
		fetch:      fetch,
		fetchCond:  sync.NewCond(fetchLocker),
	}

	go rq.refreshLoop(ctx)

	return rq
}

func (q *Queue) Register(u string, options ...RegisterOption) {
	var refreshInterval time.Duration
	var httpcl HTTPClient
	for _, option := range options {
		//nolint:forcetypeassert
		switch option.Ident() {
		case identHTTPClient{}:
			httpcl = option.Value().(HTTPClient)
		case identRefreshInterval{}:
			refreshInterval = option.Value().(time.Duration)
		}
	}

	e := entry{
		sem:             make(chan struct{}, 1),
		refreshInterval: refreshInterval,
		request: &FetchRequest{
			Client: httpcl,
			URL:    u,
		},
	}
	q.mu.Lock()
	q.registry[u] = &e
	q.mu.Unlock()
}

func (q *Queue) getRegistered(u string) (*entry, bool) {
	q.mu.RLock()
	e, ok := q.registry[u]
	q.mu.RUnlock()

	return e, ok
}

func (q *Queue) IsRegistered(u string) bool {
	_, ok := q.getRegistered(u)
	return ok
}

func (q *Queue) fetchLoop(ctx context.Context) {
	for {
		q.fetchCond.L.Lock()
		for len(q.fetchQueue) <= 0 {
			q.fetchCond.Wait()
		}
		list := make([]*rqentry, len(q.fetchQueue))
		copy(list, q.fetchQueue)
		q.fetchQueue = q.fetchQueue[:0]
		q.fetchCond.L.Unlock()

		select {
		case <-ctx.Done():
			return
		default:
		}

		for _, rq := range list {
			e, ok := q.getRegistered(rq.url)
			if !ok {
				continue
			}
			q.fetchAndStore(ctx, e)
		}
	}
}

// This loop is responsible for periodically updating the cached content
func (q *Queue) refreshLoop(ctx context.Context) {
	// Tick every q.windowSize duration.
	ticker := time.NewTicker(q.windowSize)

	go q.fetchLoop(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			// To avoid getting stuck here, we just copy the relevant
			// items, and release the lock within this critical section
			var list []*rqentry
			q.mu.Lock()
			var max int
			for i, r := range q.list {
				max = i
				if r.fireAt.Before(t) || r.fireAt.Equal(t) {
					list = append(list, r)
					continue
				}
				break
			}
			// TODO: need to explicitly truncate every so often?
			q.list = q.list[max+1:]
			q.mu.Unlock() // release lock

			// Now we need to fetch these, but do this elsewhere so
			// that we don't block this main loop
			q.fetchCond.L.Lock()
			q.fetchQueue = append(q.fetchQueue, list...)
			q.fetchCond.L.Unlock()
		}
	}
}

func (q *Queue) fetchAndStore(ctx context.Context, e *entry) error {
	// synchronously go fetch
	e.lastFetch = time.Now()
	res, err := q.fetch.Fetch(ctx, e.request)
	if err != nil {
		// Even if the request failed, we need to queue the next fetch
		// TODO: queue next fetch
		<-e.sem
		return fmt.Errorf(`failed to fetch %q: %w`, e.request.URL, err)
	}

	// TODO: queue next fetch
	buf, err := io.ReadAll(res.Body)
	defer res.Body.Close()
	if err != nil {
		<-e.sem
		return fmt.Errorf(`failed to read body for %q: %w`, e.request.URL, err)
	}
	e.data = buf
	return nil
}

func (q *Queue) Enqueue(u string, interval time.Duration) error {
	fireAt := time.Now().Add(interval)

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
				list = append(append(list[:i], &rqentry{fireAt: fireAt, url: u}), list[i:]...)
				break
			}
		}
	}

	q.list = list
	return nil
}

func (q Queue) MarshalJSON() ([]byte, error) {
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
	log.Printf("---->%q", buf.String())
	return buf.Bytes(), nil
}
