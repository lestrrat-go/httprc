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

// Cache represents a cache that stores resources locally, while
// periodically refreshing the contents based on HTTP header values
// and/or user-supplied hints.
//
// Refresh is performed _periodically_, and therefore the contents
// are not kept up-to-date in real time. The interval between checks
// for refreshes is called the refresh window.
//
// The default refresh window is 15 minutes. This means that if a
// resource is fetched is at time T, and it is supposed to be
// refreshed in 20 minutes, the next refresh for this resource will
// happen at T+30 minutes (15+15 minutes).
type Cache struct {
	mu    sync.RWMutex
	queue *queue
}

const defaultRefreshWindow = 15 * time.Minute

// New creates a new Cache object.
//
// The context object in the argument controls the life-span of the
// auto-refresh worker.
//
// Refresh will only be performed periodically where the interval between
// refreshes are controlled by the `refresh window` variable.
//
// The refresh window can be configured by using `httprc.WithRefreshWindow`
// option. If you want refreshes to be performed more often, provide a smaller
// refresh window. If you specify a refresh window that is smaller than 1
// second, it will automatically be set to the default value, which is 15
// minutes.
//
// Internally the HTTP fetching is done using a pool of HTTP fetch
// workers. The default number of workers is 3. You may change this
// number by specifying the `httprc.WithFetchWorkerCount`
func New(ctx context.Context, options ...ConstructorOption) *Cache {
	var refreshWindow time.Duration
	var errSink ErrSink
	var nfetchers int
	for _, option := range options {
		//nolint:forcetypeassert
		switch option.Ident() {
		case identRefreshWindow{}:
			refreshWindow = option.Value().(time.Duration)
		case identFetchWorkerCount{}:
			nfetchers = option.Value().(int)
		case identErrSink{}:
			errSink = option.Value().(ErrSink)
		}
	}

	if refreshWindow < time.Second {
		refreshWindow = defaultRefreshWindow
	}

	if nfetchers < 1 {
		nfetchers = 3
	}

	fetch := newFetcher(ctx, nfetchers)
	queue := newQueue(ctx, refreshWindow, fetch, errSink)

	return &Cache{
		queue: queue,
	}
}

// Register configures a URL to be stored in the cache.
//
// For any given URL, the URL must be registered _BEFORE_ it is
// accessed using `Get()` method.
func (c *Cache) Register(u string, options ...RegisterOption) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue.Register(u, options...)
	return nil
}

// Unregister removes the given URL `u` from the cache.
//
// Subsequent calls to `Get()` will fail until `u` is registered again.
func (c *Cache) Unregister(u string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.queue.Unregister(u)
}

// IsRegistered returns true if the given URL `u` has already been
// registered in the system.
func (c *Cache) IsRegistered(u string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.queue.IsRegistered(u)
}

// Get returns the cached object.
//
// The context.Context argument is used to control the timeout for
// synchronous fetches, when they need to happen. Synchronous fetches
// will be performed when the cache does not contain the specified
// resource.
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
		if err := c.queue.fetchAndStore(ctx, e); err != nil {
			return nil, fmt.Errorf(`failed to fetch %q: %w`, e.request.URL, err)
		}
	}

	e.releaseSem()

	e.mu.RLock()
	data := e.data
	e.mu.RUnlock()

	return data, nil
}
