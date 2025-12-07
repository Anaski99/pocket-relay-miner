package relayer

import (
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/pokt-network/pocket-relay-miner/logging"
)

// PanicRecoveryMiddleware wraps an HTTP handler with panic recovery.
// If a panic occurs during request handling, it:
// 1. Logs the panic with full context (method, path, stack trace)
// 2. Increments panic recovery metrics
// 3. Returns HTTP 500 Internal Server Error to the client
//
// This prevents a single panic from crashing the entire relayer process,
// ensuring high availability even when bugs occur.
//
// Example usage:
//
//	mux := http.NewServeMux()
//	mux.HandleFunc("/relay", handleRelay)
//
//	server := &http.Server{
//	    Handler: PanicRecoveryMiddleware(logger, mux),
//	}
func PanicRecoveryMiddleware(logger logging.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				// Increment panic recovery metric
				logging.PanicRecoveriesTotal.WithLabelValues("http_handler").Inc()

				// Log panic with full context
				logger.Error().
					Str(logging.FieldComponent, "http_handler").
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Str("remote_addr", r.RemoteAddr).
					Str("panic_value", fmt.Sprintf("%v", rec)).
					Str("stack_trace", string(debug.Stack())).
					Msg("PANIC RECOVERED in HTTP handler")

				// Return 500 to client (connection may already be closed)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)
	})
}
