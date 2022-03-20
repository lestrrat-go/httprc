package httprc

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"sync"
	"time"

	"github.com/lestrrat-go/httpcc"
)

// Transformer is responsible for converting an HTTP response
// into an appropriate form of your choosing.
type Transformer interface {
	// Transform receives an HTTP response object, and should
	// return an appropriate object that suits your needs.
	//
	// If you happen to use the response body, you are responsible
	// for closing the body
	Transform(*http.Response) (interface{}, error)
}

// BodyBytes is the default Transformer applied to all resources
type BodyBytes struct{}

func (BodyBytes) Transform(res *http.Response) (interface{}, error) {
	buf, err := ioutil.ReadAll(res.Body)
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
	refreshInterval    time.Duration
	minRefreshInterval time.Duration

	request *fetchRequest

	transform Transformer
	data      interface{}
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

// queue is responsible for updating the contents of the storage
type queue struct {
	mu         sync.RWMutex
	registry   map[string]*entry
	windowSize time.Duration
	fetch      *fetcher
	fetchCond  *sync.Cond
	fetchQueue []*rqentry

	// list is a sorted list of urls to their expected fire time
	// when we get a new tick in the RQ loop, we process everything
	// that can be fired up to the point the tick was called
	list []*rqentry
}

func newQueue(ctx context.Context, window time.Duration, fetch *fetcher) *queue {
	fetchLocker := &sync.Mutex{}
	rq := &queue{
		windowSize: window,
		fetch:      fetch,
		fetchCond:  sync.NewCond(fetchLocker),
		registry:   make(map[string]*entry),
	}

	go rq.refreshLoop(ctx)

	return rq
}

func (q *queue) Register(u string, options ...RegisterOption) {
	var refreshInterval time.Duration
	var httpcl HTTPClient
	var transform Transformer = BodyBytes{}

	minRefreshInterval := 15 * time.Minute
	for _, option := range options {
		//nolint:forcetypeassert
		switch option.Ident() {
		case identHTTPClient{}:
			httpcl = option.Value().(HTTPClient)
		case identRefreshInterval{}:
			refreshInterval = option.Value().(time.Duration)
		case identMinRefreshInterval{}:
			minRefreshInterval = option.Value().(time.Duration)
		case identTransformer{}:
			transform = option.Value().(Transformer)
		}
	}

	e := entry{
		sem:                make(chan struct{}, 1),
		minRefreshInterval: minRefreshInterval,
		transform:          transform,
		refreshInterval:    refreshInterval,
		request: &fetchRequest{
			Client: httpcl,
			URL:    u,
		},
	}
	q.mu.Lock()
	q.registry[u] = &e
	q.mu.Unlock()
}

func (q *queue) Unregister(u string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.registry[u]
	if !ok {
		return fmt.Errorf(`url %q has not been registered`, u)
	}
	delete(q.registry, u)
	return nil
}

func (q *queue) getRegistered(u string) (*entry, bool) {
	q.mu.RLock()
	e, ok := q.registry[u]
	q.mu.RUnlock()

	return e, ok
}

func (q *queue) IsRegistered(u string) bool {
	_, ok := q.getRegistered(u)
	return ok
}

func (q *queue) fetchLoop(ctx context.Context) {
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

			e, ok := q.getRegistered(rq.url)
			if !ok {
				continue
			}
			q.fetchAndStore(ctx, e)
		}
	}
}

// This loop is responsible for periodically updating the cached content
func (q *queue) refreshLoop(ctx context.Context) {
	// Tick every q.windowSize duration.
	ticker := time.NewTicker(q.windowSize)

	go q.fetchLoop(ctx)
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
	// synchronously go fetch
	e.lastFetch = time.Now()
	res, err := q.fetch.Fetch(ctx, e.request)
	if err != nil {
		// Even if the request failed, we need to queue the next fetch
		q.enqueueNextFetch(nil, e)
		return fmt.Errorf(`failed to fetch %q: %w`, e.request.URL, err)
	}

	q.enqueueNextFetch(res, e)

	data, err := e.transform.Transform(res)
	if err != nil {
		return fmt.Errorf(`failed to transform HTTP response for %q: %w`, e.request.URL, err)
	}
	e.data = data

	return nil
}

func (q *queue) Enqueue(u string, interval time.Duration) error {
	fireAt := time.Now().Add(interval).Round(time.Second)

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

func (q queue) MarshalJSON() ([]byte, error) {
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
	q.Enqueue(e.request.URL, dur)
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
