package httprc

import (
	"fmt"
	"sync"
	"time"
)

// entry represents a resource to be fetched over HTTP,
// long with optional specifications such as the *http.Client
// object to use.
type entry struct {
	mu  sync.RWMutex
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

func newEntry(minRefreshInterval, refreshInterval time.Duration, transform Transformer, req *fetchRequest) *entry {
	return &entry{
		sem:                make(chan struct{}, 1),
		minRefreshInterval: minRefreshInterval,
		transform:          transform,
		refreshInterval:    refreshInterval,
		request:            req,
	}
}

func (e *entry) acquireSem() {
	e.sem <- struct{}{}
}

func (e *entry) releaseSem() {
	<-e.sem
}

func (e *entry) hasBeenFetched() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return !e.lastFetch.IsZero()
}

// registry stores the URLs that the cache can handle
type registry struct {
	mu      sync.RWMutex
	entries map[string]*entry
}

func newRegistry() *registry {
	return &registry{
		entries: make(map[string]*entry),
	}
}

func (r *registry) Register(u string, e *entry) {
	r.mu.Lock()
	r.entries[u] = e
	r.mu.Unlock()
}

func (r *registry) Unregister(u string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.entries[u]
	if !ok {
		return fmt.Errorf(`url %q has not been registered`, u)
	}
	delete(r.entries, u)
	return nil
}

func (r *registry) getRegistered(u string) (*entry, bool) {
	r.mu.RLock()
	e, ok := r.entries[u]
	r.mu.RUnlock()

	return e, ok
}

func (r *registry) IsRegistered(u string) bool {
	_, ok := r.getRegistered(u)
	return ok
}

func (r *registry) Snapshot() *Snapshot {
	r.mu.RLock()
	list := make([]SnapshotEntry, 0, len(r.entries))

	for url, e := range r.entries {
		list = append(list, SnapshotEntry{
			URL:         url,
			LastFetched: e.lastFetch,
			Data:        e.data,
		})
	}
	r.mu.RUnlock()

	return &Snapshot{
		Entries: list,
	}
}
