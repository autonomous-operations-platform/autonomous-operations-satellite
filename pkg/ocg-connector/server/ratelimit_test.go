package server

import (
	"context"
	"testing"

	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

func TestNewLimiter(t *testing.T) {
	if NewLimiter(0, 10) != nil {
		t.Error("NewLimiter(0,...) should be nil (disabled)")
	}
	if NewLimiter(-5, 10) != nil {
		t.Error("NewLimiter(negative,...) should be nil (disabled)")
	}
	if NewLimiter(10, 20) == nil {
		t.Error("NewLimiter(10,20) should be non-nil")
	}
}

func TestUnaryRateLimitInterceptor(t *testing.T) {
	interceptor := UnaryRateLimitInterceptor(rate.NewLimiter(0, 1))

	called := 0
	handler := func(_ context.Context, _ any) (any, error) {
		called++
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/ocgconnector.v1.OcgConnectorService/Fetch"}

	resp, err := interceptor(context.Background(), nil, info, handler)
	if err != nil {
		t.Fatalf("first call: unexpected error %v", err)
	}
	if resp != "ok" || called != 1 {
		t.Fatalf("first call: resp=%v called=%d", resp, called)
	}

	_, err = interceptor(context.Background(), nil, info, handler)
	if codeOf(err) != codes.ResourceExhausted {
		t.Fatalf("second call: code = %v, want ResourceExhausted", codeOf(err))
	}
	if called != 1 {
		t.Fatalf("handler should not run when throttled; called=%d", called)
	}
}
