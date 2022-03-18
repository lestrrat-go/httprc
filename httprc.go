//go:generate tools/genoptions.sh

// Package httprc implements a cache for resources available
// over http(s). Its aim is not only to cache these resources so
// that it saves on HTTP roundtrips, but it also periodically
// attempts to auto-refresh these resources once they are cached
// based on the user-specified intervals and HTTP `Expires` and
// `Cache-Control` headers, thus keeping the entries _relatively_ fresh.
package httprc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type HTTPClient interface {
	Get(string) (*http.Response, error)
}

type Cache interface {
	// Configure is responsible for configuring the options that should
	// be applied when fetching the remote resource.
	// Unlike a traditional cache, the http2ud.Cache requires that entries
	// be configured before hand. It will be an error if an unregistered
	// url is requested in Get()
	Register(string, ...RegisterOption) error
	Get(context.Context, string) (interface{}, error)
	// Purge(string) error
	// Cached(string) bool
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

	data interface{}
}

type memory struct {
	muStorage sync.RWMutex

	// rq *RefreshQueue

	storage map[string]*entry
	fetcher Fetcher
}

func NewMemory() Cache {
	return &memory{
		storage: make(map[string]*entry),
	}
}

func (c *memory) Register(u string, options ...RegisterOption) error {
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

	c.muStorage.Lock()
	c.storage[u] = &e
	c.muStorage.Unlock()
	return nil
}

func (c *memory) getRegistered(u string) (*entry, bool) {
	c.muStorage.RLock()
	entry, ok := c.storage[u]
	c.muStorage.RUnlock()

	return entry, ok
}

func (c *memory) Get(ctx context.Context, u string) (interface{}, error) {
	e, ok := c.getRegistered(u)
	if !ok {
		return nil, fmt.Errorf(`url %q is not registered (did you make sure to call Register() first?)`, u)
	}

	// Only one goroutine may enter this section.
	e.sem <- struct{}{}

	// has this entry been fetched?
	if e.lastFetch.IsZero() {
		// synchronously go fetch
		e.lastFetch = time.Now()
		res, err := c.fetcher.Fetch(ctx, e.request)
		if err != nil {
			// Even if the request failed, we need to queue the next fetch
			// TODO: queue next fetch
			<-e.sem
			return nil, fmt.Errorf(`failed to fetch %q: %w`, u, err)
		}

		// TODO: queue next fetch
		buf, err := io.ReadAll(res.Body)
		defer res.Body.Close()
		if err != nil {
			<-e.sem
			return nil, fmt.Errorf(`failed to read body for %q: %w`, u, err)
		}
		e.data = buf
	}

	<-e.sem

	return e.data, nil
}
