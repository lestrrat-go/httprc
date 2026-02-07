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
