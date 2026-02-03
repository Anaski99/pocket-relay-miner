//go:build test

package relayer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdktypes "github.com/pokt-network/shannon-sdk/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/pokt-network/pocket-relay-miner/logging"
	"github.com/pokt-network/pocket-relay-miner/testutil"
	"github.com/pokt-network/pocket-relay-miner/transport"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
)

// GRPCRelayServiceTestSuite provides characterization tests for gRPC transport.
// Tests the relay_grpc_service.go implementation using real gRPC client/server.
type GRPCRelayServiceTestSuite struct {
	suite.Suite

	// Dependencies
	logger         logging.Logger
	backendServer  *httptest.Server
	grpcService    *RelayGRPCService
	grpcServer     *grpc.Server
	grpcListener   net.Listener
	grpcClientConn *grpc.ClientConn

	// Test data
	supplierAddr string
	appAddr      string
	serviceID    string

	// Mocks
	publisher      *grpcMockPublisher
	responseSigner *ResponseSigner
}

func TestGRPCRelayServiceTestSuite(t *testing.T) {
	suite.Run(t, new(GRPCRelayServiceTestSuite))
}

func (s *GRPCRelayServiceTestSuite) SetupSuite() {
	s.logger = zerolog.New(io.Discard).With().Logger()

	// Use testutil for deterministic addresses
	s.supplierAddr = testutil.TestSupplierAddress()
	s.appAddr = testutil.TestAppAddress()
	s.serviceID = testutil.TestServiceID
}

func (s *GRPCRelayServiceTestSuite) SetupTest() {
	// Create backend HTTP server for relay forwarding
	s.backendServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Default: return 200 OK with JSON-RPC response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))

	// Create mocks
	s.publisher = &grpcMockPublisher{}

	// Create response signer with mock signer
	s.responseSigner = &ResponseSigner{
		logger:            s.logger,
		signers:           make(map[string]Signer),
		operatorAddresses: []string{s.supplierAddr},
	}
	s.responseSigner.signers[s.supplierAddr] = &grpcMockSigner{}

	// Create gRPC relay service
	blockHeight := &atomic.Int64{}
	blockHeight.Store(100)

	serviceConfigs := map[string]ServiceConfig{
		s.serviceID: {
			Backends: map[string]BackendConfig{
				"rest": {
					URL: s.backendServer.URL,
				},
				"grpc": {
					URL: s.backendServer.URL,
				},
			},
		},
	}

	s.grpcService = NewRelayGRPCService(s.logger, RelayGRPCServiceConfig{
		ServiceConfigs:     serviceConfigs,
		ResponseSigner:     s.responseSigner,
		Publisher:          s.publisher,
		RelayProcessor:     nil,     // Most tests use nil
		RelayPipeline:      nil,     // Use nil to skip validation/metering for most tests
		CurrentBlockHeight: blockHeight,
		MaxBodySize:        10 * 1024 * 1024,
		BufferPool:         NewBufferPool(10 * 1024 * 1024),
	})

	// Create gRPC server with relay service
	s.grpcServer = NewGRPCServerForRelayService(s.grpcService)

	// Start gRPC server on random port
	var err error
	s.grpcListener, err = net.Listen("tcp", "127.0.0.1:0")
	s.Require().NoError(err)

	go func() {
		_ = s.grpcServer.Serve(s.grpcListener)
	}()

	// Create gRPC client connection
	s.grpcClientConn, err = grpc.NewClient(
		s.grpcListener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	s.Require().NoError(err)
}

func (s *GRPCRelayServiceTestSuite) TearDownTest() {
	if s.grpcClientConn != nil {
		_ = s.grpcClientConn.Close()
	}
	if s.grpcServer != nil {
		s.grpcServer.Stop()
	}
	if s.grpcListener != nil {
		_ = s.grpcListener.Close()
	}
	if s.backendServer != nil {
		s.backendServer.Close()
	}
}

