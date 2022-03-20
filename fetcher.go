package httprc

import (
	"context"
	"fmt"
	"net/http"
)

// Fetcher is an interface, because you are under no circumstances
// allowed to use a zero value for the underlying implementation.
type Fetcher interface {
	Fetch(context.Context, *FetchRequest) (*http.Response, error)
}

// FetchRequest is a set of data that can be used to make an HTTP
// request.
type FetchRequest struct {
	// Client contains the HTTP Client that can be used to make a
	// request. By setting a custom *http.Client, you can for example
	// provide a custom http.Transport
	//
	// If not specified, http.DefaultClient will be used.
	Client HTTPClient

	// URL contains the URL to be fetched
	URL string

	// reply is a field that is only used by the internals of the fetcher
	// it is used to return the result of fetching
	reply chan *fetchResult
}

type fetchResult struct {
	Response *http.Response
	Error    error
}

type fetcher struct {
	requests chan *FetchRequest
}

func NewFetcher(ctx context.Context /*options ...FetcherOption*/) Fetcher {
	nworkers := 1

	incoming := make(chan *FetchRequest)
	for i := 0; i < nworkers; i++ {
		go runFetchWorker(ctx, incoming)
	}
	return &fetcher{
		requests: incoming,
	}
}

// Fetch requests that a HTTP request be made on behalf of the caller,
// and returns the http.Response object.
func (f *fetcher) Fetch(ctx context.Context, req *FetchRequest) (*http.Response, error) {
	reply := make(chan *fetchResult)
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
		return nil, ctx.Err()
	case fr := <-reply:
		return fr.Response, fr.Error
	}

	// There's no way the control can reach here
	return nil, fmt.Errorf(`httprc.Fetcher.Fetch: should not get here`)
}

func runFetchWorker(ctx context.Context, incoming chan *FetchRequest) error {
LOOP:
	for {
		select {
		case <-ctx.Done():
			break LOOP
		case req := <-incoming:
			res, err := req.Client.Get(req.URL)
			r := &fetchResult{Response: res, Error: err}
			select {
			case <-ctx.Done():
				break LOOP
			case req.reply <- r:
			}
			close(req.reply)
		}
	}
	return nil
}
