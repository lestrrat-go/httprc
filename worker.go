package httprc

import (
	"context"
	"fmt"
)

func worker(ctx context.Context, next <-chan Resource) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-next:
			if err := r.Sync(ctx); err != nil {
				// TODO: somehow return this error
				fmt.Println(err)
			}
			r.SetBusy(false)
		}
	}
}
