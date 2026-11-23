// Package server implements the OcgConnectorService gRPC endpoint.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/ocg-connector/otlp"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/ocg-connector/srs"
	ocgconnectorv1 "github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/ocg-connector/v1"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/uuidv7"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// DefaultMaxPageSize is the server-enforced ceiling on records per page.
	DefaultMaxPageSize = 1000
	defaultPageSize    = 100
)

// SRSReader is the SRS read surface the server depends on.
type SRSReader interface {
	Fetch(ctx context.Context, store, sinceID, schemaVersion string, limit int) ([]srs.Record, error)
}

type Server struct {
	ocgconnectorv1.UnimplementedOcgConnectorServiceServer

	srs         SRSReader
	maxPageSize int
	log         *slog.Logger
}

func New(reader SRSReader, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		srs:         reader,
		maxPageSize: DefaultMaxPageSize,
		log:         logger.With("component", "ocg-connector"),
	}
}

func (s *Server) Fetch(ctx context.Context, req *ocgconnectorv1.FetchRequest) (*ocgconnectorv1.FetchResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "fetch: nil request")
	}
	if srs.Handles(req.GetStore()) && req.GetSinceId() != "" && !uuidv7.IsValid(req.GetSinceId()) {
		return nil, status.Errorf(codes.InvalidArgument, "fetch: since_id %q is not a valid UUIDv7", req.GetSinceId())
	}
	limit := s.clampLimit(int(req.GetLimit()))

	records, err := s.read(ctx, req.GetStore(), req.GetSinceId(), req.GetSchemaVersion(), limit)
	if err != nil {
		return nil, s.mapError(ctx, req.GetStore(), err)
	}
	return &ocgconnectorv1.FetchResponse{Records: otlp.Encode(records)}, nil
}

func (s *Server) read(ctx context.Context, store, sinceID, schemaVersion string, limit int) ([]srs.Record, error) {
	if !srs.Handles(store) {
		return nil, fmt.Errorf("%w: %q", errUnknownStore, store)
	}
	if s.srs == nil {
		return nil, errNoBackend
	}
	return s.srs.Fetch(ctx, store, sinceID, schemaVersion, limit)
}

var errNoBackend = errors.New("server: no backend configured for store")

var errUnknownStore = errors.New("server: unknown store")

func (s *Server) clampLimit(limit int) int {
	if limit <= 0 {
		return defaultPageSize
	}
	if limit > s.maxPageSize {
		return s.maxPageSize
	}
	return limit
}

func (s *Server) mapError(ctx context.Context, store string, err error) error {
	switch {
	case errors.Is(err, errUnknownStore), errors.Is(err, srs.ErrUnknownStore):
		return status.Errorf(codes.InvalidArgument, "fetch: unknown store %q", store)
	case errors.Is(err, srs.ErrInvalidCursor):
		return status.Errorf(codes.InvalidArgument, "fetch: %v", err)
	case errors.Is(err, errNoBackend):
		return status.Errorf(codes.Unimplemented, "fetch: store %q is not served by this endpoint", store)
	default:
		s.log.ErrorContext(ctx, "fetch failed", "store", store, "err", err)
		return status.Error(codes.Internal, "fetch: internal error")
	}
}
