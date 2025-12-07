package relayer

import (
	"context"
	"fmt"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pokt-network/pocket-relay-miner/logging"
)

// UnaryPanicRecoveryInterceptor creates a gRPC unary server interceptor for panic recovery.
// If a panic occurs during unary RPC handling, it:
// 1. Logs the panic with full context (method, stack trace)
// 2. Increments panic recovery metrics
// 3. Returns gRPC Internal error to the client
//
// This prevents a single panic from crashing the entire relayer process.
//
// Example usage:
//
//	grpcServer := grpc.NewServer(
//	    grpc.UnaryInterceptor(UnaryPanicRecoveryInterceptor(logger)),
//	)
func UnaryPanicRecoveryInterceptor(logger logging.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				// Increment panic recovery metric
				logging.PanicRecoveriesTotal.WithLabelValues("grpc_handler").Inc()

				// Log panic with full context
				logger.Error().
					Str(logging.FieldComponent, "grpc_handler").
					Str("method", info.FullMethod).
					Str("panic_value", fmt.Sprintf("%v", r)).
					Str("stack_trace", string(debug.Stack())).
					Msg("PANIC RECOVERED in gRPC unary handler")

				// Return Internal error to client
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()

		return handler(ctx, req)
	}
}

// StreamPanicRecoveryInterceptor creates a gRPC stream server interceptor for panic recovery.
// If a panic occurs during stream RPC handling, it:
// 1. Logs the panic with full context (method, stack trace)
// 2. Increments panic recovery metrics
// 3. Returns gRPC Internal error to the client
//
// This prevents a single panic from crashing the entire relayer process.
//
// Example usage:
//
//	grpcServer := grpc.NewServer(
//	    grpc.StreamInterceptor(StreamPanicRecoveryInterceptor(logger)),
//	)
func StreamPanicRecoveryInterceptor(logger logging.Logger) grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) (err error) {
		defer func() {
			if r := recover(); r != nil {
				// Increment panic recovery metric
				logging.PanicRecoveriesTotal.WithLabelValues("grpc_stream").Inc()

				// Log panic with full context
				logger.Error().
					Str(logging.FieldComponent, "grpc_stream").
					Str("method", info.FullMethod).
					Str("panic_value", fmt.Sprintf("%v", r)).
					Str("stack_trace", string(debug.Stack())).
					Msg("PANIC RECOVERED in gRPC stream handler")

				// Return Internal error to client
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()

		return handler(srv, ss)
	}
}
