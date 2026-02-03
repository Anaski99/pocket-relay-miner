//go:build test

package relayer

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/suite"

	"github.com/pokt-network/pocket-relay-miner/logging"
)

// MiddlewareTestSuite characterizes middleware behavior.
// Middleware wraps HTTP handlers to add cross-cutting concerns like
// panic recovery, logging, CORS, etc.
type MiddlewareTestSuite struct {
	suite.Suite

	logger logging.Logger
}

func TestMiddlewareTestSuite(t *testing.T) {
	suite.Run(t, new(MiddlewareTestSuite))
}

func (s *MiddlewareTestSuite) SetupTest() {
	s.logger = zerolog.New(io.Discard).With().Logger()
}

func (s *MiddlewareTestSuite) TestMiddleware_PanicRecovery_HandlesPanic() {
	// PanicRecoveryMiddleware should catch panics and return 500

	// Handler that panics
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	// Wrap with panic recovery
	wrapped := PanicRecoveryMiddleware(s.logger, panicHandler)

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()

	// Should not panic
	s.NotPanics(func() {
		wrapped.ServeHTTP(w, req)
	})

	// Should return 500
	resp := w.Result()
	defer resp.Body.Close()
	s.Equal(http.StatusInternalServerError, resp.StatusCode)

	// Response body should indicate error
	bodyBz, _ := io.ReadAll(resp.Body)
	s.Contains(string(bodyBz), "Internal Server Error")
}

func (s *MiddlewareTestSuite) TestMiddleware_PanicRecovery_NormalRequest() {
	// PanicRecoveryMiddleware should pass through normal requests

	// Normal handler that returns 200
	normalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("success"))
	})

	// Wrap with panic recovery
	wrapped := PanicRecoveryMiddleware(s.logger, normalHandler)

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()

	// Execute
	wrapped.ServeHTTP(w, req)

	// Should return 200
	resp := w.Result()
	defer resp.Body.Close()
	s.Equal(http.StatusOK, resp.StatusCode)

	// Body should contain success message
	bodyBz, _ := io.ReadAll(resp.Body)
	s.Equal("success", string(bodyBz))
}

func (s *MiddlewareTestSuite) TestMiddleware_PanicRecovery_PanicString() {
	// Panic with string should be logged and recovered

	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("panic message")
	})

	wrapped := PanicRecoveryMiddleware(s.logger, panicHandler)

	req := httptest.NewRequest(http.MethodPost, "/relay", nil)
	w := httptest.NewRecorder()

	// Should not crash
	s.NotPanics(func() {
		wrapped.ServeHTTP(w, req)
	})

	s.Equal(http.StatusInternalServerError, w.Result().StatusCode)
}

func (s *MiddlewareTestSuite) TestMiddleware_PanicRecovery_PanicError() {
	// Panic with error should be logged and recovered

	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(io.ErrUnexpectedEOF)
	})

	wrapped := PanicRecoveryMiddleware(s.logger, panicHandler)

	req := httptest.NewRequest(http.MethodPost, "/relay", nil)
	w := httptest.NewRecorder()

	// Should not crash
	s.NotPanics(func() {
		wrapped.ServeHTTP(w, req)
	})

	s.Equal(http.StatusInternalServerError, w.Result().StatusCode)
}

func (s *MiddlewareTestSuite) TestMiddleware_PanicRecovery_PanicNil() {
	// Panic with nil should be logged and recovered

	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(nil)
	})

	wrapped := PanicRecoveryMiddleware(s.logger, panicHandler)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	// Should not crash (nil panic is unusual but valid)
	s.NotPanics(func() {
		wrapped.ServeHTTP(w, req)
	})
}

func (s *MiddlewareTestSuite) TestMiddleware_PanicRecovery_MultiplePanics() {
	// Multiple panics should each be recovered independently

	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	wrapped := PanicRecoveryMiddleware(s.logger, panicHandler)

	// Execute multiple requests
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		w := httptest.NewRecorder()

		// Should not panic
		s.NotPanics(func() {
			wrapped.ServeHTTP(w, req)
		})

		s.Equal(http.StatusInternalServerError, w.Result().StatusCode)
	}
}

func (s *MiddlewareTestSuite) TestMiddleware_PanicRecovery_PreservesPath() {
	// Panic recovery should log the request path

	called := false
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		// Verify path is accessible before panic
		s.Equal("/relay/endpoint", r.URL.Path)
		panic("test")
	})

	wrapped := PanicRecoveryMiddleware(s.logger, panicHandler)

	req := httptest.NewRequest(http.MethodPost, "/relay/endpoint", nil)
	w := httptest.NewRecorder()

	wrapped.ServeHTTP(w, req)

	s.True(called, "handler should have been called")
	s.Equal(http.StatusInternalServerError, w.Result().StatusCode)
}

func (s *MiddlewareTestSuite) TestMiddleware_PanicRecovery_PreservesMethod() {
	// Panic recovery should log the request method

	called := false
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		s.Equal(http.MethodPut, r.Method)
		panic("test")
	})

	wrapped := PanicRecoveryMiddleware(s.logger, panicHandler)

	req := httptest.NewRequest(http.MethodPut, "/test", nil)
	w := httptest.NewRecorder()

	wrapped.ServeHTTP(w, req)

	s.True(called)
	s.Equal(http.StatusInternalServerError, w.Result().StatusCode)
}