// createTestRelayRequest creates a valid RelayRequest protobuf for testing.
func (s *GRPCRelayServiceTestSuite) createTestRelayRequest() *servicetypes.RelayRequest {
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

	return &servicetypes.RelayRequest{
		Meta: servicetypes.RelayRequestMetadata{
			SessionHeader: &sessiontypes.SessionHeader{
				ApplicationAddress:      s.appAddr,
				ServiceId:               s.serviceID,
				SessionId:               "test-session-123",
				SessionStartBlockHeight: 90,
				SessionEndBlockHeight:   100,
			},
			SupplierOperatorAddress: s.supplierAddr,
			Signature:               []byte("test-signature"),
		},
		Payload: payloadBz,
	}
}

// sendRelayViaGRPC sends a RelayRequest via gRPC and returns the response.
func (s *GRPCRelayServiceTestSuite) sendRelayViaGRPC(ctx context.Context, req *servicetypes.RelayRequest) (*servicetypes.RelayResponse, error) {
	// Create gRPC stream manually for SendRelay method
	stream, err := s.grpcClientConn.NewStream(ctx, &grpc.StreamDesc{
		StreamName:    "SendRelay",
		ServerStreams: false,
		ClientStreams: false,
	}, RelayServiceMethodPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create stream: %w", err)
	}

	// Send request
	if err := stream.SendMsg(req); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	// Close send direction
	if err := stream.CloseSend(); err != nil {
		return nil, fmt.Errorf("failed to close send: %w", err)
	}

	// Receive response
	resp := &servicetypes.RelayResponse{}
	if err := stream.RecvMsg(resp); err != nil {
		return nil, err
	}

	return resp, nil
}

// Test Cases

func (s *GRPCRelayServiceTestSuite) TestGRPCRelay_UnarySuccess() {
	// Test: Valid unary gRPC relay request returns correct response with supplier signature

	ctx := context.Background()
	relayReq := s.createTestRelayRequest()

	// Send relay
	resp, err := s.sendRelayViaGRPC(ctx, relayReq)
	s.Require().NoError(err)
	s.Require().NotNil(resp)

	// Verify response structure
	s.NotNil(resp.Meta)
	s.NotEmpty(resp.Meta.SupplierOperatorSignature)
	s.NotEmpty(resp.Payload)

	// Verify signature was produced by mock signer
	s.Equal([]byte("grpc-test-signature-32-chars-ok"), resp.Meta.SupplierOperatorSignature)

	// Verify response payload contains backend data
	poktResp, err := sdktypes.DeserializeHTTPResponse(resp.Payload)
	s.Require().NoError(err)
	s.Contains(string(poktResp.BodyBz), "jsonrpc")
}

func (s *GRPCRelayServiceTestSuite) TestGRPCRelay_MissingServiceID() {
	// Test: Request without service ID metadata returns appropriate gRPC status error

	ctx := context.Background()
	relayReq := s.createTestRelayRequest()

	// Remove service ID
	relayReq.Meta.SessionHeader.ServiceId = ""

	// Send relay - should return error
	resp, err := s.sendRelayViaGRPC(ctx, relayReq)

	// Verify gRPC error
	s.Require().Error(err)
	s.Nil(resp)

	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.InvalidArgument, st.Code())
	s.Contains(st.Message(), "service ID")
}

func (s *GRPCRelayServiceTestSuite) TestGRPCRelay_MissingSupplierAddress() {
	// Test: Request without supplier address returns appropriate gRPC status error

	ctx := context.Background()
	relayReq := s.createTestRelayRequest()

	// Remove supplier address
	relayReq.Meta.SupplierOperatorAddress = ""

	// Send relay - should return error
	resp, err := s.sendRelayViaGRPC(ctx, relayReq)

	// Verify gRPC error
	s.Require().Error(err)
	s.Nil(resp)

	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.InvalidArgument, st.Code())
	s.Contains(st.Message(), "supplier operator address")
}

