# github.com/lestrrat-go/httprc/v3 ![](https://github.com/lestrrat-go/httprc/v3/workflows/CI/badge.svg) [![Go Reference](https://pkg.go.dev/badge/github.com/lestrrat-go/httprc/v3.svg)](https://pkg.go.dev/github.com/lestrrat-go/httprc/v3)

`httprc` is a HTTP "Refresh" Cache. Its aim is to cache a remote resource that
can be fetched via HTTP, but keep the cached content up-to-date based on periodic
refreshing.

# Client

A `httprc.Client` object is comprised of 3 parts: The user-facing controller API,
the main controller loop, and set of workers that perform the actual fetching.

The user-facing controller API is the object returned when you call `(httprc.Client).Start`.

```go
ctrl, _ := client.Start(ctx)
```

# Controller API

The controller API gives you access to the controller backend that runs asynchronously.
All methods take a `context.Context` object because they potentially block. You should
be careful to use `context.WithTimeout` to properly set a timeout if you cannot tolerate
a blocking operation.

# Main Controller Loop

The main controller loop is run asynchronously to the controller API. It is single threaded,
and it has two reponsibilities.

The first is to receive commands from the controller API,
and appropriately modify the state of the goroutine, i.e. modify the list of resources
it is watching, performing forced refreshes, etc.

The other is to periodically wake up and go through the list of resources and re-fetch
ones that are past their TTL (in reality, each resource carry a "next-check" time, not
a TTL). The main controller loop itself does nothing more: it just kicks these checks periodically.

The interval between fetches is changed dynamically based on either the metadata carried
with the HTTP responses, such as `Cache-Control` and `Expires` headers, or a constant
interval set by the user for a given resource. Between these values, the main controller loop
will pick the shortest interval (but no less than 1 second) and checks if resources
need updating based on that value.

For example, if a resource A has an expiry of 10 minutes and if resource has an expiry of 5
minutes, the main controller loop will attempt to wake up roughly every 5 minutes to check
on the resources.

When the controller loop detects that a resource needs to be checked for freshness, 
it will send the resource to the worker pool to be synced.

# Interval calculation

After the resource is synced, the next fetch is scheduled. The interval to the next
fetch is calculated either by using constant intervals, or by heuristics using values
from the `http.Response` object.

If the constant interval is specified, no extra calculation is performed. If you specify
a constant interval of 15 minutes, the resource will be checked every 15 minutes. This is
predictable and reliable, but not necessarily efficient.

If you do not specify a constant interval, the HTTP response is analyzed for
values in `Cache-Control` and `Expires` headers. These values will be compared against
a maximum and minimum interval values, which default to 30 days and 15 minutes, respectively.
If the values obtained from the headers fall within that range, the value from the header is
used. If the value is larger than the maximum, the maximum is used. If the value is lower
than the minimum, the minimum is used.

# SYNOPSIS

