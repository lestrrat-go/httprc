package httprc

import (
	"context"
	"fmt"
	"sync"
)

func worker(ctx context.Context, wg *sync.WaitGroup, next <-chan Resource, errSink ErrorSink, traceSink TraceSink) {
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
		}
	}
}