func (s *GRPCRelayServiceTestSuite) TestGRPCRelay_MissingSessionHeader() {
	// Test: Request without session header returns appropriate gRPC error

	ctx := context.Background()
	relayReq := s.createTestRelayRequest()

	// Remove session header
	relayReq.Meta.SessionHeader = nil

	// Send relay - should return error
	resp, err := s.sendRelayViaGRPC(ctx, relayReq)

	// Verify gRPC error
	s.Require().Error(err)
	s.Nil(resp)

	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.InvalidArgument, st.Code())
	s.Contains(st.Message(), "session header")
}

func (s *GRPCRelayServiceTestSuite) TestGRPCRelay_InvalidPayload() {
	// Test: Malformed relay payload returns appropriate gRPC error

	ctx := context.Background()
	relayReq := s.createTestRelayRequest()

	// Set invalid payload (not valid POKTHTTPRequest proto)
	relayReq.Payload = []byte("invalid-protobuf-data")

	// Send relay - should return error
	resp, err := s.sendRelayViaGRPC(ctx, relayReq)

	// Verify gRPC error
	s.Require().Error(err)
	s.Nil(resp)

	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.InvalidArgument, st.Code())
	s.Contains(st.Message(), "deserialize")
}

func (s *GRPCRelayServiceTestSuite) TestGRPCRelay_NoSignerForSupplier() {
	// Test: Request for supplier without signer returns FailedPrecondition error

	ctx := context.Background()
	relayReq := s.createTestRelayRequest()

	// Use different supplier address (no signer)
	relayReq.Meta.SupplierOperatorAddress = "pokt1unknownsupplier"

	// Send relay - should return error
	resp, err := s.sendRelayViaGRPC(ctx, relayReq)

	// Verify gRPC error
	s.Require().Error(err)
	s.Nil(resp)

	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.FailedPrecondition, st.Code())
	s.Contains(st.Message(), "no signer")
}

func (s *GRPCRelayServiceTestSuite) TestGRPCRelay_UnknownService() {
	// Test: Request for unknown service returns NotFound error

	ctx := context.Background()
	relayReq := s.createTestRelayRequest()

	// Use unknown service ID
	relayReq.Meta.SessionHeader.ServiceId = "unknown-service"

	// Send relay - should return error
	resp, err := s.sendRelayViaGRPC(ctx, relayReq)

	// Verify gRPC error
	s.Require().Error(err)
	s.Nil(resp)

	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.NotFound, st.Code())
	s.Contains(st.Message(), "unknown service")
}

func (s *GRPCRelayServiceTestSuite) TestGRPCRelay_BackendTimeout() {
	// Test: Context deadline exceeded returns gRPC DeadlineExceeded error

	// Create slow backend that exceeds timeout
	slowBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slowBackend.Close()

	// Update service config with slow backend
	s.grpcService.serviceConfigs[s.serviceID] = ServiceConfig{
		Backends: map[string]BackendConfig{
			"rest": {URL: slowBackend.URL},
		},
	}

	// Use context with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	relayReq := s.createTestRelayRequest()

	// Send relay - should timeout
	resp, err := s.sendRelayViaGRPC(ctx, relayReq)

	// Actual behavior: Returns gRPC DeadlineExceeded error when context times out
	// This documents that gRPC context timeouts are handled at the transport layer
	s.Require().Error(err)
	s.Nil(resp)

	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.DeadlineExceeded, st.Code())
}

func (s *GRPCRelayServiceTestSuite) TestGRPCRelay_BackendUnavailable() {
	// Test: Backend unavailable returns error response

	// Configure backend with invalid URL
	s.grpcService.serviceConfigs[s.serviceID] = ServiceConfig{
		Backends: map[string]BackendConfig{
			"rest": {URL: "http://localhost:1"}, // Port 1 should be refused
		},
	}

	ctx := context.Background()
	relayReq := s.createTestRelayRequest()

	// Send relay - backend connection should fail
	resp, err := s.sendRelayViaGRPC(ctx, relayReq)

	// Should return error response (wrapped by service)
	s.Require().NoError(err)
	s.NotNil(resp)

	// Verify error response
	poktResp, err := sdktypes.DeserializeHTTPResponse(resp.Payload)
	s.Require().NoError(err)
	s.Equal(500, int(poktResp.StatusCode))
}

