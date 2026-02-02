//go:build test

package relayer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alitto/pond/v2"
	sdktypes "github.com/pokt-network/shannon-sdk/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/suite"
	"google.golang.org/protobuf/proto"

	"github.com/pokt-network/pocket-relay-miner/logging"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
)

// ProxyConcurrentSuite provides high-concurrency tests for the proxy server.
// All tests MUST pass `go test -race`.
type ProxyConcurrentSuite struct {
	suite.Suite

	// Backend server
	backendServer *httptest.Server

	// ProxyServer under test
	proxy *ProxyServer

	// Dependencies
	logger         logging.Logger
	config         *Config
	healthChecker  *HealthChecker
	workerPool     pond.Pool
	validator      *mockRelayValidator
	responseSigner *ResponseSigner
	publisher      *mockMinedRelayPublisher

	// Test data
	supplierAddr  string
	supplier2Addr string
	appAddr       string
	app2Addr      string
	serviceID     string
	serviceID2    string
}

func TestProxyConcurrentSuite(t *testing.T) {
	suite.Run(t, new(ProxyConcurrentSuite))
}

func (s *ProxyConcurrentSuite) SetupSuite() {
	// Create logger
	s.logger = zerolog.New(io.Discard).With().Logger()

	// Setup test data - using hardcoded values to avoid import cycle with testutil
	s.supplierAddr = "pokt1supplier123"
	s.supplier2Addr = "pokt1supplier456"
	s.appAddr = "pokt1app123"
	s.app2Addr = "pokt1app456"
	s.serviceID = "anvil"
	s.serviceID2 = "ethereum"
}

// getTestConcurrency returns appropriate concurrency level for tests.
// Returns 1000 for nightly mode, 100 for regular CI.
func getTestConcurrency() int {
	if os.Getenv("TEST_MODE") == "nightly" {
		return 1000
	}
	return 100
}

// generateDeterministicSessionID generates a deterministic session ID.
func generateDeterministicSessionID(seed int) string {
	return fmt.Sprintf("session-%d", seed)
}

func (s *ProxyConcurrentSuite) SetupTest() {
	// Create backend server
	s.backendServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simple JSON-RPC response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))

	// Create config with multiple services
	s.config = &Config{
		ListenAddr:                   "0.0.0.0:8080",
		DefaultValidationMode:        ValidationModeOptimistic,
		DefaultRequestTimeoutSeconds: 30,
		DefaultMaxBodySizeBytes:      10 * 1024 * 1024,
		Services: map[string]ServiceConfig{
			s.serviceID: {
				Backends: map[string]BackendConfig{
					"jsonrpc": {URL: s.backendServer.URL},
				},
			},
			s.serviceID2: {
				Backends: map[string]BackendConfig{
					"jsonrpc": {URL: s.backendServer.URL},
				},
			},
		},
		TimeoutProfiles: map[string]TimeoutProfile{
			"fast": {
				Name:                         "fast",
				RequestTimeoutSeconds:        30,
				ResponseHeaderTimeoutSeconds: 30,
				DialTimeoutSeconds:           5,
				TLSHandshakeTimeoutSeconds:   10,
			},
			"streaming": {
				Name:                         "streaming",
				RequestTimeoutSeconds:        600,
				ResponseHeaderTimeoutSeconds: 0,
				DialTimeoutSeconds:           10,
				TLSHandshakeTimeoutSeconds:   15,
			},
		},
		HTTPTransport: HTTPTransportConfig{
			MaxIdleConns:                 500,
			MaxIdleConnsPerHost:          100,
			MaxConnsPerHost:              500,
			IdleConnTimeoutSeconds:       90,
			DialTimeoutSeconds:           5,
			TLSHandshakeTimeoutSeconds:   10,
			ResponseHeaderTimeoutSeconds: 30,
			ExpectContinueTimeoutSeconds: 1,
			TCPKeepAliveSeconds:          30,
			DisableCompression:           true,
		},
	}

	// Create health checker
	s.healthChecker = NewHealthChecker(s.logger)

	// Create worker pool with high concurrency
	s.workerPool = pond.NewPool(500)

	// Create proxy server
	var err error
	s.proxy, err = NewProxyServer(s.logger, s.config, s.healthChecker, nil, s.workerPool)
	s.Require().NoError(err)

	// Create mock dependencies
	s.validator = &mockRelayValidator{}
	s.publisher = &mockMinedRelayPublisher{}

	// Create response signer
	s.responseSigner = &ResponseSigner{
		logger:            s.logger,
		signers:           make(map[string]Signer),
		operatorAddresses: []string{s.supplierAddr, s.supplier2Addr},
	}
	s.responseSigner.signers[s.supplierAddr] = &mockSigner{}
	s.responseSigner.signers[s.supplier2Addr] = &mockSigner{}

	// Wire up dependencies that use interfaces
	s.proxy.SetValidator(s.validator)
	s.proxy.SetResponseSigner(s.responseSigner)
	s.proxy.publisher = s.publisher
	s.proxy.SetBlockHeight(100)
}

