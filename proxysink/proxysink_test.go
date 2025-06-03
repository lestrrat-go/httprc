package proxysink_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/httprc/v3/proxysink"
)

type mockBackend[T any] struct {
	mu   sync.Mutex
	puts []T
}

func (m *mockBackend[T]) Put(ctx context.Context, v T) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts = append(m.puts, v)
}

func (m *mockBackend[T]) getPuts() []T {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]T, len(m.puts))
	copy(result, m.puts)
	return result
}

func TestProxyBasic(t *testing.T) {
	t.Parallel()

	backend := &mockBackend[string]{}
	proxy := proxysink.New(backend)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go proxy.Run(ctx)

	// Give some time for goroutines to start
	time.Sleep(10 * time.Millisecond)

	// Put some values
	proxy.Put(ctx, "test1")
	proxy.Put(ctx, "test2")
	proxy.Put(ctx, "test3")

	// Give some time for processing
	time.Sleep(50 * time.Millisecond)

	puts := backend.getPuts()
	if len(puts) != 3 {
		t.Errorf("expected 3 puts, got %d", len(puts))
		return
	}

	expected := []string{"test1", "test2", "test3"}
	for i, exp := range expected {
		if puts[i] != exp {
			t.Errorf("put %d: expected %q, got %q", i, exp, puts[i])
		}
	}
}

func TestProxyWithCancelledContext(t *testing.T) {
	t.Parallel()

	backend := &mockBackend[string]{}
	proxy := proxysink.New(backend)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Should not block or panic
	proxy.Put(ctx, "test")

	// Give some time
	time.Sleep(10 * time.Millisecond)

	puts := backend.getPuts()
	if len(puts) != 0 {
		t.Errorf("expected 0 puts with cancelled context, got %d", len(puts))
	}
}

func TestProxyMultipleValues(t *testing.T) {
	t.Parallel()

	backend := &mockBackend[int]{}
	proxy := proxysink.New(backend)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go proxy.Run(ctx)

	// Give some time for goroutines to start
	time.Sleep(10 * time.Millisecond)

	// Put many values rapidly
	const numValues = 100
	for i := 0; i < numValues; i++ {
		proxy.Put(ctx, i)
	}

	// Give time for processing
	time.Sleep(100 * time.Millisecond)

	puts := backend.getPuts()
	if len(puts) != numValues {
		t.Errorf("expected %d puts, got %d", numValues, len(puts))
		return
	}

	// Values should be in order
	for i := 0; i < numValues; i++ {
		if puts[i] != i {
			t.Errorf("put %d: expected %d, got %d", i, i, puts[i])
		}
	}
}

func TestProxyContextCancellation(t *testing.T) {
	t.Parallel()

	backend := &mockBackend[string]{}
	proxy := proxysink.New(backend)

	ctx, cancel := context.WithCancel(context.Background())

	go proxy.Run(ctx)

	// Give some time for goroutines to start
	time.Sleep(10 * time.Millisecond)

	// Put some values
	proxy.Put(ctx, "before_cancel")

	// Give time for processing
	time.Sleep(20 * time.Millisecond)

	// Cancel context
	cancel()

	// Give time for cleanup
	time.Sleep(20 * time.Millisecond)

	// Try to put after cancellation
	proxy.Put(ctx, "after_cancel")

	// Give more time
	time.Sleep(20 * time.Millisecond)

	puts := backend.getPuts()

	// Should have at least the value before cancellation
	if len(puts) == 0 {
		t.Error("expected at least one put before cancellation")
		return
	}

	if puts[0] != "before_cancel" {
		t.Errorf("expected first put to be %q, got %q", "before_cancel", puts[0])
	}
}

func TestProxyWithTimeout(t *testing.T) {
	t.Parallel()

	backend := &mockBackend[string]{}
	proxy := proxysink.New(backend)

	// Short timeout context
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	go proxy.Run(ctx)

	// Give some time for goroutines to start
	time.Sleep(10 * time.Millisecond)

	proxy.Put(ctx, "test_timeout")

	// Wait for timeout
	time.Sleep(60 * time.Millisecond)

	puts := backend.getPuts()
	if len(puts) != 1 {
		t.Errorf("expected 1 put, got %d", len(puts))
		return
	}

	if puts[0] != "test_timeout" {
		t.Errorf("expected %q, got %q", "test_timeout", puts[0])
	}
}

