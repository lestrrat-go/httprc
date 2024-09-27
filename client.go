package httprc

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/lestrrat-go/httprc/v3/errsink"
	"github.com/lestrrat-go/httprc/v3/proxysink"
	"github.com/lestrrat-go/httprc/v3/tracesink"
)

// Client is the main entry point for the httprc package.
type Client struct {
	mu         sync.Mutex
	numWorkers int
	running    bool
	errSink    ErrorSink
	traceSink  TraceSink
}

const DefaultWorkers = 5
const oneDay = 24 * time.Hour

func NewClient(options ...NewClientOption) *Client {
	//nolint:stylecheck
	var errSink ErrorSink = errsink.NewNop()
	//nolint:stylecheck
	var traceSink TraceSink = tracesink.NewNop()
	numWorkers := DefaultWorkers
	//nolint:forcetypeassert
	for _, option := range options {
		switch option.Ident() {
		case identWorkers{}:
			numWorkers = option.Value().(int)
		case identErrorSink{}:
			errSink = option.Value().(ErrorSink)
		case identTraceSink{}:
			traceSink = option.Value().(TraceSink)
		}
	}

	if numWorkers <= 0 {
		numWorkers = 1
	}
	return &Client{
		numWorkers: numWorkers,
		errSink:    errSink,
		traceSink:  traceSink,
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
	shutdown     chan struct{}
}

// Shutdown stops the client and all associated goroutines, and waits for them
// to finish. If the context is canceled, the function will return immediately:
// there fore you should not use the context you used to start the client (because
// presumably it's already canceled).
//
// Waiting for the client shutdown will also ensure that all sinks are properly
// flushed.
func (c *Controller) Shutdown(ctx context.Context) error {
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
)

type ctrlRequest struct {
	op       int
	reply    chan error
	resource Resource
}

// AddResource adds a new resource to the controller. If the resource already
// exists, it will return an error.
func (c *Controller) AddResource(r Resource) error {
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

func (c *Controller) loop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
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

// Start sets the client into motion. It will start a number of worker goroutines,
// and return a Controller object that you can use to control the execution of
// the client.
//
// If you attempt to call Start more than once, it will return an error.
func (c *Client) Start(octx context.Context) (*Controller, error) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return nil, errAlreadyRunning
	}
	c.running = true
	c.mu.Unlock()

	// DON'T CANCEL THIS IN THIS METHOD! It's the responsibility of the
	// controller to cancel this context.
	ctx, cancel := context.WithCancel(octx)

	var wg sync.WaitGroup

	// start proxy goroutines that will accept sink requests
	// and forward them to the appropriate sink
	var errSink ErrorSink
	if _, ok := c.errSink.(errsink.Nop); ok {
		errSink = c.errSink
	} else {
		proxy := proxysink.New[error](c.errSink)
		wg.Add(1)
		go func(wg *sync.WaitGroup, proxy *proxysink.Proxy[error]) {
			defer wg.Done()
			proxy.Run(ctx)
		}(&wg, proxy)

		errSink = proxy
	}

	var traceSink TraceSink
	if _, ok := c.traceSink.(tracesink.Nop); ok {
		traceSink = c.traceSink
	} else {
		proxy := proxysink.New[string](c.traceSink)
		wg.Add(1)
		go func(wg *sync.WaitGroup, proxy *proxysink.Proxy[string]) {
			defer wg.Done()
			proxy.Run(ctx)
		}(&wg, proxy)
		traceSink = proxy
	}

	incoming := make(chan ctrlRequest, c.numWorkers)
	outgoing := make(chan Resource, c.numWorkers)
	wg.Add(c.numWorkers)
	for range c.numWorkers {
		go worker(ctx, &wg, outgoing, errSink, traceSink)
	}

	tickDuration := oneDay
	ctrl := &Controller{
		cancel:       cancel,
		outgoing:     outgoing,
		incoming:     incoming,
		tickDuration: tickDuration,
		check:        time.NewTicker(tickDuration),
		shutdown:     make(chan struct{}),
	}
	wg.Add(1)
	go ctrl.loop(ctx, &wg)

	go func(wg *sync.WaitGroup, ch chan struct{}) {
		wg.Wait()
		close(ch)
	}(&wg, ctrl.shutdown)

	return ctrl, nil
}
