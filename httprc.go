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
	"net/http"
	"sync"
	"time"
)

type HTTPClient interface {
	Get(string) (*http.Response, error)
}

type Cache struct {
	mu    sync.RWMutex
	queue *Queue

	// Configure is responsible for configuring the options that should
	// be applied when fetching the remote resource.
	// Unlike a traditional cache, the http2ud.Cache requires that entries
	// be configured before hand. It will be an error if an unregistered
	// url is requested in Get()
	//Register(string, ...RegisterOption) error
	//Get(context.Context, string) (interface{}, error)
	// Purge(string) error
	// Cached(string) bool
}

func New(ctx context.Context, options ...NewOption) *Cache {
	refreshWindow := 15 * time.Minute
	for _, option := range options {
		//nolint:forcetypeassert
		switch option.Ident() {
		case identRefreshWindow{}:
			refreshWindow = option.Value().(time.Duration)
		}
	}
	fetch := NewFetcher(ctx)
	queue := NewQueue(ctx, refreshWindow, fetch)

	return &Cache{
		queue: queue,
	}
}

func (c *Cache) Register(u string, options ...RegisterOption) error {
	c.mu.Lock()
	c.queue.Register(u, options...)
	c.mu.Unlock()
	return nil
}

func (c *Cache) Get(ctx context.Context, u string) (interface{}, error) {
	c.mu.RLock()
	e, ok := c.queue.getRegistered(u)
	if !ok {
		c.mu.RUnlock()
		return nil, fmt.Errorf(`url %q is not registered (did you make sure to call Register() first?)`, u)
	}
	c.mu.RUnlock()

	// Only one goroutine may enter this section.
	e.acquireSem()
	// has this entry been fetched?
	if !e.hasBeenFetched() {
		c.queue.fetchAndStore(ctx, e)
	}

	e.releaseSem()

	return e.data, nil
}
