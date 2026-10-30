package doris

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// batcher accumulates items of type T and flushes them to Doris in batches.
// It runs a single background goroutine per table, replacing the previous
// pattern of one goroutine and one single-row INSERT per record.
//
// send is non-blocking: items are dropped when the channel is full, which
// preserves the existing fire-and-forget contract while capping goroutine growth.
//
// The caller must ensure send is never called after close returns.
type batcher[T any] struct {
	ch       chan T
	stop     chan struct{}
	flush    func(context.Context, []T) error
	size     int
	interval time.Duration
	timeout  time.Duration
	name     string
	log      *slog.Logger
	wg       sync.WaitGroup
}

func newBatcher[T any](
	name string,
	size int,
	interval time.Duration,
	timeout time.Duration,
	chanSize int,
	flush func(context.Context, []T) error,
	log *slog.Logger,
) *batcher[T] {
	b := &batcher[T]{
		ch:       make(chan T, chanSize),
		stop:     make(chan struct{}),
		flush:    flush,
		size:     size,
		interval: interval,
		timeout:  timeout,
		name:     name,
		log:      log,
	}
	b.wg.Add(1)
	go b.run()
	return b
}

// send enqueues item for batched persistence. It checks stop before attempting
// to send, guarding against a send-on-closed-channel panic when called
// concurrently with close. Items are dropped (not blocked on) when the channel
// is full.
func (b *batcher[T]) send(item T) {
	select {
	case <-b.stop:
		return
	default:
	}
	select {
	case b.ch <- item:
	default:
		b.log.Warn("write batcher channel full, record dropped", "table", b.name)
	}
}

func (b *batcher[T]) run() {
	defer b.wg.Done()
	buf := make([]T, 0, b.size)
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case item := <-b.ch:
			buf = append(buf, item)
			if len(buf) >= b.size {
				b.flushBuf(buf)
				buf = buf[:0]
			}
		case <-ticker.C:
			if len(buf) > 0 {
				b.flushBuf(buf)
				buf = buf[:0]
			}
		case <-b.stop:
			// Drain any items already in the channel before exiting.
			for {
				select {
				case item := <-b.ch:
					buf = append(buf, item)
				default:
					if len(buf) > 0 {
						b.flushBuf(buf)
					}
					return
				}
			}
		}
	}
}

func (b *batcher[T]) flushBuf(batch []T) {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	if err := b.flush(ctx, batch); err != nil {
		// Individual record IDs are logged by the flush function before it
		// returns the error, so the operator can identify every dropped record.
		b.log.Error("write batcher flush failed, records dropped",
			"table", b.name, "count", len(batch), "err", err)
	}
}

// close signals the background goroutine to drain remaining items and stop,
// then waits for it to exit. The caller must guarantee send is no longer
// called before invoking close.
func (b *batcher[T]) close() {
	close(b.stop)
	b.wg.Wait()
}
