// Command ocg-connector serves satellite records to the OCG Satellite Connector
// over gRPC.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/ocg-connector/server"
	ocgconnectorv1 "github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/ocg-connector/v1"

	"google.golang.org/grpc"
)

const (
	listenAddr      = "127.0.0.1:8080"
	shutdownTimeout = 10 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("ocg-connector exited", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen %q: %w", listenAddr, err)
	}

	grpcServer := grpc.NewServer()
	ocgconnectorv1.RegisterOcgConnectorServiceServer(grpcServer, server.New())

	errCh := make(chan error, 1)
	go func() {
		logger.InfoContext(ctx, "ocg-connector listening", "addr", listenAddr)
		errCh <- grpcServer.Serve(lis)
	}()

	select {
	case <-ctx.Done():
		stopped := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(shutdownTimeout):
			grpcServer.Stop()
		}
		return nil
	case err := <-errCh:
		return err
	}
}
