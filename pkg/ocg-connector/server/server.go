// Package server implements the OcgConnectorService gRPC endpoint.
package server

import (
	"context"

	ocgconnectorv1 "github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/ocg-connector/v1"
)

// Server implements ocgconnectorv1.OcgConnectorServiceServer.
type Server struct {
	ocgconnectorv1.UnimplementedOcgConnectorServiceServer
}

// New returns a Server.
func New() *Server {
	return &Server{}
}

// Fetch returns the next page of records for a store. The initial
// implementation returns an empty page; record backends are wired in later.
func (s *Server) Fetch(_ context.Context, _ *ocgconnectorv1.FetchRequest) (*ocgconnectorv1.FetchResponse, error) {
	return &ocgconnectorv1.FetchResponse{}, nil
}
