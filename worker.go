package httprc

import (
	"context"
	"fmt"
)

func worker(ctx context.Context, next <-chan Resource) {
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-next:
			if err := r.Sync(ctx); err != nil {
				// TODO: somehow return this error
				fmt.Println(err)
			}
			r.SetBusy(false)
		}
	}
}
