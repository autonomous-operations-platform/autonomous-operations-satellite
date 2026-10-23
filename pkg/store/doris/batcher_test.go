package doris

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBatcher_FlushError(t *testing.T) {
	flushErr := errors.New("flush boom")

	var mu sync.Mutex
	var calls int
	flush := func(_ context.Context, batch []int) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return flushErr
	}

	// size 2 so two sends trigger the size-based flush in run
	b := newBatcher("test", 2, time.Hour, time.Second, 16, flush, discardLogger())
	b.send(1)
	b.send(2)
	b.close() // drains and waits for the goroutine to exit

	mu.Lock()
	defer mu.Unlock()
	if calls == 0 {
		t.Fatal("expected flush to be called at least once")
	}
}

// TestBatcher_SizeTriggersFlush covers run's size-based flush branch: filling
// the buffer to size flushes immediately without waiting for the ticker.
func TestBatcher_SizeTriggersFlush(t *testing.T) {
	flushed := make(chan []int, 4)
	flush := func(_ context.Context, batch []int) error {
		// Copy: run reuses the backing array after flushBuf returns.
		cp := append([]int(nil), batch...)
		flushed <- cp
		return nil
	}

	// Large interval so only the size threshold can trigger the flush.
	b := newBatcher("test", 3, time.Hour, time.Second, 16, flush, discardLogger())
	b.send(1)
	b.send(2)
	b.send(3)

	select {
	case batch := <-flushed:
		if len(batch) != 3 {
			t.Errorf("expected batch of 3, got %d", len(batch))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("size threshold did not trigger a flush")
	}
	b.close()
}

// TestBatcher_SendAfterCloseIsDropped covers send's stop-guard branch: a send
// after close returns must not panic and must be dropped.
func TestBatcher_SendAfterCloseIsDropped(t *testing.T) {
	var calls int
	flush := func(_ context.Context, _ []int) error {
		calls++
		return nil
	}
	b := newBatcher("test", 2, time.Hour, time.Second, 16, flush, discardLogger())
	b.close()

	b.send(99)

	if calls != 0 {
		t.Errorf("expected no flush after close, got %d calls", calls)
	}
}

// TestBatcher_ChannelFullDropsItem covers send's channel-full branch: when the
// buffered channel is full, the item is dropped rather than blocking.
func TestBatcher_ChannelFullDropsItem(t *testing.T) {
	// Block the flush so the run goroutine cannot drain the channel, letting us
	// fill it to capacity.
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	flush := func(_ context.Context, _ []int) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return nil
	}

	// size 1 so the first send triggers a flush that then blocks on release;
	// chanSize 1 so one further item fills the channel and the next is dropped.
	b := newBatcher("test", 1, time.Hour, time.Second, 1, flush, discardLogger())

	b.send(1) // consumed by run, triggers flush which blocks on release
	<-started // ensure the goroutine is parked inside flush
	b.send(2) // fills the channel buffer (capacity 1)
	b.send(3) // channel full -> dropped via send's default branch (line 67)

	close(release) // let flush return so close can drain and exit
	b.close()
}
