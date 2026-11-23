package server

import (
	"context"

	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NewLimiter returns a token-bucket limiter, or nil when perSecond <= 0
// (rate limiting disabled).
func NewLimiter(perSecond float64, burst int) *rate.Limiter {
	if perSecond <= 0 {
		return nil
	}
	return rate.NewLimiter(rate.Limit(perSecond), burst)
}

// UnaryRateLimitInterceptor rejects calls with ResourceExhausted when the
// global token bucket is empty, without invoking the handler.
func UnaryRateLimitInterceptor(l *rate.Limiter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !l.Allow() {
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}
		return handler(ctx, req)
	}
}