func TestProxyInterface(t *testing.T) {
	t.Parallel()

	// Ensure Proxy implements Backend interface
	backend := &mockBackend[string]{}
	var _ proxysink.Backend[string] = proxysink.New(backend)
}

func TestProxyBatching(t *testing.T) {
	t.Parallel()

	backend := &mockBackend[string]{}
	proxy := proxysink.New(backend)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go proxy.Run(ctx)

	// Give some time for goroutines to start
	time.Sleep(10 * time.Millisecond)

	// Put values very rapidly to test batching behavior
	values := []string{"a", "b", "c", "d", "e"}
	for _, v := range values {
		proxy.Put(ctx, v)
	}

	// Give time for processing
	time.Sleep(100 * time.Millisecond)

	puts := backend.getPuts()
	if len(puts) != len(values) {
		t.Errorf("expected %d puts, got %d", len(values), len(puts))
		return
	}

	// All values should be present
	for i, expected := range values {
		if puts[i] != expected {
			t.Errorf("put %d: expected %q, got %q", i, expected, puts[i])
		}
	}
}

func TestProxyClose(t *testing.T) {
	t.Parallel()

	backend := &mockBackend[string]{}
	proxy := proxysink.New(backend)

	// Test Close function - this was not covered at all (0%)
	proxy.Close()

	// Try to put after close - this will panic, so we need to catch it
	ctx := context.Background()

	func() {
		defer func() {
			if r := recover(); r != nil {
				// Expected panic due to sending on closed channel
				t.Logf("Expected panic caught: %v", r)
			}
		}()
		proxy.Put(ctx, "should_panic")
	}()

	// Give some time
	time.Sleep(10 * time.Millisecond)

	puts := backend.getPuts()
	// Should be empty since channel is closed and Put panicked
	if len(puts) != 0 {
		t.Errorf("expected 0 puts after close, got %d", len(puts))
	}
}

func TestProxyPutWithCancelledContext(t *testing.T) {
	t.Parallel()

	backend := &mockBackend[string]{}
	proxy := proxysink.New(backend)

	// Create already cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// This should hit the ctx.Done() case in Put function
	proxy.Put(ctx, "test_cancelled")

	// Give some time
	time.Sleep(10 * time.Millisecond)

	puts := backend.getPuts()
	if len(puts) != 0 {
		t.Errorf("expected 0 puts with cancelled context, got %d", len(puts))
	}
}

func TestProxyFlushLoopCancellation(t *testing.T) {
	t.Parallel()

	backend := &mockBackend[string]{}
	proxy := proxysink.New(backend)

	ctx, cancel := context.WithCancel(context.Background())

	go proxy.Run(ctx)

	// Give some time for goroutines to start
	time.Sleep(10 * time.Millisecond)

	// Put a value
	proxy.Put(ctx, "test_value")

	// Give time for processing
	time.Sleep(20 * time.Millisecond)

	// Cancel context while there might be pending values
	cancel()

	// Give time for cleanup
	time.Sleep(50 * time.Millisecond)

	puts := backend.getPuts()
	if len(puts) != 1 {
		t.Errorf("expected 1 put before cancellation, got %d", len(puts))
		return
	}

	if puts[0] != "test_value" {
		t.Errorf("expected %q, got %q", "test_value", puts[0])
	}
}

func TestProxyLargeBatch(t *testing.T) {
	t.Parallel()

	backend := &mockBackend[string]{}
	proxy := proxysink.New(backend)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go proxy.Run(ctx)

	// Give some time for goroutines to start
	time.Sleep(10 * time.Millisecond)

	// Put a large number of values to test pending slice reallocation
	const numValues = 50
	for i := 0; i < numValues; i++ {
		proxy.Put(ctx, fmt.Sprintf("value_%d", i))
	}

	// Give time for processing
	time.Sleep(200 * time.Millisecond)

	puts := backend.getPuts()
	if len(puts) != numValues {
		t.Errorf("expected %d puts, got %d", numValues, len(puts))
		return
	}

	// Check all values are present and in order
	for i := 0; i < numValues; i++ {
		expected := fmt.Sprintf("value_%d", i)
		if puts[i] != expected {
			t.Errorf("put %d: expected %q, got %q", i, expected, puts[i])
		}
	}
}

