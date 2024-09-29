package httprc

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/lestrrat-go/httprc/v3/errsink"
	"github.com/lestrrat-go/httprc/v3/proxysink"
	"github.com/lestrrat-go/httprc/v3/tracesink"
)

// Client is the main entry point for the httprc package.
type Client struct {
	mu                 sync.Mutex
	httpcl             HTTPClient
	numWorkers         int
	running            bool
	errSink            ErrorSink
	traceSink          TraceSink
	wl                 Whitelist
	defaultMaxInterval time.Duration
	defaultMinInterval time.Duration
}

const DefaultWorkers = 5

// DefaultMaxInterval is the default maximum interval between fetches
const DefaultMaxInterval = 24 * time.Hour * 30

// DefaultMinInterval is the default minimum interval between fetches.
const DefaultMinInterval = 15 * time.Minute

// used internally
const oneDay = 24 * time.Hour

// NewClient creates a new `httprc.Client` object.
//
// By default ALL urls are allowed. This may not be suitable for you if
// are using this in a production environment. You are encouraged to specify
// a whitelist using the `WithWhitelist` option.
func NewClient(options ...NewClientOption) *Client {
	//nolint:stylecheck
	var errSink ErrorSink = errsink.NewNop()
	//nolint:stylecheck
	var traceSink TraceSink = tracesink.NewNop()
	var wl Whitelist = InsecureWhitelist{}
	var httpcl HTTPClient = http.DefaultClient

	defaultMinInterval := DefaultMinInterval
	defaultMaxInterval := DefaultMaxInterval

	numWorkers := DefaultWorkers
	//nolint:forcetypeassert
	for _, option := range options {
		switch option.Ident() {
		case identHTTPClient{}:
			httpcl = option.Value().(HTTPClient)
		case identWorkers{}:
			numWorkers = option.Value().(int)
		case identErrorSink{}:
			errSink = option.Value().(ErrorSink)
		case identTraceSink{}:
			traceSink = option.Value().(TraceSink)
		case identWhitelist{}:
			wl = option.Value().(Whitelist)
		}
	}

	if numWorkers <= 0 {
		numWorkers = 1
	}
	return &Client{
		httpcl:     httpcl,
		numWorkers: numWorkers,
		errSink:    errSink,
		traceSink:  traceSink,
		wl:         wl,

		defaultMinInterval: defaultMinInterval,
		defaultMaxInterval: defaultMaxInterval,
	}
}

// Start sets the client into motion. It will start a number of worker goroutines,
// and return a Controller object that you can use to control the execution of
// the client.
//
// If you attempt to call Start more than once, it will return an error.
func (c *Client) Start(octx context.Context) (Controller, error) {
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

		ocancel := cancel
		cancel = func() {
			ocancel()
			proxy.Close()
		}

		traceSink = proxy
	}

	incoming := make(chan any, c.numWorkers)
	outgoing := make(chan Resource, c.numWorkers)
	syncoutgoing := make(chan synchronousRequest, c.numWorkers)
	wg.Add(c.numWorkers)
	for range c.numWorkers {
		wrk := worker{
			incoming:  incoming,
			next:      outgoing,
			nextsync:  syncoutgoing,
			errSink:   errSink,
			traceSink: traceSink,
			httpcl:    c.httpcl,
		}
		go wrk.Run(ctx, &wg)
	}

	tickInterval := oneDay
	ctrl := &controller{
		cancel:       cancel,
		items:        make(map[string]Resource),
		outgoing:     outgoing,
		syncoutgoing: syncoutgoing,
		incoming:     incoming,
		traceSink:    traceSink,
		tickInterval: tickInterval,
		check:        time.NewTicker(tickInterval),
		shutdown:     make(chan struct{}),
		wl:           c.wl,

		defaultMinInterval: c.defaultMinInterval,
		defaultMaxInterval: c.defaultMaxInterval,
	}
	wg.Add(1)
	go ctrl.loop(ctx, &wg)

	go func(wg *sync.WaitGroup, ch chan struct{}) {
		wg.Wait()
		close(ch)
	}(&wg, ctrl.shutdown)

	return ctrl, nil
}
