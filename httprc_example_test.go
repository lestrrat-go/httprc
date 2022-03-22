package httprc_test

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/lestrrat-go/httprc"
)

const (
	helloWorld   = `Hello World!`
	goodbyeWorld = `Goodbye World!`
)

func Example() {
	var mu sync.RWMutex

	msg := helloWorld

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(`Cache-Control`, fmt.Sprintf(`max-age=%d`, 3))
		w.WriteHeader(http.StatusOK)
		mu.RLock()
		fmt.Fprint(w, msg)
		mu.RUnlock()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errSink := httprc.ErrSinkFunc(func(err error) {
		log.Printf("%s", err)
	})

	c := httprc.New(ctx,
		httprc.WithErrSink(errSink),
		httprc.WithRefreshWindow(time.Second), // force checks every second
	)

	c.Register(srv.URL, httprc.WithHTTPClient(srv.Client()))

	payload, err := c.Get(ctx, srv.URL)
	if err != nil {
		log.Printf("%s", err)
		return
	}

	if string(payload.([]byte)) != helloWorld {
		log.Printf("payload mismatch: %s", payload)
		return
	}

	mu.Lock()
	msg = goodbyeWorld
	mu.Unlock()

	time.Sleep(4 * time.Second)

	payload, err = c.Get(ctx, srv.URL)
	if err != nil {
		log.Printf("%s", err)
		return
	}

	if string(payload.([]byte)) != goodbyeWorld {
		log.Printf("payload mismatch: %s", payload)
		return
	}

	cancel()

	// OUTPUT:
}
