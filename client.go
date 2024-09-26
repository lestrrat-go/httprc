package httprc

import (
	"context"
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
	}
}

func (c *Controller) loop(ctx context.Context) error {
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
			return ctx.Err()
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

	tickDuration := 24 * time.Hour
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
