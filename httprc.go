//go:generate tools/genoptions.sh

// Package httprc implements a cache for resources available
// over http(s). Its aim is not only to cache these resources so
// that it saves on HTTP roundtrips, but it also periodically
// attempts to auto-refresh these resources once they are cached
// based on the user-specified intervals and HTTP `Expires` and
// `Cache-Control` headers, thus keeping the entries _relatively_ fresh.
package httprc
