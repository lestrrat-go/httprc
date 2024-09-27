package httprc

import (
	"context"
	"slices"
	"sync"
	"time"
)

type Controller interface {
	AddResource(Resource) error
	RemoveResource(string) error
	Refresh(string) error
	ShutdownContext(context.Context) error
	Shutdown(time.Duration) error
}

type controller struct {
	cancel context.CancelFunc
	check  *time.Ticker
	// incoming accepts new control requests from external sources
	incoming chan ctrlRequest
	// outgoing sends Syncer objects to the worker pool
	outgoing chan Resource

	syncoutgoing chan synchronousRequest
	items        []Resource
	tickDuration time.Duration
	shutdown     chan struct{}
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

const (
	addResource = iota
	rmResource
	refreshResource
)

type ctrlRequest struct {
	op       int
	reply    chan error
	resource Resource
	u        string
}

// AddResource adds a new resource to the controller. If the resource already
// exists, it will return an error.
func (c *controller) AddResource(r Resource) error {
	reply := make(chan error, 1)
	c.incoming <- ctrlRequest{
		op:       addResource,
		reply:    reply,
		resource: r,
	}
	return <-reply
}

// RemoveResource removes a resource from the controller. If the resource does
// not exist, it will return an error.
func (c *controller) RemoveResource(u string) error {
	reply := make(chan error, 1)
	c.incoming <- ctrlRequest{
		op:    rmResource,
		reply: reply,
		u:     u,
	}
	return <-reply
}

// Refresh forces a resource to be refreshed immediately. If the resource does
// not exist, or if the refresh fails, it will return an error.
//
// This function is synchronous, and will block until the resource has been refreshed.
func (c *controller) Refresh(u string) error {
	reply := make(chan error, 1)
	c.incoming <- ctrlRequest{
		op:    refreshResource,
		reply: reply,
		u:     u,
	}
	return <-reply
}

func (c *controller) handleRequest(ctx context.Context, req ctrlRequest) {
	switch req.op {
	case addResource:
		r := req.resource
		for _, item := range c.items {
			if item.URL() == r.URL() {
				// Already exists
				sendReply(ctx, req.reply, errResourceAlreadyExists)
				return
			}
		}

		c.items = append(c.items, r)
		sendReply(ctx, req.reply, nil)

		// force the next check to happen immediately
		if d := r.ConstantInterval(); d > 0 {
			c.tickDuration = d
		} else if d := r.MinimumInterval(); d > 0 {
			c.tickDuration = d
		}

		c.check.Reset(time.Nanosecond)
	case rmResource:
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
		sendReply(ctx, req.reply, nil)
		c.check.Reset(minInterval)
	case refreshResource:
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
	}
}

func sendWorker(ctx context.Context, ch chan Resource, r Resource) {
	r.SetBusy(false)
	select {
	case <-ctx.Done():
	case ch <- r:
	}
}

func sendWorkerSynchronous(ctx context.Context, ch chan synchronousRequest, r synchronousRequest) {
	select {
	case <-ctx.Done():
	case ch <- r:
	}
}

func sendReply(ctx context.Context, ch chan error, err error) {
	defer close(ch)
	if err == nil {
		return
	}

	select {
	case <-ctx.Done():
	case ch <- err:
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
