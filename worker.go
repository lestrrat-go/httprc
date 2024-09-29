package httprc

import (
	"context"
	"fmt"
	"sync"
)

type worker struct {
	httpcl    HTTPClient
	incoming  chan any
	next      <-chan Resource
	nextsync  <-chan synchronousRequest
	errSink   ErrorSink
	traceSink TraceSink
}

func (w worker) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	ctx = withTraceSink(ctx, w.traceSink)
	ctx = withHTTPClient(ctx, w.httpcl)
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-w.next:
			w.traceSink.Put(ctx, fmt.Sprintf("httprc worker: syncing %q", r.URL()))
			if err := r.Sync(ctx); err != nil {
				w.errSink.Put(ctx, err)
			}
			r.SetBusy(false)
			select {
			case <-ctx.Done():
			case w.incoming <- adjustIntervalRequest{resource: r}:
			}
		case sr := <-w.nextsync:
			w.traceSink.Put(ctx, fmt.Sprintf("httprc worker: syncing %q (synchronous)", sr.resource.URL()))
			if err := sr.resource.Sync(ctx); err != nil {
				sendReply(ctx, sr.reply, struct{}{}, err)
			}
			sr.resource.SetBusy(false)
			sendReply(ctx, sr.reply, struct{}{}, nil)
			select {
			case <-ctx.Done():
			case w.incoming <- adjustIntervalRequest{resource: sr.resource}:
			}
		}
	}
}
