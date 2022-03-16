package httpu2d

import (
	"context"
	"fmt"
	"net/http"
)

// Fetcher is an interface, because you are under no circumstances
// allowed to use a zero value for the underlying implementation.
type Fetcher interface {
}

type FetchRequest struct {
	Client *http.Client
	URL    string
}

type FetchResult struct {
	Response *http.Response
	Error    error
}

type fetcher struct{}

func NewFetcher(options ...FetcherOption) Fetcher {
	var nworkers int

	incoming := make(chan *FetchRequest)
	for i := 0; i < nworkers; i++ {
		go runFetchWorker(ctx)
	}
}

func (f *fetcher) Fetch(ctx context.Context, req *FetchRequest) (*http.Response, error) {
	reply := make(chan *FetchResponse)
	req.reply = reply

	// Send a request to the backend
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case f.requests <- req:
	}

	// wait until we get a reply
	select {
	case <-ctx.Done():
		return nil, ctx.Error()
	case fr := <-reply:
		return fr.Reponse, fr.Error
	}

	// There's no way the control can reach here
	return nil, fmt.Errorf(`httpu2d.Fetcher.Fetch: should not get here`)
}

func runFetchWorker(ctx context.Context, incoming chan *FetchRequest) error {
LOOP:
	for {
		select {
		case <-ctx.Done():
			break LOOP
		case req := <-incoming:
			res, err := req.Client.Get(req.URL)
			r := &FetchResult{Response: res, Error: err}
			select {
			case <-ctx.Done():
				break LOOP
			case req.reply <- r:
			}
			close(req.reply)
		}
	}
}
