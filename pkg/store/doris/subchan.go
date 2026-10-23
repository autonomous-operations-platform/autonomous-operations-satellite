package doris

import (
	"sync"
	"sync/atomic"
)

type subChan[T any] struct {
	ch     chan T
	mu     sync.Mutex
	closed bool
	drops  atomic.Uint64
	once   sync.Once
}

func newSubChan[T any]() *subChan[T] {
	return &subChan[T]{ch: make(chan T, subscriberBufSize)}
}

func (s *subChan[T]) C() <-chan T { return s.ch }

func (s *subChan[T]) send(v T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.drops.Add(1)
		return
	}
	select {
	case s.ch <- v:
	default:
		s.drops.Add(1)
	}
}

func (s *subChan[T]) Drops() uint64 { return s.drops.Load() }

func (s *subChan[T]) closeOnce() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.ch)
		s.mu.Unlock()
	})
}
