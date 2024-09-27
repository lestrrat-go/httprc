# github.com/lestrrat-go/httprc/v3 ![](https://github.com/lestrrat-go/httprc/v3/workflows/CI/badge.svg) [![Go Reference](https://pkg.go.dev/badge/github.com/lestrrat-go/httprc/v3.svg)](https://pkg.go.dev/github.com/lestrrat-go/httprc/v3) [![codecov.io](https://codecov.io/github/lestrrat-go/httprc/coverage.svg)](https://codecov.io/github/lestrrat-go/httprc)

`httprc` is a HTTP "Refresh" Cache. Its aim is to cache a remote resource that
can be fetched via HTTP, but keep the cached content up-to-date based on periodic
refreshing.

# SYNOPSIS

<!-- INCLUDE(client_example_test.go) -->
```go
package httprc_test

import (
  "context"
  "encoding/json"
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
  r, err := httprc.NewResource[HelloWorld](srv.URL, httprc.JSONTransformer[HelloWorld]())
  if err != nil {
    fmt.Println(err.Error())
    return
  }

  // Add the resource to the controller, so that it starts fetching
  ctrl.Add(ctx, r)

  {
    tctx, tcancel := context.WithTimeout(ctx, time.Second)
    defer tcancel()
    if err := r.Ready(tctx); err != nil {
      fmt.Println(err.Error())
      return
    }
  }
  m := r.Resource()
  fmt.Println(m.Hello)
  // OUTPUT:
  // world
}
```
source: [client_example_test.go](https://github.com/lestrrat-go/httprc/blob/v3/client_example_test.go)
<!-- END INCLUDE -->
