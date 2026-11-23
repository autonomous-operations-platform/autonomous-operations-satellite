// Command ocg-connector serves satellite records to the OCG Satellite Connector
// over gRPC.
//
// Configuration:
//
//	-listen      gRPC listen address (default "127.0.0.1:8080")
//	-reflection  enable gRPC server reflection (dev only)
package main

import (
	"context"
	"flag"
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
	"google.golang.org/grpc/reflection"
)

const shutdownTimeout = 10 * time.Second

type config struct {
	listen     string
	reflection bool
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("ocg-connector exited", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := server.New()
	return serve(ctx, cfg.listen, cfg.reflection, srv, logger)
}

func serve(ctx context.Context, listen string, enableReflection bool, srv *server.Server, logger *slog.Logger) error {
	lis, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen %q: %w", listen, err)
	}

	grpcServer := grpc.NewServer()
	ocgconnectorv1.RegisterOcgConnectorServiceServer(grpcServer, srv)
	if enableReflection {
		reflection.Register(grpcServer)
	}

	errCh := make(chan error, 1)
	go func() {
		logger.InfoContext(ctx, "ocg-connector listening", "addr", listen)
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

// parseConfig builds the configuration from command-line arguments.
func parseConfig(args []string) (config, error) {
	fs := flag.NewFlagSet("ocg-connector", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "gRPC listen address (no TLS/auth; see package docs before exposing beyond localhost)")
	enableReflection := fs.Bool("reflection", false, "enable gRPC server reflection (dev only)")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}

	return config{
		listen:     *listen,
		reflection: *enableReflection,
	}, nil
}
