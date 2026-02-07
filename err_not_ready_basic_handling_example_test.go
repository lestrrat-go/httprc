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
