// Package httpu2d implements a cache for resources available
// over http(s). Its aim is not only to cache these resources so
// that it saves on HTTP roundtrips, but it also periodically
// attempts to auto-refresh these resources once they are cached
// based on the user-specified intervals and HTTP `Expires` and
// `Cache-Control` headers, thus keeping the entries _relatively_ fresh.
package httpu2d

import (
	"fmt"
	"sync"
)

type Cache interface {
	// Configure is responsible for configuring the options that should
	// be applied when fetching the remote resource.
	// Unlike a traditional cache, the http2ud.Cache requires that entries
	// be configured before hand. It will be an error if an unregistered
	// url is requested in Get()
	Register(string, ...CacheOption) error
	Get(string) ([]byte, error)
	Purge(string) error
	Cached(string) bool
}

// entry represents a resource to be fetched over HTTP,
// long with optional specifications such as the *http.Client
// object to use.
type entry struct {
}

type memory struct {
	muStorage sync.RWMutex
	storage   map[string]*Entry
}

func NewMemory() Cache {
	return &memory{
		storage: make(map[string]*entry),
	}
}

func (c *memory) getRegistered(u string) (*entry, bool) {
	c.muStorage.RLock()
	entry, ok := c.storage[u]
	c.muStorage.RUnlock()

	return entry, ok
}

func (c *memory) Get(u string) ([]byte, error) {
	entry, ok := c.getRegistered(u)
	if !ok {
		return fmt.Errorf(`url %q is not registered (did you make sure to call Register() first?)`, u)
	}

	// Only one goroutine may enter this section.
	entry.sem <- struct{}{}

	// has this entry been fetched?
	if entry.lastFecthed.IsZero() {
		// synchronously go fetch
	}

	<-entry.sem

	return entry.Data, nil
}
