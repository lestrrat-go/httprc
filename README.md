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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"hello": "world"})
	}))

	// Create a new client
	cl := httprc.NewClient()

	// Start the client, and obtain a Controller object
	ctrl, err := cl.Run(ctx)
	if err != nil {
		fmt.Println(err.Error())
		return
	}

	r, err := httprc.NewResource[HelloWorld](srv.URL, httprc.JSONTransformer[HelloWorld]())
	if err != nil {
		fmt.Println(err.Error())
		return
	}

	// Add the resource to the controller, so that it starts fetching
	ctrl.AddResource(r)

	time.Sleep(1 * time.Second)

	m := r.Resource()
	fmt.Println(m.Hello)
	// OUTPUT:
	// world
}


```
source: [client_example_test.go](https://github.com/lestrrat-go/httprc/blob/main/client_example_test.go)
<!-- END INCLUDE -->