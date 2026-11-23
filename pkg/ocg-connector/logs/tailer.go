// Package logs serves component log lines to the OCG connector by scanning log
// files on demand per Fetch request. No persistent index is kept.
package logs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/ocg-connector/srs"
)

const scopePrefix = "satellite/logs/"

var ErrInvalidCursor = errors.New("logs: invalid cursor")

var ErrUnknownStore = errors.New("logs: unknown store")

// Source is one log file to scan. ParseTime must never return a zero time.
type Source struct {
	Component string
	Path      string
	ParseTime func([]byte) time.Time
}

type Store struct {
	sources map[string]Source
	log     *slog.Logger
}

func New(sources []Source, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	m := make(map[string]Source, len(sources))
	for _, src := range sources {
		m["srs/logs/"+src.Component] = src
	}
	return &Store{sources: m, log: logger.With("component", "logs")}
}

func Handles(store string) bool {
	return strings.HasPrefix(store, "srs/logs/")
}

func (s *Store) Close() error { return nil }

func (s *Store) Query(ctx context.Context, store, sinceID string, limit int) ([]srs.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	src, ok := s.sources[store]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownStore, store)
	}

	sinceNano, err := parseSinceID(sinceID)
	if err != nil {
		return nil, err
	}

	recs, err := scanSource(src, sinceNano, limit)
	if err != nil {
		s.log.WarnContext(ctx, "scan source failed", "source", src.Path, "err", err)
		return nil, err
	}
	return recs, nil
}

func parseSinceID(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02",
		"02.01.2006",
	} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UnixNano(), nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrInvalidCursor, s)
}

func scanSource(src Source, sinceNano int64, limit int) ([]srs.Record, error) {
	if limit <= 0 {
		return nil, nil
	}
	parseTime := src.ParseTime
	if parseTime == nil {
		parseTime = func(_ []byte) time.Time { return time.Now() }
	}

	f, err := os.Open(src.Path) //nolint:gosec // G304: path is operator configuration, not request input
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("logs: open source %q: %w", src.Path, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("logs: stat source %q: %w", src.Path, err)
	}
	size := info.Size()

	startOffset := int64(0)
	if sinceNano > 0 && size > 0 {
		startOffset, err = findOffset(f, size, sinceNano, parseTime)
		if err != nil {
			return nil, err
		}
		if startOffset > 0 {
			startOffset, _, err = readLineAt(f, startOffset, size)
			if err != nil {
				return nil, err
			}
		}
	}

	if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("logs: seek source %q: %w", src.Path, err)
	}

	var records []srs.Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimRight(scanner.Bytes(), "\r\n")
		if len(line) == 0 {
			continue
		}
		ts := parseTime(line)
		if sinceNano > 0 && ts.UnixNano() <= sinceNano {
			continue
		}
		nano := ts.UnixNano()
		records = append(records, srs.Record{
			ID:           strconv.FormatInt(nano, 10),
			ScopeName:    scopePrefix + src.Component,
			TimeUnixNano: uint64(nano),
			Body:         append([]byte(nil), line...),
		})
		if len(records) >= limit {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("logs: scan source %q: %w", src.Path, err)
	}
	return records, nil
}

// findOffset binary-searches for the byte offset of the first line with
// timestamp > sinceNano, stopping once the range narrows to 4096 bytes so the
// caller's sequential scan covers the rest. It assumes chronological order.
func findOffset(f *os.File, size, sinceNano int64, parseTime func([]byte) time.Time) (int64, error) {
	lo, hi := int64(0), size
	for hi-lo > 4096 {
		mid := (lo + hi) / 2
		lineStart, line, err := readLineAt(f, mid, size)
		if err != nil {
			return 0, err
		}
		if lineStart >= size {
			hi = mid
			continue
		}
		if parseTime(line).UnixNano() <= sinceNano {
			lo = lineStart + 1
		} else {
			hi = lineStart
		}
	}
	return lo, nil
}

func readLineAt(f *os.File, offset, size int64) (int64, []byte, error) {
	start := offset
	if offset > 0 {
		buf := make([]byte, 512)
		pos := offset
		found := false
		for pos < size && !found {
			n := int64(len(buf))
			if size-pos < n {
				n = size - pos
			}
			nr, err := f.ReadAt(buf[:n], pos)
			for i := 0; i < nr; i++ {
				if buf[i] == '\n' {
					start = pos + int64(i) + 1
					found = true
					break
				}
			}
			if !found {
				pos += int64(nr)
			}
			if err != nil {
				break
			}
		}
		if !found {
			return size, nil, nil
		}
	}
	if start >= size {
		return size, nil, nil
	}
	end := start + 64*1024
	if end > size {
		end = size
	}
	buf := make([]byte, end-start)
	n, err := f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, nil, fmt.Errorf("logs: readAt: %w", err)
	}
	buf = buf[:n]
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		buf = buf[:i]
	}
	return start, bytes.TrimRight(buf, "\r"), nil
}