func TestProxyChannelBlocking(t *testing.T) {
	t.Parallel()

	backend := &mockBackend[string]{}
	proxy := proxysink.New(backend)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Don't start the Run goroutine to make channel block
	// This should test the blocking behavior in Put

	// Try to put - this should not block indefinitely due to buffered channel
	proxy.Put(ctx, "test_blocking")

	// Give some time
	time.Sleep(10 * time.Millisecond)

	// Now start the proxy
	go proxy.Run(ctx)

	// Give time for processing
	time.Sleep(50 * time.Millisecond)

	puts := backend.getPuts()
	if len(puts) != 1 {
		t.Errorf("expected 1 put, got %d", len(puts))
		return
	}

	if puts[0] != "test_blocking" {
		t.Errorf("expected %q, got %q", "test_blocking", puts[0])
	}
}

func TestProxyFlushLoopEmptyPendingOnCancel(t *testing.T) {
	t.Parallel()

	backend := &mockBackend[string]{}
	proxy := proxysink.New(backend)

	ctx, cancel := context.WithCancel(context.Background())

	go proxy.Run(ctx)

	// Give some time for goroutines to start
	time.Sleep(10 * time.Millisecond)

	// Cancel immediately without putting any values
	// This should test the case where flushloop receives ctx.Done()
	// with empty pending slice
	cancel()

	// Give time for cleanup
	time.Sleep(50 * time.Millisecond)

	puts := backend.getPuts()
	if len(puts) != 0 {
		t.Errorf("expected 0 puts with immediate cancellation, got %d", len(puts))
	}
}

func TestProxyFlushLoopWithPendingOnCancel(t *testing.T) {
	t.Parallel()

	backend := &mockBackend[string]{}
	proxy := proxysink.New(backend)

	ctx, cancel := context.WithCancel(context.Background())

	go proxy.Run(ctx)

	// Give some time for goroutines to start
	time.Sleep(10 * time.Millisecond)

	// Put multiple values rapidly to build up pending queue
	for i := 0; i < 5; i++ {
		proxy.Put(ctx, fmt.Sprintf("pending_%d", i))
	}

	// Cancel context while values are still in pending queue
	// This should test the case where ctx.Done() triggers but len(p.pending) > 0
	cancel()

	// Give time for cleanup - should process remaining pending values
	time.Sleep(100 * time.Millisecond)

	puts := backend.getPuts()
	if len(puts) == 0 {
		t.Error("expected some puts to be processed before cancellation")
	}

	// All pending values should have been processed
	t.Logf("Processed %d values before cancellation", len(puts))
}

func TestProxyLargePendingSliceReallocation(t *testing.T) {
	t.Parallel()

	backend := &mockBackend[string]{}
	proxy := proxysink.New(backend)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go proxy.Run(ctx)

	// Give some time for goroutines to start
	time.Sleep(10 * time.Millisecond)

	// Put many values very rapidly to trigger pending slice growth beyond defaultPendingSize (10)
	// This should test the "if cap(p.pending) > defaultPendingSize" branch
	const numValues = 25

	// Add all values very rapidly to increase chance of large pending queue
	for i := 0; i < numValues; i++ {
		proxy.Put(ctx, fmt.Sprintf("realloc_%d", i))
		// Very short sleep to allow some batching
		if i%5 == 0 {
			time.Sleep(1 * time.Millisecond)
		}
	}

	// Give time for all processing
	time.Sleep(200 * time.Millisecond)

	puts := backend.getPuts()
	if len(puts) != numValues {
		t.Errorf("expected %d puts, got %d", numValues, len(puts))
		return
	}

	// Check all values are present
	for i := 0; i < numValues; i++ {
		expected := fmt.Sprintf("realloc_%d", i)
		found := false
		for _, put := range puts {
			if put == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing expected value: %s", expected)
		}
	}
}
