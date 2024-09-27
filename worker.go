package httprc

import (
	"context"
	"fmt"
	"sync"
)

type synchronousRequest struct {
	r     Resource
	reply chan error
}

func worker(ctx context.Context, wg *sync.WaitGroup, next <-chan Resource, nextsync <-chan synchronousRequest, errSink ErrorSink, traceSink TraceSink) {
	defer wg.Done()
	ctx = withTraceSink(ctx, traceSink)
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-next:
			traceSink.Put(ctx, fmt.Sprintf("httprc worker: syncing %q", r.URL()))
			if err := r.Sync(ctx); err != nil {
				errSink.Put(ctx, err)
			}
			r.SetBusy(false)
		case sr := <-nextsync:
			traceSink.Put(ctx, fmt.Sprintf("httprc worker: syncing %q (synchronous)", sr.r.URL()))
			if err := sr.r.Sync(ctx); err != nil {
				sendReply(ctx, sr.reply, err)
			}
			sr.r.SetBusy(false)
			sendReply(ctx, sr.reply, nil)
		}
	}
}
