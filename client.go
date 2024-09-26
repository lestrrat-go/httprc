package httprc

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Client is the main entry point for the httprc package.
type Client struct {
	mu         sync.Mutex
	numWorkers int
	running    bool
}

const DefaultWorkers = 5
const oneDay = 24 * time.Hour

func NewClient(options ...NewClientOption) *Client {
	numWorkers := DefaultWorkers
	//nolint:forcetypeassert
	for _, option := range options {
		switch option.Ident() {
		case identWorkers{}:
			numWorkers = option.Value().(int)
		}
	}

	if numWorkers <= 0 {
		numWorkers = 1
	}
	return &Client{
		numWorkers: numWorkers,
	}
}

// Controller It is responsible for accepting
// a set of Syncer objects and processing them periodically in resource-specific intervals.
type Controller struct {
	cancel context.CancelFunc
	check  *time.Ticker
	// incoming accepts new control requests from external sources
	incoming chan ctrlRequest
	// outgoing sends Syncer objects to the worker pool
	outgoing     chan Resource
	items        []Resource
	tickDuration time.Duration
}

func (c *Controller) Stop() {
	c.cancel()
}

const (
	addResource = iota
	rmResource
)

type ctrlRequest struct {
	op       int
	reply    chan error
	resource Resource
}

func (c *Controller) AddResource(r Resource) error {
	reply := make(chan error, 1)
	c.incoming <- ctrlRequest{
		op:       addResource,
		reply:    reply,
		resource: r,
	}
	return <-reply
}

func (c *Controller) RemoveResource(s Resource) {
	reply := make(chan error, 1)
	c.incoming <- ctrlRequest{
		op:       rmResource,
		reply:    reply,
		resource: s,
	}
	<-reply
}

func (c *Controller) handleRequest(req ctrlRequest) {
	defer close(req.reply)
	switch req.op {
	case addResource:
		r := req.resource
		for _, item := range c.items {
			if item.URL() == r.URL() {
				// Already exists
				req.reply <- errResourceAlreadyExists
				return
			}
		}

		c.items = append(c.items, r)
		req.reply <- nil

		// force the next check to happen immediately
		if d := r.ConstantInterval(); d > 0 {
			c.tickDuration = d
		} else if d := r.MinimumInterval(); d > 0 {
			c.tickDuration = d
		}

		c.check.Reset(time.Nanosecond)
	case rmResource:
		r := req.resource
		minInterval := oneDay
		loc := -1
		for i, item := range c.items {
			if d := item.MinimumInterval(); d < minInterval {
				minInterval = d
			}

			if item.URL() == r.URL() {
				loc = i
			}
		}

		if loc < 0 {
			req.reply <- errResourceNotFound
			return
		}

		c.items = slices.Delete(c.items, loc, loc+1)
		req.reply <- nil

		c.check.Reset(minInterval)
	}
}

func (c *Controller) loop(ctx context.Context) {
	for {
		select {
		case req := <-c.incoming:
			c.handleRequest(req)
		case t := <-c.check.C:
			// Always reset the ticker because the previous tick
			// could have arrived by way of a forced tick
			c.check.Reset(c.tickDuration)
			for _, item := range c.items {
				if item.IsBusy() || item.Next().After(t) {
					continue
				}
				item.SetBusy(true)
				c.outgoing <- item
			}
		case <-ctx.Done():
			return
		}
	}
}

// Run sets the client into motion. It will start a number of worker goroutines,
// and return a Controller object that you can use to control the execution of
// the client.
//
// If you attempt to call Run more than once, it will return an error.
func (c *Client) Run(octx context.Context) (*Controller, error) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return nil, errAlreadyRunning
	}
	c.running = true
	c.mu.Unlock()

	ctx, cancel := context.WithCancel(octx)

	incoming := make(chan ctrlRequest, c.numWorkers)
	outgoing := make(chan Resource, c.numWorkers)
	for range c.numWorkers {
		go worker(ctx, outgoing)
	}

	tickDuration := oneDay
	ctrl := &Controller{
		cancel:       cancel,
		outgoing:     outgoing,
		incoming:     incoming,
		tickDuration: tickDuration,
		check:        time.NewTicker(tickDuration),
	}
	go ctrl.loop(ctx)

	return ctrl, nil
}
