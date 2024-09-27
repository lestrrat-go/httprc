package httprc

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"
)

type Controller interface {
	// Add adds a new `http.Resource` to the controller. If the resource already exists,
	// it will return an error.
	Add(context.Context, Resource) error

	// Lookup a `httprc.Resource` by its URL. If the resource does not exist, it
	// will return an error.
	Lookup(context.Context, string) (Resource, error)

	// Remove a `httprc.Resource` from the controller by its URL. If the resource does
	// not exist, it will return an error.
	Remove(context.Context, string) error

	// Refresh forces a resource to be refreshed immediately. If the resource does
	// not exist, or if the refresh fails, it will return an error.
	Refresh(context.Context, string) error

	ShutdownContext(context.Context) error
	Shutdown(time.Duration) error
}

type controller struct {
	cancel context.CancelFunc
	check  *time.Ticker
	// incoming accepts new control requests from external sources
	incoming chan any
	// outgoing sends Syncer objects to the worker pool
	outgoing chan Resource

	traceSink TraceSink

	syncoutgoing chan synchronousRequest
	items        []Resource
	tickDuration time.Duration
	shutdown     chan struct{}

	wl Whitelist
}

// Shutdown is a convenience function that calls ShutdownContext with a
// context that has a timeout of `timeout`.
func (c *controller) Shutdown(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return c.ShutdownContext(ctx)
}

// ShutdownContext stops the client and all associated goroutines, and waits for them
// to finish. If the context is canceled, the function will return immediately:
// there fore you should not use the context you used to start the client (because
// presumably it's already canceled).
//
// Waiting for the client shutdown will also ensure that all sinks are properly
// flushed.
func (c *controller) ShutdownContext(ctx context.Context) error {
	c.cancel()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.shutdown:
		return nil
	}
}

type ctrlRequest[T any] struct {
	reply    chan T
	resource Resource
	u        string
}
type lookupReply struct {
	r   Resource
	err error
}

type addRequest ctrlRequest[error]
type rmRequest ctrlRequest[error]
type refreshRequest ctrlRequest[error]
type lookupRequest ctrlRequest[lookupReply]

// Lookup returns a resource by its URL. If the resource does not exist, it
// will return an error.
//
// Unfortunately, due to the way typed parameters are handled in Go, we can only
// return a Resource object (and not a ResourceBase[T] object). This means that
// you will either need to use the `Resource.Get()` method or use a type
// assertion to obtain a `ResourceBase[T]` to get to the actual object you are
// looking for
func (c *controller) Lookup(ctx context.Context, u string) (Resource, error) {
	reply := make(chan lookupReply, 1)
	req := lookupRequest{
		reply: reply,
		u:     u,
	}
	select {
	case c.incoming <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-reply:
		return r.r, r.err
	}
}

// Add adds a new resource to the controller. If the resource already
// exists, it will return an error.
func (c *controller) Add(ctx context.Context, r Resource) error {
	if !c.wl.IsAllowed(r.URL()) {
		return fmt.Errorf(`httprc.Controller.AddResource: cannot add %q: %w`, r.URL(), errBlockedByWhitelist)
	}

	reply := make(chan error, 1)
	req := addRequest{
		reply:    reply,
		resource: r,
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.incoming <- req:
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-reply:
		return err
	}
}

// Remove removes a resource from the controller. If the resource does
// not exist, it will return an error.
func (c *controller) Remove(ctx context.Context, u string) error {
	reply := make(chan error, 1)
	req := rmRequest{
		reply: reply,
		u:     u,
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.incoming <- req:
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-reply:
		return err
	}
}

// Refresh forces a resource to be refreshed immediately. If the resource does
// not exist, or if the refresh fails, it will return an error.
//
// This function is synchronous, and will block until the resource has been refreshed.
func (c *controller) Refresh(ctx context.Context, u string) error {
	reply := make(chan error, 1)
	req := refreshRequest{
		reply: reply,
		u:     u,
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.incoming <- req:
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-reply:
		return err
	}
}

func (c *controller) handleRequest(ctx context.Context, req any) {
	switch req := req.(type) {
	case addRequest:
		r := req.resource
		for _, item := range c.items {
			if item.URL() == r.URL() {
				// Already exists
				sendReply(ctx, req.reply, errResourceAlreadyExists)
				return
			}
		}

		c.items = append(c.items, r)
		closeReply(req.reply)

		// force the next check to happen immediately
		if d := r.ConstantInterval(); d > 0 {
			c.tickDuration = d
		} else if d := r.MinimumInterval(); d > 0 {
			c.tickDuration = d
		}

		c.check.Reset(time.Nanosecond)
	case rmRequest:
		u := req.u
		minInterval := oneDay
		loc := -1
		for i, item := range c.items {
			if d := item.MinimumInterval(); d < minInterval {
				minInterval = d
			}

			if item.URL() == u {
				loc = i
			}
		}

		if loc < 0 {
			sendReply(ctx, req.reply, errResourceNotFound)
			return
		}

		c.items = slices.Delete(c.items, loc, loc+1)
		closeReply[error](req.reply)
		c.check.Reset(minInterval)
	case refreshRequest:
		u := req.u
		for _, item := range c.items {
			if item.URL() != u {
				continue
			}
			item.SetNext(time.Unix(0, 0))
			sendWorkerSynchronous(ctx, c.syncoutgoing, synchronousRequest{
				r:     item,
				reply: req.reply,
			})
			return
		}
		sendReply(ctx, req.reply, errResourceNotFound)
	case lookupRequest:
		u := req.u
		for _, item := range c.items {
			if item.URL() == u {
				sendReply(ctx, req.reply, lookupReply{r: item})
				return
			}
		}
		sendReply(ctx, req.reply, lookupReply{err: errResourceNotFound})
	}
}

func sendWorker(ctx context.Context, ch chan Resource, r Resource) {
	r.SetBusy(true)
	select {
	case <-ctx.Done():
	case ch <- r:
	}
}

func sendWorkerSynchronous(ctx context.Context, ch chan synchronousRequest, r synchronousRequest) {
	r.r.SetBusy(true)
	select {
	case <-ctx.Done():
	case ch <- r:
	}
}

func closeReply[T any](ch chan T) {
	close(ch)
}

func sendReply[T any](ctx context.Context, ch chan T, v T) {
	defer closeReply[T](ch)
	select {
	case <-ctx.Done():
	case ch <- v:
	}
}

func (c *controller) loop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case req := <-c.incoming:
			c.handleRequest(ctx, req)
		case t := <-c.check.C:
			// Always reset the ticker because the previous tick
			// could have arrived by way of a forced tick
			c.traceSink.Put(ctx, fmt.Sprintf("httprc controller: checking resources. Next check in %s", time.Now().Add(c.tickDuration)))
			c.check.Reset(c.tickDuration)
			for _, item := range c.items {
				if item.IsBusy() || item.Next().After(t) {
					continue
				}
				sendWorker(ctx, c.outgoing, item)
			}
		case <-ctx.Done():
			return
		}
	}
}