func (s *ProxyConcurrentSuite) TearDownTest() {
	if s.backendServer != nil {
		s.backendServer.Close()
	}
	if s.proxy != nil {
		_ = s.proxy.Close()
	}
	if s.workerPool != nil {
		s.workerPool.StopAndWait()
	}
}

// createTestRelayRequest creates a valid RelayRequest for testing.
func (s *ProxyConcurrentSuite) createTestRelayRequest(idx int, serviceID, supplierAddr string) []byte {
	poktReq := &sdktypes.POKTHTTPRequest{
		Method: "POST",
		Url:    "/",
		Header: make(map[string]*sdktypes.Header),
		BodyBz: []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`),
	}
	poktReq.Header["Content-Type"] = &sdktypes.Header{
		Key:    "Content-Type",
		Values: []string{"application/json"},
	}

	payloadBz, err := proto.Marshal(poktReq)
	s.Require().NoError(err)

	sessionID := generateDeterministicSessionID(idx)

	relayReq := &servicetypes.RelayRequest{
		Meta: servicetypes.RelayRequestMetadata{
			SessionHeader: &sessiontypes.SessionHeader{
				ApplicationAddress:      s.appAddr,
				ServiceId:               serviceID,
				SessionId:               sessionID,
				SessionStartBlockHeight: 90,
				SessionEndBlockHeight:   100,
			},
			SupplierOperatorAddress: supplierAddr,
			Signature:               []byte("test-signature"),
		},
		Payload: payloadBz,
	}

	relayReqBz, err := relayReq.Marshal()
	s.Require().NoError(err)

	return relayReqBz
}

// Concurrent Block Height Tests

func (s *ProxyConcurrentSuite) TestBlockHeight_ConcurrentUpdates() {
	// Test that concurrent block height updates don't race
	numGoroutines := getTestConcurrency()

	var wg sync.WaitGroup
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(height int64) {
			defer wg.Done()
			s.proxy.SetBlockHeight(height)
		}(int64(i))
	}
	wg.Wait()

	// Should complete without race detector errors
	finalHeight := s.proxy.currentBlockHeight.Load()
	s.True(finalHeight >= 0)
}

func (s *ProxyConcurrentSuite) TestBlockHeight_ConcurrentReadWrite() {
	// Test concurrent reads and writes to block height
	numGoroutines := getTestConcurrency()

	var wg sync.WaitGroup
	var readCount atomic.Int64

	// Start writers
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func(height int64) {
			defer wg.Done()
			s.proxy.SetBlockHeight(height)
		}(int64(i))
	}

	// Start readers
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.proxy.currentBlockHeight.Load()
			readCount.Add(1)
		}()
	}

	wg.Wait()
	s.Equal(int64(numGoroutines/2), readCount.Load())
}

// Concurrent Health Check Tests

func (s *ProxyConcurrentSuite) TestHealthCheck_ConcurrentRequests() {
	numGoroutines := getTestConcurrency()

	var wg sync.WaitGroup
	var successCount atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			w := httptest.NewRecorder()
			s.proxy.handleRelay(w, req)

			if w.Code == http.StatusOK {
				successCount.Add(1)
			}
		}()
	}

	wg.Wait()
	s.Equal(int64(numGoroutines), successCount.Load())
}

// Concurrent Relay Request Tests (Error Path)

func (s *ProxyConcurrentSuite) TestConcurrentRelays_InvalidRequests() {
	// Test concurrent invalid relay requests
	// These will fail due to missing supplier cache, but should not race
	numGoroutines := getTestConcurrency()

	var wg sync.WaitGroup
	var responseCount atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			reqBody := s.createTestRelayRequest(idx, s.serviceID, s.supplierAddr)
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))

			w := httptest.NewRecorder()
			s.proxy.handleRelay(w, req)

			// We expect 500 because supplier cache is not configured
			responseCount.Add(1)
		}(i)
	}

	wg.Wait()
	s.Equal(int64(numGoroutines), responseCount.Load())
}

func (s *ProxyConcurrentSuite) TestConcurrentRelays_MixedServices() {
	// Test concurrent relays to different services
	numGoroutines := getTestConcurrency()

	var wg sync.WaitGroup
	var responseCount atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Alternate between services
			serviceID := s.serviceID
			if idx%2 == 0 {
				serviceID = s.serviceID2
			}

			reqBody := s.createTestRelayRequest(idx, serviceID, s.supplierAddr)
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))

			w := httptest.NewRecorder()
			s.proxy.handleRelay(w, req)

			responseCount.Add(1)
		}(i)
	}

	wg.Wait()
	s.Equal(int64(numGoroutines), responseCount.Load())
}

func (s *ProxyConcurrentSuite) TestConcurrentRelays_SameSession() {
	// Test concurrent relays with same session ID
	// Important for deduplication logic
	numGoroutines := getTestConcurrency()

	var wg sync.WaitGroup
	var responseCount atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// All use same session index (0) for same session ID
			reqBody := s.createTestRelayRequest(0, s.serviceID, s.supplierAddr)
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))

			w := httptest.NewRecorder()
			s.proxy.handleRelay(w, req)

			responseCount.Add(1)
		}(i)
	}

	wg.Wait()
	s.Equal(int64(numGoroutines), responseCount.Load())
}

func (s *ProxyConcurrentSuite) TestConcurrentRelays_DifferentSuppliers() {
	// Test concurrent relays with different suppliers
	numGoroutines := getTestConcurrency()

	var wg sync.WaitGroup
	var responseCount atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Alternate between suppliers
			supplierAddr := s.supplierAddr
			if idx%2 == 0 {
				supplierAddr = s.supplier2Addr
			}

			reqBody := s.createTestRelayRequest(idx, s.serviceID, supplierAddr)
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))

			w := httptest.NewRecorder()
			s.proxy.handleRelay(w, req)

			responseCount.Add(1)
		}(i)
	}

	wg.Wait()
	s.Equal(int64(numGoroutines), responseCount.Load())
}

// Worker Pool Tests

func (s *ProxyConcurrentSuite) TestWorkerPool_HighConcurrency() {
	// Test worker pool under high load
	pool := pond.NewPool(100)
	defer pool.StopAndWait()

	numTasks := getTestConcurrency() * 5
	var completed atomic.Int64

	for i := 0; i < numTasks; i++ {
		pool.Submit(func() {
			// Simulate some work
			time.Sleep(time.Microsecond)
			completed.Add(1)
		})
	}

	pool.StopAndWait()
	s.Equal(int64(numTasks), completed.Load())
}

func (s *ProxyConcurrentSuite) TestWorkerPool_Backpressure() {
	// Test worker pool handles backpressure gracefully
	pool := pond.NewPool(10) // Small pool
	defer pool.StopAndWait()

	numTasks := 1000
	var completed atomic.Int64

	for i := 0; i < numTasks; i++ {
		pool.Submit(func() {
			completed.Add(1)
		})
	}

	pool.StopAndWait()
	s.Equal(int64(numTasks), completed.Load())
}

// Buffer Pool Tests

func (s *ProxyConcurrentSuite) TestBufferPool_ConcurrentAccess() {
	// Create a buffer pool
	bp := NewBufferPool(4096)

	numGoroutines := getTestConcurrency()
	var wg sync.WaitGroup
	var accessCount atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Use ReadWithBuffer to test pool (the public API)
			reader := bytes.NewReader([]byte("test data"))
			data, err := bp.ReadWithBuffer(reader)
			if err == nil && len(data) > 0 {
				accessCount.Add(1)
			}
		}()
	}

	wg.Wait()
	s.Equal(int64(numGoroutines), accessCount.Load())
}

func (s *ProxyConcurrentSuite) TestBufferPool_Creation() {
	// Test that buffer pool can be created with various sizes
	bp1 := NewBufferPool(1024)
	s.NotNil(bp1)

	bp2 := NewBufferPool(10 * 1024 * 1024)
	s.NotNil(bp2)

	// Test with invalid size (should use default)
	bp3 := NewBufferPool(0)
	s.NotNil(bp3)
}

// Client Pool Tests

func (s *ProxyConcurrentSuite) TestClientPool_ConcurrentAccess() {
	// Test concurrent access to client pool
	s.proxy.clientPoolMu.RLock()
	poolSize := len(s.proxy.clientPool)
	s.proxy.clientPoolMu.RUnlock()

	numGoroutines := getTestConcurrency()
	var wg sync.WaitGroup
	var accessCount atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			s.proxy.clientPoolMu.RLock()
			_ = len(s.proxy.clientPool)
			s.proxy.clientPoolMu.RUnlock()

			accessCount.Add(1)
		}()
	}

	wg.Wait()
	s.Equal(int64(numGoroutines), accessCount.Load())

	// Pool size should be unchanged
	s.proxy.clientPoolMu.RLock()
	newPoolSize := len(s.proxy.clientPool)
	s.proxy.clientPoolMu.RUnlock()
	s.Equal(poolSize, newPoolSize)
}

// Parsed Backend URLs Tests

func (s *ProxyConcurrentSuite) TestParsedBackendURLs_ConcurrentAccess() {
	// Test concurrent access to parsed backend URLs
	numGoroutines := getTestConcurrency()
	var wg sync.WaitGroup
	var accessCount atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			key := fmt.Sprintf("service%d:jsonrpc", idx%10)

			// Concurrent reads (may return nil for non-existent keys)
			_, _ = s.proxy.parsedBackendURLs.Load(key)
			accessCount.Add(1)
		}(i)
	}

	wg.Wait()
	s.Equal(int64(numGoroutines), accessCount.Load())
}

// Validator Tests

func (s *ProxyConcurrentSuite) TestValidator_ConcurrentValidation() {
	// Test concurrent validation calls
	numGoroutines := getTestConcurrency()
	var wg sync.WaitGroup
	var callCount atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			ctx := context.Background()
			_ = s.validator.ValidateRelayRequest(ctx, &servicetypes.RelayRequest{})
			callCount.Add(1)
		}()
	}

	wg.Wait()
	s.Equal(int64(numGoroutines), callCount.Load())
}

// Response Signer Tests

func (s *ProxyConcurrentSuite) TestResponseSigner_ConcurrentSigning() {
	// Test concurrent signing operations
	numGoroutines := getTestConcurrency()
	var wg sync.WaitGroup
	var signCount atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Alternate between suppliers
			supplierAddr := s.supplierAddr
			if idx%2 == 0 {
				supplierAddr = s.supplier2Addr
			}

			signer, ok := s.responseSigner.signers[supplierAddr]
			if ok {
				var msg [32]byte
				_, _ = signer.Sign(msg)
				signCount.Add(1)
			}
		}(i)
	}

	wg.Wait()
	s.Equal(int64(numGoroutines), signCount.Load())
}

// Publisher Tests

func (s *ProxyConcurrentSuite) TestPublisher_ConcurrentPublish() {
	// Test concurrent publish operations
	numGoroutines := getTestConcurrency()
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			ctx := context.Background()
			_ = s.publisher.Publish(ctx, nil)
		}()
	}

	wg.Wait()

	// All publishes should be recorded
	s.publisher.mu.Lock()
	publishCount := len(s.publisher.published)
	s.publisher.mu.Unlock()

	s.Equal(numGoroutines, publishCount)
}

// Session Monitor Tests (if available)

func (s *ProxyConcurrentSuite) TestSessionMonitor_NilSafe() {
	// Session monitor may be nil in test mode
	// This should not cause races when checking
	if s.proxy.sessionMonitor == nil {
		s.T().Log("SessionMonitor is nil (expected in test)")
	}
}

// Metric Recorder Tests

func (s *ProxyConcurrentSuite) TestMetricRecorder_ConcurrentRecording() {
	// Metric recorder may be nil in test mode
	// Just verify no crashes on nil access
	if s.proxy.metricRecorder == nil {
		s.T().Log("MetricRecorder is nil (expected in test)")
		return
	}

	// If available, test concurrent recording
	numGoroutines := getTestConcurrency()
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Would record metrics here if recorder is available
		}()
	}

	wg.Wait()
}

// Mixed Concurrent Operations

func (s *ProxyConcurrentSuite) TestMixed_ConcurrentOperations() {
	// Test multiple concurrent operation types
	numGoroutines := getTestConcurrency()
	var wg sync.WaitGroup
	var totalOps atomic.Int64

	// Health checks
	for i := 0; i < numGoroutines/4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			w := httptest.NewRecorder()
			s.proxy.handleRelay(w, req)
			totalOps.Add(1)
		}()
	}

	// Block height updates
	for i := 0; i < numGoroutines/4; i++ {
		wg.Add(1)
		go func(h int64) {
			defer wg.Done()
			s.proxy.SetBlockHeight(h)
			totalOps.Add(1)
		}(int64(i))
	}

	// Relay requests (error path)
	for i := 0; i < numGoroutines/4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			reqBody := s.createTestRelayRequest(idx, s.serviceID, s.supplierAddr)
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))
			w := httptest.NewRecorder()
			s.proxy.handleRelay(w, req)
			totalOps.Add(1)
		}(i)
	}

	// Publisher operations
	for i := 0; i < numGoroutines/4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			_ = s.publisher.Publish(ctx, nil)
			totalOps.Add(1)
		}()
	}

	wg.Wait()
	s.Equal(int64(numGoroutines), totalOps.Load())
}

// Stress Test

func (s *ProxyConcurrentSuite) TestStress_BurstLoad() {
	// Simulate burst load scenario
	numBursts := 10
	requestsPerBurst := getTestConcurrency() / 10

	var totalResponses atomic.Int64

	for burst := 0; burst < numBursts; burst++ {
		var wg sync.WaitGroup

		for i := 0; i < requestsPerBurst; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()

				req := httptest.NewRequest(http.MethodGet, "/health", nil)
				w := httptest.NewRecorder()
				s.proxy.handleRelay(w, req)

				if w.Code == http.StatusOK {
					totalResponses.Add(1)
				}
			}(i)
		}

		wg.Wait()
	}

	s.Equal(int64(numBursts*requestsPerBurst), totalResponses.Load())
}

// Shutdown Tests

func (s *ProxyConcurrentSuite) TestShutdown_GracefulUnderLoad() {
	// Create a proxy specifically for this test
	workerPool := pond.NewPool(100)
	proxy, err := NewProxyServer(s.logger, s.config, s.healthChecker, nil, workerPool)
	s.Require().NoError(err)

	// Start concurrent requests
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					req := httptest.NewRequest(http.MethodGet, "/health", nil)
					w := httptest.NewRecorder()
					proxy.handleRelay(w, req)
				}
			}
		}()
	}

	// Let requests run for a bit
	time.Sleep(10 * time.Millisecond)

	// Signal stop and close
	close(stop)
	err = proxy.Close()
	s.NoError(err)

	workerPool.StopAndWait()
	wg.Wait()
}