func (s *GRPCRelayServiceTestSuite) TestGRPCRelay_Backend5xxError() {
	// Test: Backend 5xx errors are NOT wrapped or mined (returns gRPC Unavailable)

	// Create backend that returns 503 Service Unavailable
	backend5xx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"service unavailable"}`))
	}))
	defer backend5xx.Close()

	s.grpcService.serviceConfigs[s.serviceID] = ServiceConfig{
		Backends: map[string]BackendConfig{
			"rest": {URL: backend5xx.URL},
		},
	}

	ctx := context.Background()
	relayReq := s.createTestRelayRequest()

	// Send relay
	resp, err := s.sendRelayViaGRPC(ctx, relayReq)

	// Should return gRPC Unavailable error (NOT wrapped response)
	s.Require().Error(err)
	s.Nil(resp)

	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.Unavailable, st.Code())
	s.Contains(st.Message(), "backend service error")
}

func (s *GRPCRelayServiceTestSuite) TestGRPCRelay_Backend4xxError() {
	// Test: Backend 4xx errors ARE wrapped and signed (valid relay, client error)

	// Create backend that returns 400 Bad Request
	backend4xx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer backend4xx.Close()

	s.grpcService.serviceConfigs[s.serviceID] = ServiceConfig{
		Backends: map[string]BackendConfig{
			"rest": {URL: backend4xx.URL},
		},
	}

	ctx := context.Background()
	relayReq := s.createTestRelayRequest()

	// Send relay
	resp, err := s.sendRelayViaGRPC(ctx, relayReq)

	// Should succeed (4xx is valid relay, just client error)
	s.Require().NoError(err)
	s.NotNil(resp)

	// Verify response is signed
	s.NotEmpty(resp.Meta.SupplierOperatorSignature)

	// Verify 400 status code in payload
	poktResp, err := sdktypes.DeserializeHTTPResponse(resp.Payload)
	s.Require().NoError(err)
	s.Equal(400, int(poktResp.StatusCode))
}

func (s *GRPCRelayServiceTestSuite) TestGRPCInterceptor_MetadataForwarding() {
	// Test: HTTP headers in POKTHTTPRequest are forwarded correctly to backend

	// Track headers received by backend
	var receivedHeaders http.Header
	var mu sync.Mutex
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedHeaders = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer backend.Close()

	s.grpcService.serviceConfigs[s.serviceID] = ServiceConfig{
		Backends: map[string]BackendConfig{
			"rest": {
				URL: backend.URL,
				Headers: map[string]string{
					"X-Backend-Auth": "secret-token",
				},
			},
		},
	}

	ctx := context.Background()
	relayReq := s.createTestRelayRequest()

	// Add custom header to POKTHTTPRequest
	relayReq.Payload, _ = proto.Marshal(&sdktypes.POKTHTTPRequest{
		Method: "POST",
		Url:    "/",
		Header: map[string]*sdktypes.Header{
			"Content-Type": {
				Key:    "Content-Type",
				Values: []string{"application/json"},
			},
			"X-Custom-Header": {
				Key:    "X-Custom-Header",
				Values: []string{"custom-value"},
			},
		},
		BodyBz: []byte(`{"jsonrpc":"2.0","id":1}`),
	})

	// Send relay
	resp, err := s.sendRelayViaGRPC(ctx, relayReq)
	s.Require().NoError(err)
	s.NotNil(resp)

	// Verify headers were forwarded
	mu.Lock()
	defer mu.Unlock()
	s.NotNil(receivedHeaders)
	s.Equal("application/json", receivedHeaders.Get("Content-Type"))
	s.Equal("custom-value", receivedHeaders.Get("X-Custom-Header"))
	s.Equal("secret-token", receivedHeaders.Get("X-Backend-Auth")) // From config
}

func (s *GRPCRelayServiceTestSuite) TestGRPCInterceptor_ErrorMapping() {
	// Test: HTTP status codes from backend are mapped correctly to gRPC responses

	testCases := []struct {
		backendStatus int
		shouldWrap    bool
		expectedCode  codes.Code
	}{
		{200, true, codes.OK},           // Success - wrapped
		{400, true, codes.OK},           // Client error - wrapped
		{404, true, codes.OK},           // Not found - wrapped
		{500, false, codes.Unavailable}, // Server error - NOT wrapped
		{502, false, codes.Unavailable}, // Bad gateway - NOT wrapped
		{503, false, codes.Unavailable}, // Service unavailable - NOT wrapped
	}

	for _, tc := range testCases {
		s.Run(fmt.Sprintf("BackendStatus_%d", tc.backendStatus), func() {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.backendStatus)
				_, _ = w.Write([]byte(fmt.Sprintf(`{"status":%d}`, tc.backendStatus)))
			}))
			defer backend.Close()

			s.grpcService.serviceConfigs[s.serviceID] = ServiceConfig{
				Backends: map[string]BackendConfig{
					"rest": {URL: backend.URL},
				},
			}

			ctx := context.Background()
			relayReq := s.createTestRelayRequest()

			resp, err := s.sendRelayViaGRPC(ctx, relayReq)

			if tc.shouldWrap {
				// Should succeed with wrapped response
				s.Require().NoError(err)
				s.NotNil(resp)

				poktResp, err := sdktypes.DeserializeHTTPResponse(resp.Payload)
				s.Require().NoError(err)
				s.Equal(tc.backendStatus, int(poktResp.StatusCode))
			} else {
				// Should return gRPC error
				s.Require().Error(err)
				st, ok := status.FromError(err)
				s.Require().True(ok)
				s.Equal(tc.expectedCode, st.Code())
			}
		})
	}
}

func (s *GRPCRelayServiceTestSuite) TestGRPCConcurrent_MultipleRequests() {
	// Test: Many concurrent unary requests are handled independently

	concurrency := 50
	successCount := &atomic.Int64{}
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(requestNum int) {
			defer wg.Done()

			ctx := context.Background()
			relayReq := s.createTestRelayRequest()

			// Make each request unique via session ID
			relayReq.Meta.SessionHeader.SessionId = fmt.Sprintf("session-%d", requestNum)

			resp, err := s.sendRelayViaGRPC(ctx, relayReq)
			if err == nil && resp != nil {
				successCount.Add(1)
			}
		}(i)
	}

	wg.Wait()

	// All requests should succeed
	s.Equal(int64(concurrency), successCount.Load())
}

func (s *GRPCRelayServiceTestSuite) TestGRPCWeb_ContentTypeDetection() {
	// Test: gRPC content type in POKTHTTPRequest triggers backend selection

	// The relay service determines RPC type from POKTHTTPRequest Content-Type header
	ctx := context.Background()
	relayReq := s.createTestRelayRequest()

	// Set gRPC content type in POKTHTTPRequest
	poktReq := &sdktypes.POKTHTTPRequest{
		Method: "POST",
		Url:    "/",
		Header: map[string]*sdktypes.Header{
			"Content-Type": {
				Key:    "Content-Type",
				Values: []string{"application/grpc"},
			},
		},
		BodyBz: []byte(`{"method":"test"}`),
	}
	payloadBz, _ := proto.Marshal(poktReq)
	relayReq.Payload = payloadBz

	// Send relay
	resp, err := s.sendRelayViaGRPC(ctx, relayReq)

	// Should succeed (backend selection uses "grpc" RPC type)
	s.Require().NoError(err)
	s.NotNil(resp)

	// Verify response is signed
	s.NotEmpty(resp.Meta.SupplierOperatorSignature)
}

func (s *GRPCRelayServiceTestSuite) TestGRPCRelay_UnknownMethod() {
	// Test: Unknown gRPC method returns Unimplemented error

	ctx := context.Background()

	// Try to call a method that doesn't exist
	stream, err := s.grpcClientConn.NewStream(ctx, &grpc.StreamDesc{
		StreamName:    "UnknownMethod",
		ServerStreams: false,
		ClientStreams: false,
	}, "/pocket.service.RelayService/UnknownMethod")
	s.Require().NoError(err)

	relayReq := s.createTestRelayRequest()
	err = stream.SendMsg(relayReq)
	s.Require().NoError(err)

	err = stream.CloseSend()
	s.Require().NoError(err)

	resp := &servicetypes.RelayResponse{}
	err = stream.RecvMsg(resp)

	// Should return Unimplemented error
	s.Require().Error(err)
	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.Unimplemented, st.Code())
	s.Contains(st.Message(), "unknown method")
}

func (s *GRPCRelayServiceTestSuite) TestGRPCRelay_ContextCancellation() {
	// Test: Context cancellation is handled properly

	// Create slow backend
	slowBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slowBackend.Close()

	s.grpcService.serviceConfigs[s.serviceID] = ServiceConfig{
		Backends: map[string]BackendConfig{
			"rest": {URL: slowBackend.URL},
		},
	}

	// Create context that will be cancelled
	ctx, cancel := context.WithCancel(context.Background())

	// Send relay in goroutine
	resultCh := make(chan error, 1)
	go func() {
		relayReq := s.createTestRelayRequest()
		_, err := s.sendRelayViaGRPC(ctx, relayReq)
		resultCh <- err
	}()

	// Cancel context after short delay
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Wait for result
	err := <-resultCh

	// Should return error (context cancelled or backend error)
	s.Require().Error(err)
}

func (s *GRPCRelayServiceTestSuite) TestGRPCRelay_GzipCompression() {
	// Test: gRPC gzip compression metadata is handled (registered in relay_grpc_service.go:18)

	// Create backend that returns large response
	largeResponse := bytes.Repeat([]byte("x"), 10000)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(largeResponse)
	}))
	defer backend.Close()

	s.grpcService.serviceConfigs[s.serviceID] = ServiceConfig{
		Backends: map[string]BackendConfig{
			"rest": {URL: backend.URL},
		},
	}

	// Create context with gzip compression metadata
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"grpc-encoding", "gzip",
	))

	relayReq := s.createTestRelayRequest()

	// Send relay with compression
	resp, err := s.sendRelayViaGRPC(ctx, relayReq)

	// Should succeed
	s.Require().NoError(err)
	s.NotNil(resp)

	// Verify response contains large data
	poktResp, err := sdktypes.DeserializeHTTPResponse(resp.Payload)
	s.Require().NoError(err)
	s.Greater(len(poktResp.BodyBz), 5000)
}

// Mock types for gRPC relay service tests

type grpcMockPublisher struct {
	mu        sync.Mutex
	published []*transport.MinedRelayMessage
	err       error
}

func (m *grpcMockPublisher) Publish(ctx context.Context, msg *transport.MinedRelayMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = append(m.published, msg)
	return m.err
}

func (m *grpcMockPublisher) PublishBatch(ctx context.Context, msgs []*transport.MinedRelayMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = append(m.published, msgs...)
	return m.err
}

func (m *grpcMockPublisher) Close() error {
	return nil
}

type grpcMockSigner struct {
	err error
}

func (m *grpcMockSigner) Sign(msg [32]byte) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	// Return deterministic signature for gRPC tests
	return []byte("grpc-test-signature-32-chars-ok"), nil
}