func (s *MiddlewareTestSuite) TestMiddleware_PanicRecovery_PreservesRemoteAddr() {
	// Panic recovery should log the remote address

	called := false
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		s.NotEmpty(r.RemoteAddr)
		panic("test")
	})

	wrapped := PanicRecoveryMiddleware(s.logger, panicHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	w := httptest.NewRecorder()

	wrapped.ServeHTTP(w, req)

	s.True(called)
}

func (s *MiddlewareTestSuite) TestMiddleware_ChainOrder_PanicBeforeLogging() {
	// When chaining middleware, panic recovery should be outermost
	// to catch panics from inner middleware

	// Inner middleware that panics
	panicMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("inner panic")
		})
	}

	// Base handler
	baseHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Chain: PanicRecovery -> panicMiddleware -> baseHandler
	handler := PanicRecoveryMiddleware(s.logger, panicMiddleware(baseHandler))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()

	// Should not panic
	s.NotPanics(func() {
		handler.ServeHTTP(w, req)
	})

	s.Equal(http.StatusInternalServerError, w.Result().StatusCode)
}

func (s *MiddlewareTestSuite) TestMiddleware_ChainOrder_MultipleWraps() {
	// Multiple middleware should execute in correct order

	var executionOrder []string

	// Middleware 1: records "before1" and "after1"
	middleware1 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			executionOrder = append(executionOrder, "before1")
			next.ServeHTTP(w, r)
			executionOrder = append(executionOrder, "after1")
		})
	}

	// Middleware 2: records "before2" and "after2"
	middleware2 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			executionOrder = append(executionOrder, "before2")
			next.ServeHTTP(w, r)
			executionOrder = append(executionOrder, "after2")
		})
	}

	// Base handler
	baseHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		executionOrder = append(executionOrder, "handler")
		w.WriteHeader(http.StatusOK)
	})

	// Chain: middleware1 -> middleware2 -> baseHandler
	handler := middleware1(middleware2(baseHandler))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Expected order: before1, before2, handler, after2, after1
	expected := []string{"before1", "before2", "handler", "after2", "after1"}
	s.Equal(expected, executionOrder)
}

func (s *MiddlewareTestSuite) TestMiddleware_ResponseWriter_Write() {
	// Middleware should preserve response writes

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("custom response"))
	})

	wrapped := PanicRecoveryMiddleware(s.logger, handler)

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	w := httptest.NewRecorder()

	wrapped.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	s.Equal(http.StatusCreated, resp.StatusCode)
	bodyBz, _ := io.ReadAll(resp.Body)
	s.Equal("custom response", string(bodyBz))
}

func (s *MiddlewareTestSuite) TestMiddleware_ResponseWriter_Headers() {
	// Middleware should preserve response headers

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom-Header", "custom-value")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	})

	wrapped := PanicRecoveryMiddleware(s.logger, handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()

	wrapped.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	s.Equal("custom-value", resp.Header.Get("X-Custom-Header"))
	s.Equal("application/json", resp.Header.Get("Content-Type"))
}

func (s *MiddlewareTestSuite) TestMiddleware_RequestBody() {
	// Middleware should preserve request body

	requestBody := "test request body"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBz, err := io.ReadAll(r.Body)
		s.Require().NoError(err)
		s.Equal(requestBody, string(bodyBz))
		w.WriteHeader(http.StatusOK)
	})

	wrapped := PanicRecoveryMiddleware(s.logger, handler)

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(requestBody))
	w := httptest.NewRecorder()

	wrapped.ServeHTTP(w, req)

	s.Equal(http.StatusOK, w.Result().StatusCode)
}

func (s *MiddlewareTestSuite) TestMiddleware_ConcurrentRequests() {
	// Middleware should handle concurrent requests safely

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	wrapped := PanicRecoveryMiddleware(s.logger, handler)

	done := make(chan bool, 100)

	// Execute 100 concurrent requests
	for i := 0; i < 100; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			w := httptest.NewRecorder()
			wrapped.ServeHTTP(w, req)
			s.Equal(http.StatusOK, w.Result().StatusCode)
			done <- true
		}()
	}

	// Wait for all to complete
	for i := 0; i < 100; i++ {
		<-done
	}
}

func (s *MiddlewareTestSuite) TestMiddleware_ConcurrentPanics() {
	// Middleware should handle concurrent panics safely

	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("concurrent panic")
	})

	wrapped := PanicRecoveryMiddleware(s.logger, panicHandler)

	done := make(chan bool, 100)

	// Execute 100 concurrent requests that panic
	for i := 0; i < 100; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			w := httptest.NewRecorder()

			// Should not panic
			s.NotPanics(func() {
				wrapped.ServeHTTP(w, req)
			})

			s.Equal(http.StatusInternalServerError, w.Result().StatusCode)
			done <- true
		}()
	}

	// Wait for all to complete
	for i := 0; i < 100; i++ {
		<-done
	}
}
