package httprc

import (
	"context"
	"fmt"
	"sync"
	"time"
)

func (c *controller) adjustInterval(ctx context.Context, req adjustIntervalRequest) {
	interval := roundupToSeconds(time.Until(req.resource.Next()))
	c.traceSink.Put(ctx, fmt.Sprintf("httprc controller: got adjust request (current tick interval=%s, next for %q=%s)", c.tickInterval, req.resource.URL(), interval))

	if interval < time.Second {
		interval = time.Second
	}

	if c.tickInterval < interval {
		c.traceSink.Put(ctx, fmt.Sprintf("httprc controller: no adjusting required (time to next check %s > current tick interval %s)", interval, c.tickInterval))
	} else {
		c.traceSink.Put(ctx, fmt.Sprintf("httprc controller: adjusting tick interval to %s", interval))
		c.tickInterval = interval
		c.check.Reset(interval)
	}
}

func (c *controller) addResource(ctx context.Context, req addRequest) {
	r := req.resource
	if _, ok := c.items[r.URL()]; ok {
		// Already exists
		sendReply(ctx, req.reply, struct{}{}, errResourceAlreadyExists)
		return
	}
	c.items[r.URL()] = r

	if r.MaxInterval() == 0 {
		r.SetMaxInterval(c.defaultMaxInterval)
	}

	if r.MinInterval() == 0 {
		c.traceSink.Put(ctx, fmt.Sprintf("httprc controller: set minimum interval to %s", c.defaultMinInterval))
		r.SetMinInterval(c.defaultMinInterval)
	}
	close(req.reply)

	c.traceSink.Put(ctx, fmt.Sprintf("httprc controller: added resource %q", r.URL()))
	c.SetTickInterval(time.Nanosecond)
}

func (c *controller) rmResource(ctx context.Context, req rmRequest) {
	u := req.u
	if _, ok := c.items[u]; !ok {
		sendReply(ctx, req.reply, struct{}{}, errResourceNotFound)
		return
	}

	delete(c.items, u)

	minInterval := oneDay
	for _, item := range c.items {
		if d := item.MinInterval(); d < minInterval {
			minInterval = d
		}
	}

	close(req.reply)
	c.check.Reset(minInterval)
}

func (c *controller) refreshResource(ctx context.Context, req refreshRequest) {
	u := req.u
	r, ok := c.items[u]
	if !ok {
		sendReply(ctx, req.reply, struct{}{}, errResourceNotFound)
		return
	}
	r.SetNext(time.Unix(0, 0))
	sendWorkerSynchronous(ctx, c.syncoutgoing, synchronousRequest{
		resource: r,
		reply:    req.reply,
	})
}

func (c *controller) lookupResource(ctx context.Context, req lookupRequest) {
	u := req.u
	r, ok := c.items[u]
	if !ok {
		sendReply(ctx, req.reply, nil, errResourceNotFound)
		return
	}
	sendReply(ctx, req.reply, r, nil)
}

func (c *controller) handleRequest(ctx context.Context, req any) {
	switch req := req.(type) {
	case adjustIntervalRequest:
		c.adjustInterval(ctx, req)
	case addRequest:
		c.addResource(ctx, req)
	case rmRequest:
		c.rmResource(ctx, req)
	case refreshRequest:
		c.refreshResource(ctx, req)
	case lookupRequest:
		c.lookupResource(ctx, req)
	default:
		c.traceSink.Put(ctx, fmt.Sprintf("httprc controller: unknown request type %T", req))
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
	r.resource.SetBusy(true)
	select {
	case <-ctx.Done():
	case ch <- r:
	}
}

func sendReply[T any](ctx context.Context, ch chan backendResponse[T], v T, err error) {
	defer close(ch)
	select {
	case <-ctx.Done():
	case ch <- backendResponse[T]{payload: v, err: err}:
	}
}

func (c *controller) loop(ctx context.Context, wg *sync.WaitGroup) {
	c.traceSink.Put(ctx, "httprc controller: starting main controller loop")
	defer c.traceSink.Put(ctx, "httprc controller: stopping main controller loop")
	defer wg.Done()
	for {
		select {
		case req := <-c.incoming:
			c.handleRequest(ctx, req)
		case t := <-c.check.C:
			var minNext time.Time
			var dispatched int
			minInterval := -1 * time.Second
			for _, item := range c.items {
				next := item.Next()
				if minNext.IsZero() || next.Before(minNext) {
					minNext = next
				}

				if interval := item.MinInterval(); minInterval < 0 || interval < minInterval {
					minInterval = interval
				}

				if item.IsBusy() || next.After(t) {
					continue
				}

				dispatched++
				sendWorker(ctx, c.outgoing, item)
			}

			c.traceSink.Put(ctx, fmt.Sprintf("httprc controller: dispatched %d resources", dispatched))

			// Next check is always at the earliest next check + 1 second.
			// The extra second makes sure that we are _past_ the actual next check time
			// so we can send the resource to the worker pool
			if interval := time.Until(minNext); interval > 0 {
				c.SetTickInterval(roundupToSeconds(interval) + time.Second)
				c.traceSink.Put(ctx, fmt.Sprintf("httprc controller: resetting check intervanl to %s", c.tickInterval))
			} else {
				// if we got here, either we have no resources, or all resources are busy.
				// In this state, it's possible that the interval is less than 1 second,
				// because we previously set ti to a small value for an immediate refresh.
				// in this case, we want to reset it to a sane value
				if c.tickInterval < time.Second {
					c.SetTickInterval(minInterval)
					c.traceSink.Put(ctx, fmt.Sprintf("httprc controller: resetting check intervanl to %s after forced refresh", c.tickInterval))
				}
			}

			c.traceSink.Put(ctx, fmt.Sprintf("httprc controller: next check in %s", c.tickInterval))
		case <-ctx.Done():
			return
		}
	}
}

func (c *controller) SetTickInterval(d time.Duration) {
	// TODO synchronize
	c.tickInterval = d
	c.check.Reset(d)
}
