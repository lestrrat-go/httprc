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
