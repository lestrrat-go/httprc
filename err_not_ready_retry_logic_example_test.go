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
