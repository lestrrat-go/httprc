package httprc

import (
	"context"
	"fmt"
	"net/http"
	"sync"
)

const defaultWorkerCount = 3

// HTTPClient defines the interface required for the HTTP client
// used within the fetcher.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type fetchRequest struct {
	mu sync.RWMutex

	// client contains the HTTP Client that can be used to make a
	// request. By setting a custom *http.Client, you can for example
	// provide a custom http.Transport
	//
	// If not specified, whatever specified when Cache is created
	client HTTPClient

	wl Whitelist

	// u contains the URL to be fetched
	url string

	// reply is a field that is only used by the internals of the fetcher
	// it is used to return the result of fetching
	reply chan *fetchResult
}

func newFetchRequest(url string, client HTTPClient, wl Whitelist) *fetchRequest {
	return &fetchRequest{
		url:    url,
		client: client,
		wl:     wl,
	}
}

type fetchResult struct {
	mu  sync.RWMutex
	res *http.Response
	err error
}

func (fr *fetchResult) reply(ctx context.Context, reply chan *fetchResult) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case reply <- fr:
	}

	close(reply)
	return nil
}

type fetcher struct {
	requests chan *fetchRequest
}

func newFetcher(ctx context.Context, nworkers int, wl Whitelist) *fetcher {
	if nworkers < 1 {
		nworkers = defaultWorkerCount
	}

	incoming := make(chan *fetchRequest)
	for i := 0; i < nworkers; i++ {
		go runFetchWorker(ctx, incoming, wl)
	}
	return &fetcher{
		requests: incoming,
	}
}

// fetch (unexported) is the main fetching implemntation.
// it allows the caller to reuse the same *fetchRequest object
func (f *fetcher) fetch(ctx context.Context, req *fetchRequest) (*http.Response, error) {
	reply := make(chan *fetchResult, 1)
	req.mu.Lock()
	req.reply = reply
	req.mu.Unlock()

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
		fr.mu.RLock()
		res := fr.res
		err := fr.err
		fr.mu.RUnlock()
		return res, err
	}
}

func runFetchWorker(ctx context.Context, incoming chan *fetchRequest, wl Whitelist) {
LOOP:
	for {
		select {
		case <-ctx.Done():
			break LOOP
		case req := <-incoming:
			req.mu.RLock()
			reply := req.reply
			client := req.client
			url := req.url
			reqwl := req.wl
			req.mu.RUnlock()

			var wls []Whitelist
			for _, v := range []Whitelist{wl, reqwl} {
				if v != nil {
					wls = append(wls, v)
				}
			}

			if len(wls) > 0 {
				for _, wl := range wls {
					if !wl.IsAllowed(url) {
						r := &fetchResult{
							err: fmt.Errorf(`fetching url %q rejected by whitelist`, url),
						}
						if err := r.reply(ctx, reply); err != nil {
							break LOOP
						}
						continue LOOP
					}
				}
			}

			// The body is handled by the consumer of the fetcher
			httpreq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				r := &fetchResult{
					err: err,
				}
				if err := r.reply(ctx, reply); err != nil {
					break LOOP
				}
				continue LOOP
			}

			//nolint:bodyclose
			res, err := client.Do(httpreq)
			r := &fetchResult{
				res: res,
				err: err,
			}
			if err := r.reply(ctx, reply); err != nil {
				break LOOP
			}
		}
	}
}