<!-- INCLUDE(client_example_test.go) -->
```go
package httprc_test

import (
  "context"
  "encoding/json"
  "errors"
  "fmt"
  "net/http"
  "net/http/httptest"
  "time"

  "github.com/lestrrat-go/httprc/v3"
)

func ExampleClient() {
  ctx, cancel := context.WithCancel(context.Background())
  defer cancel()

  type HelloWorld struct {
    Hello string `json:"hello"`
  }

  srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
    json.NewEncoder(w).Encode(map[string]string{"hello": "world"})
  }))

  options := []httprc.NewClientOption{
    // By default the client will allow all URLs (which is what the option
    // below is explicitly specifying). If you want to restrict what URLs
    // are allowed, you can specify another whitelist.
    //
    //		httprc.WithWhitelist(httprc.NewInsecureWhitelist()),
  }
  // If you would like to handle errors from asynchronous workers, you can specify a error sink.
  // This is disabled in this example because the trace logs are dynamic
  // and thus would interfere with the runnable example test.
  // options = append(options, httprc.WithErrorSink(errsink.NewSlog(slog.New(slog.NewJSONHandler(os.Stdout, nil)))))

  // If you would like to see the trace logs, you can specify a trace sink.
  // This is disabled in this example because the trace logs are dynamic
  // and thus would interfere with the runnable example test.
  // options = append(options, httprc.WithTraceSink(tracesink.NewSlog(slog.New(slog.NewJSONHandler(os.Stdout, nil)))))

  // Create a new client
  cl := httprc.NewClient(options...)

  // Start the client, and obtain a Controller object
  ctrl, err := cl.Start(ctx)
  if err != nil {
    fmt.Println(err.Error())
    return
  }
  // The following is required if you want to make sure that there are no
  // dangling goroutines hanging around when you exit. For example, if you
  // are running tests to check for goroutine leaks, you should call this
  // function before the end of your test.
  defer ctrl.Shutdown(time.Second)

  // Create a new resource that is synchronized every so often
  //
  // By default the client will attempt to fetch the resource once
  // as soon as it can, and then if no other metadata is provided,
  // it will fetch the resource every 15 minutes.
  //
  // If the resource responds with a Cache-Control/Expires header,
  // the client will attempt to respect that, and will try to fetch
  // the resource again based on the values obatained from the headers.
  r, err := httprc.NewResource[HelloWorld](srv.URL, httprc.JSONTransformer[HelloWorld]())
  if err != nil {
    fmt.Println(err.Error())
    return
  }

  // Add the resource to the controller, so that it starts fetching.
  // By default, a call to `Add()` will block until the first fetch
  // succeeds, via an implicit call to `r.Ready()`
  // You can change this behavior if you specify the `WithWaitReady(false)`
  // option.
  ctrl.Add(ctx, r)

  // if you specified `httprc.WithWaitReady(false)` option, the fetch will happen
  // "soon", but you're not guaranteed that it will happen before the next
  // call to `Lookup()`. If you want to make sure that the resource is ready,
  // you can call `Ready()` like so:
  /*
    {
      tctx, tcancel := context.WithTimeout(ctx, time.Second)
      defer tcancel()
      if err := r.Ready(tctx); err != nil {
        fmt.Println(err.Error())
        return
      }
    }
  */
  m := r.Resource()
  fmt.Println(m.Hello)
  // OUTPUT:
  // world
}

// Example_err_not_ready_basic_handling demonstrates basic handling of ErrNotReady
func Example_err_not_ready_basic_handling() {
  ctx := context.Background()

  // Create a server that's slow to respond
  srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
    time.Sleep(2 * time.Second)
    json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
  }))
  defer srv.Close()

  cl := httprc.NewClient()
  ctrl, err := cl.Start(ctx)
  if err != nil {
    fmt.Println("Failed to start client:", err)
    return
  }
  defer ctrl.Shutdown(time.Second)

  resource, err := httprc.NewResource[map[string]string](
    srv.URL,
    httprc.JSONTransformer[map[string]string](),
  )
  if err != nil {
    fmt.Println("Failed to create resource:", err)
    return
  }

  // Add with timeout - will return ErrNotReady
  addCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
  defer cancel()

  err = ctrl.Add(addCtx, resource)
  if err != nil {
    if errors.Is(err, httprc.ErrNotReady()) {
      // Resource registered, will fetch in background
      fmt.Println("Resource registered but not ready yet")
      fmt.Println("Safe to continue with application startup")
      return
    }
    // Registration failed
    fmt.Println("Failed to register resource:", err)
    return
  }

  // Resource registered AND ready with data
  fmt.Println("Resource ready")

  // OUTPUT:
  // Resource registered but not ready yet
  // Safe to continue with application startup
}

// Example_err_not_ready_retry_logic demonstrates proper retry logic that
// distinguishes between registration failures and ErrNotReady
func Example_err_not_ready_retry_logic() {
  ctx := context.Background()

  srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
    time.Sleep(2 * time.Second)
    json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
  }))
  defer srv.Close()

  cl := httprc.NewClient()
  ctrl, err := cl.Start(ctx)
  if err != nil {
    fmt.Println("Failed to start client:", err)
    return
  }
  defer ctrl.Shutdown(time.Second)

  var resource httprc.Resource
  url := srv.URL

  // Retry logic: only retry registration failures
  for attempt := 1; attempt <= 3; attempt++ {
    resource, err = httprc.NewResource[map[string]string](
      url,
      httprc.JSONTransformer[map[string]string](),
    )
    if err != nil {
      fmt.Printf("Attempt %d: failed to create resource: %v\n", attempt, err)
      continue
    }

    addCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
    err = ctrl.Add(addCtx, resource)
    cancel()

    if err == nil {
      // Success - registered and ready
      fmt.Println("Resource registered and ready")
      return
    }

    if errors.Is(err, httprc.ErrNotReady()) {
      // Registered successfully, just not ready yet
      // Don't retry Add() - it would fail with duplicate URL
      fmt.Printf("Attempt %d: Resource registered, not ready yet\n", attempt)
      fmt.Println("Resource will fetch in background, continuing...")
      return
    }

    // Registration failed - retry
    fmt.Printf("Attempt %d: Registration failed: %v\n", attempt, err)
    if attempt < 3 {
      time.Sleep(time.Second * time.Duration(attempt))
    }
  }

  // OUTPUT:
  // Attempt 1: Resource registered, not ready yet
  // Resource will fetch in background, continuing...
}

// Example_err_not_ready_checking_underlying_error demonstrates how to check
// the underlying error wrapped by ErrNotReady
func Example_err_not_ready_checking_underlying_error() {
  ctx := context.Background()

  srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
    time.Sleep(2 * time.Second)
    json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
  }))
  defer srv.Close()

  cl := httprc.NewClient()
  ctrl, err := cl.Start(ctx)
  if err != nil {
    fmt.Println("Failed to start client:", err)
    return
  }
  defer ctrl.Shutdown(time.Second)

  resource, err := httprc.NewResource[map[string]string](
    srv.URL,
    httprc.JSONTransformer[map[string]string](),
  )
  if err != nil {
    fmt.Println("Failed to create resource:", err)
    return
  }

  // Add with timeout
  addCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
  defer cancel()

  err = ctrl.Add(addCtx, resource)
  if err != nil {
    if errors.Is(err, httprc.ErrNotReady()) {
      // Resource registered, check why it's not ready
      // errors.Is() automatically unwraps the error chain
      if errors.Is(err, context.DeadlineExceeded) {
        fmt.Println("Resource registered but timed out waiting for data")
        fmt.Println("Will continue fetching in background")
      } else {
        fmt.Printf("Resource registered but not ready: %v\n", err)
      }
      return
    }
    // Registration failed
    fmt.Println("Registration failed:", err)
    return
  }

  fmt.Println("Resource ready")

  // OUTPUT:
  // Resource registered but timed out waiting for data
  // Will continue fetching in background
}
```
source: [client_example_test.go](https://github.com/lestrrat-go/httprc/blob/refs/heads/v3/client_example_test.go)
<!-- END INCLUDE -->
