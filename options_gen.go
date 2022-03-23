// This file is auto-generated by github.com/lestrrat-go/option/cmd/genoptions. DO NOT EDIT

package httprc

import (
	"time"

	"github.com/lestrrat-go/option"
)

type Option = option.Interface

// CacheOption desribes options that can be passed to `New()`
type CacheOption interface {
	Option
	cacheOption()
}

type cacheOption struct {
	Option
}

func (*cacheOption) cacheOption() {}

// FetchOption describes options that can be passed to `(httprc.Fetcher).Fetch()`
type FetchOption interface {
	Option
	fetchOption()
}

type fetchOption struct {
	Option
}

func (*fetchOption) fetchOption() {}

// FetcherOption describes options that can be passed to `(httprc.Fetcher).NewFetcher()`
type FetcherOption interface {
	Option
	cacheOption()
}

type fetcherOption struct {
	Option
}

func (*fetcherOption) cacheOption() {}

// RegisterOption desribes options that can be passed to `(httprc.Cache).Register()`
type RegisterOption interface {
	Option
	registerOption()
}

type registerOption struct {
	Option
}

func (*registerOption) registerOption() {}

type identErrSink struct{}
type identFetcherWorkerCount struct{}
type identHTTPClient struct{}
type identMinRefreshInterval struct{}
type identRefreshInterval struct{}
type identRefreshWindow struct{}
type identTransformer struct{}
type identWhitelist struct{}

func (identErrSink) String() string {
	return "WithErrSink"
}

func (identFetcherWorkerCount) String() string {
	return "WithFetcherWorkerCount"
}

func (identHTTPClient) String() string {
	return "WithHTTPClient"
}

func (identMinRefreshInterval) String() string {
	return "WithMinRefreshInterval"
}

func (identRefreshInterval) String() string {
	return "WithRefreshInterval"
}

func (identRefreshWindow) String() string {
	return "WithRefreshWindow"
}

func (identTransformer) String() string {
	return "WithTransformer"
}

func (identWhitelist) String() string {
	return "WithWhitelist"
}

// WithErrSink specifies the `httprc.ErrSink` object that handles errors
// that occurred during the cache's execution. For example, you will be
// able to intercept errors that occurred during the execution of Transformers.
func WithErrSink(v ErrSink) CacheOption {
	return &cacheOption{option.New(identErrSink{}, v)}
}

// WithFetchWorkerCount specifies the number of HTTP fetch workers that are spawned
// in the backend. By default 3 workers are spawned.
func WithFetcherWorkerCount(v int) FetcherOption {
	return &fetcherOption{option.New(identFetcherWorkerCount{}, v)}
}

// WithHTTPClient specififes the HTTP Client object that should be used to fetch
// the resource. For example, if you need an `*http.Client` instance that requires
// special TLS or Authorization setup, you might want to pass it using this option.
func WithHTTPClient(v HTTPClient) RegisterOption {
	return &registerOption{option.New(identHTTPClient{}, v)}
}

// WithMinRefreshInterval specifies the minimum refresh interval to be used.
//
// When we fetch the key from a remote URL, we first look at the `max-age`
// directive from `Cache-Control` response header. If this value is present,
// we compare the `max-age` value and the value specified by this option
// and take the larger one (e.g. if `max-age` = 5 minutes and `min refresh` = 10
// minutes, then next fetch will happen in 10 minutes)
//
// Next we check for the `Expires` header, and similarly if the header is
// present, we compare it against the value specified by this option,
// and take the larger one.
//
// Finally, if neither of the above headers are present, we use the
// value specified by this option as the interval until the next refresh.
//
// If unspecified, the minimum refresh interval is 1 hour.
//
// This value and the header values are ignored if `WithRefreshInterval` is specified.
func WithMinRefreshInterval(v time.Duration) RegisterOption {
	return &registerOption{option.New(identMinRefreshInterval{}, v)}
}

// WithRefreshInterval specifies the static interval between refreshes
// of resources controlled by `httprc.Cache`.
//
// Providing this option overrides the adaptive token refreshing based
// on Cache-Control/Expires header (and `httprc.WithMinRefreshInterval`),
// and refreshes will *always* happen in this interval.
//
// You generally do not want to make this value too small, as it can easily
// be considered a DoS attack, and there is no backoff mechanism for failed
// attempts.
func WithRefreshInterval(v time.Duration) RegisterOption {
	return &registerOption{option.New(identRefreshInterval{}, v)}
}

// WithRefreshWindow specifies the interval between checks for refreshes.
// `httprc.Cache` does not check for refreshes in exact intervals. Instead,
// it wakes up at every tick that occurs in the interval specified by
// `WithRefreshWindow` option, and refreshes all entries that need to be
// refreshed within this window.
//
// The default value is 15 minutes.
//
// You generally do not want to make this value too small, as it can easily
// be considered a DoS attack, and there is no backoff mechanism for failed
// attempts.
func WithRefreshWindow(v time.Duration) CacheOption {
	return &cacheOption{option.New(identRefreshWindow{}, v)}
}

// WithTransformer specifies the `httprc.Transformer` object that should be applied
// to the fetched resource. The `Transform()` method is only called if the HTTP request
// returns a `200 OK` status.
func WithTransformer(v Transformer) RegisterOption {
	return &registerOption{option.New(identTransformer{}, v)}
}

// WithWhitelist specifies the Whitelist object that can control which URLs are
// allowed to be processed.
//
// It can be passed to `httprc.NewCache` as a whitelist applied to all
// URLs that are fetched by the cache, or it can be passed on a per-URL
// basis using `(httprc.Cache).Register()`. If both are specified,
// the url must fulfill _both_ the cache-wide whitelist and the per-URL
// whitelist.
func WithWhitelist(v Whitelist) FetcherOption {
	return &fetcherOption{option.New(identWhitelist{}, v)}
}
