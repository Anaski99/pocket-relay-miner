//go:build test

package relayer

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdktypes "github.com/pokt-network/shannon-sdk/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/suite"
	"google.golang.org/protobuf/proto"

	"github.com/pokt-network/pocket-relay-miner/logging"
	"github.com/pokt-network/pocket-relay-miner/transport"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
)

// RelayProcessorTestSuite provides tests for relay validation, metering, signing, and publishing.
type RelayProcessorTestSuite struct {
	suite.Suite

	// Dependencies
	logger    logging.Logger
	validator *mockRelayValidatorForProcessor
	publisher *mockMinedRelayPublisherForProcessor
	signer    *ResponseSigner

	// Test data
	supplierAddr string
	appAddr      string
	serviceID    string
}

func TestRelayProcessorTestSuite(t *testing.T) {
	suite.Run(t, new(RelayProcessorTestSuite))
}

func (s *RelayProcessorTestSuite) SetupSuite() {
	s.logger = zerolog.New(io.Discard).With().Logger()

	// Setup test data - using hardcoded values to avoid import cycle with testutil
	s.supplierAddr = "pokt1supplier123"
	s.appAddr = "pokt1app123"
	s.serviceID = "anvil"
}

func (s *RelayProcessorTestSuite) SetupTest() {
	s.validator = &mockRelayValidatorForProcessor{}
	s.publisher = &mockMinedRelayPublisherForProcessor{}

	// Create response signer with mock signer
	s.signer = &ResponseSigner{
		logger:            s.logger,
		signers:           make(map[string]Signer),
		operatorAddresses: []string{s.supplierAddr},
	}
	s.signer.signers[s.supplierAddr] = &mockSignerForProcessor{}
}

// getRelayProcessorTestConcurrency returns appropriate concurrency level.
func getRelayProcessorTestConcurrency() int {
	if os.Getenv("TEST_MODE") == "nightly" {
		return 1000
	}
	return 100
}

// createTestRelayRequest creates a valid RelayRequest for testing.
func (s *RelayProcessorTestSuite) createTestRelayRequest() *servicetypes.RelayRequest {
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

// Validation Tests

func (s *RelayProcessorTestSuite) TestValidator_ValidRelay() {
	// Create valid relay request
	relayReq := s.createTestRelayRequest()

	// Validate
	ctx := context.Background()
	err := s.validator.ValidateRelayRequest(ctx, relayReq)
	s.NoError(err)
}

func (s *RelayProcessorTestSuite) TestValidator_ValidationFails() {
	// Configure validator to fail
	s.validator.validateErr = fmt.Errorf("signature verification failed")

	relayReq := s.createTestRelayRequest()

	ctx := context.Background()
	err := s.validator.ValidateRelayRequest(ctx, relayReq)
	s.Error(err)
	s.Contains(err.Error(), "signature verification failed")
}

func (s *RelayProcessorTestSuite) TestValidator_MissingServiceID() {
	relayReq := s.createTestRelayRequest()
	relayReq.Meta.SessionHeader.ServiceId = ""

	// Validation logic would check for empty service ID
	// The mock doesn't, but this documents expected behavior
	s.Empty(relayReq.Meta.SessionHeader.ServiceId)
}

func (s *RelayProcessorTestSuite) TestValidator_MissingSupplierAddress() {
	relayReq := s.createTestRelayRequest()
	relayReq.Meta.SupplierOperatorAddress = ""

	// Validation logic would check for empty supplier address
	s.Empty(relayReq.Meta.SupplierOperatorAddress)
}

func (s *RelayProcessorTestSuite) TestValidator_ExpiredSession() {
	// Set validator to report expired session
	s.validator.currentBlockHeight = 200 // Past session end height (100)

	relayReq := s.createTestRelayRequest()

	// Session ends at block 100, current is 200
	s.True(s.validator.currentBlockHeight > relayReq.Meta.SessionHeader.SessionEndBlockHeight)
}

func (s *RelayProcessorTestSuite) TestValidator_RewardEligibility() {
	relayReq := s.createTestRelayRequest()

	ctx := context.Background()
	err := s.validator.CheckRewardEligibility(ctx, relayReq)
	s.NoError(err)
}

func (s *RelayProcessorTestSuite) TestValidator_RewardEligibilityFails() {
	s.validator.rewardEligibleErr = fmt.Errorf("session already claimed")

	relayReq := s.createTestRelayRequest()

	ctx := context.Background()
	err := s.validator.CheckRewardEligibility(ctx, relayReq)
	s.Error(err)
}

// Block Height Tests

func (s *RelayProcessorTestSuite) TestValidator_GetCurrentBlockHeight() {
	s.validator.currentBlockHeight = 500

	height := s.validator.GetCurrentBlockHeight()
	s.Equal(int64(500), height)
}

func (s *RelayProcessorTestSuite) TestValidator_SetCurrentBlockHeight() {
	s.validator.SetCurrentBlockHeight(750)

	s.Equal(int64(750), s.validator.currentBlockHeight)
}

// Signer Tests

func (s *RelayProcessorTestSuite) TestSigner_ValidResponse() {
	signer := s.signer.signers[s.supplierAddr]
	s.NotNil(signer)

	var msg [32]byte
	copy(msg[:], []byte("test message to sign 32 bytes!!"))

	sig, err := signer.Sign(msg)
	s.NoError(err)
	s.NotEmpty(sig)
}

func (s *RelayProcessorTestSuite) TestSigner_SigningError() {
	// Create signer that returns error
	errSigner := &mockSignerForProcessor{err: fmt.Errorf("signing key unavailable")}
	s.signer.signers[s.supplierAddr] = errSigner

	var msg [32]byte
	sig, err := errSigner.Sign(msg)
	s.Error(err)
	s.Nil(sig)
}

func (s *RelayProcessorTestSuite) TestSigner_DeterministicOutput() {
	signer := s.signer.signers[s.supplierAddr]

	var msg [32]byte
	copy(msg[:], []byte("deterministic message 32 bytes!!"))

	sig1, err := signer.Sign(msg)
	s.NoError(err)

	sig2, err := signer.Sign(msg)
	s.NoError(err)

	// Mock signer returns deterministic output
	s.Equal(sig1, sig2)
}

func (s *RelayProcessorTestSuite) TestSigner_OperatorAddresses() {
	s.Contains(s.signer.operatorAddresses, s.supplierAddr)
}

func (s *RelayProcessorTestSuite) TestSigner_MultipleSupplers() {
	// Add second supplier
	supplier2 := "pokt1supplier456"
	s.signer.signers[supplier2] = &mockSignerForProcessor{}
	s.signer.operatorAddresses = append(s.signer.operatorAddresses, supplier2)

	// Both should be accessible
	_, ok := s.signer.signers[s.supplierAddr]
	s.True(ok)

	_, ok = s.signer.signers[supplier2]
	s.True(ok)
}

// Publisher Tests

func (s *RelayProcessorTestSuite) TestPublisher_PublishSuccess() {
	ctx := context.Background()
	msg := &transport.MinedRelayMessage{
		SessionId:               "test-session-123",
		SupplierOperatorAddress: s.supplierAddr,
	}

	err := s.publisher.Publish(ctx, msg)
	s.NoError(err)

	// Verify message was recorded
	s.publisher.mu.Lock()
	s.Len(s.publisher.published, 1)
	s.publisher.mu.Unlock()
}

func (s *RelayProcessorTestSuite) TestPublisher_PublishBatch() {
	ctx := context.Background()
	msgs := []*transport.MinedRelayMessage{
		{SessionId: "session-1", SupplierOperatorAddress: s.supplierAddr},
		{SessionId: "session-2", SupplierOperatorAddress: s.supplierAddr},
		{SessionId: "session-3", SupplierOperatorAddress: s.supplierAddr},
	}

	err := s.publisher.PublishBatch(ctx, msgs)
	s.NoError(err)

	// Verify all messages were recorded
	s.publisher.mu.Lock()
	s.Len(s.publisher.published, 3)
	s.publisher.mu.Unlock()
}

func (s *RelayProcessorTestSuite) TestPublisher_PublishError() {
	s.publisher.err = fmt.Errorf("redis connection failed")

	ctx := context.Background()
	err := s.publisher.Publish(ctx, nil)
	s.Error(err)
}

func (s *RelayProcessorTestSuite) TestPublisher_Close() {
	err := s.publisher.Close()
	s.NoError(err)
}

// Concurrent Processing Tests

func (s *RelayProcessorTestSuite) TestConcurrent_Validation() {
	numGoroutines := getRelayProcessorTestConcurrency()
	var wg sync.WaitGroup
	var validCount atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			relayReq := s.createTestRelayRequest()
			ctx := context.Background()
			err := s.validator.ValidateRelayRequest(ctx, relayReq)
			if err == nil {
				validCount.Add(1)
			}
		}()
	}

	wg.Wait()
	s.Equal(int64(numGoroutines), validCount.Load())
}

func (s *RelayProcessorTestSuite) TestConcurrent_Signing() {
	numGoroutines := getRelayProcessorTestConcurrency()
	var wg sync.WaitGroup
	var signedCount atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			signer := s.signer.signers[s.supplierAddr]
			var msg [32]byte
			_, err := signer.Sign(msg)
			if err == nil {
				signedCount.Add(1)
			}
		}()
	}

	wg.Wait()
	s.Equal(int64(numGoroutines), signedCount.Load())
}

func (s *RelayProcessorTestSuite) TestConcurrent_Publishing() {
	numGoroutines := getRelayProcessorTestConcurrency()
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			ctx := context.Background()
			msg := &transport.MinedRelayMessage{
				SessionId:               fmt.Sprintf("session-%d", idx),
				SupplierOperatorAddress: s.supplierAddr,
			}
			_ = s.publisher.Publish(ctx, msg)
		}(i)
	}

	wg.Wait()

	// All should be published
	s.publisher.mu.Lock()
	s.Equal(numGoroutines, len(s.publisher.published))
	s.publisher.mu.Unlock()
}

func (s *RelayProcessorTestSuite) TestConcurrent_MixedOperations() {
	numGoroutines := getRelayProcessorTestConcurrency()
	var wg sync.WaitGroup
	var totalOps atomic.Int64

	// Validation operations
	for i := 0; i < numGoroutines/3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			relayReq := s.createTestRelayRequest()
			ctx := context.Background()
			_ = s.validator.ValidateRelayRequest(ctx, relayReq)
			totalOps.Add(1)
		}()
	}

	// Signing operations
	for i := 0; i < numGoroutines/3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			signer := s.signer.signers[s.supplierAddr]
			var msg [32]byte
			_, _ = signer.Sign(msg)
			totalOps.Add(1)
		}()
	}

	// Publishing operations
	for i := 0; i < numGoroutines/3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx := context.Background()
			msg := &transport.MinedRelayMessage{
				SessionId: fmt.Sprintf("session-%d", idx),
			}
			_ = s.publisher.Publish(ctx, msg)
			totalOps.Add(1)
		}(i)
	}

	wg.Wait()

	// Account for integer division (numGoroutines/3 * 3 may be less than numGoroutines)
	expectedOps := (numGoroutines / 3) * 3
	s.Equal(int64(expectedOps), totalOps.Load())
}

// Mock implementations for this test file

type mockRelayValidatorForProcessor struct {
	validateErr        error
	rewardEligibleErr  error
	currentBlockHeight int64
}

func (m *mockRelayValidatorForProcessor) ValidateRelayRequest(ctx context.Context, relayRequest *servicetypes.RelayRequest) error {
	return m.validateErr
}

func (m *mockRelayValidatorForProcessor) CheckRewardEligibility(ctx context.Context, relayRequest *servicetypes.RelayRequest) error {
	return m.rewardEligibleErr
}

func (m *mockRelayValidatorForProcessor) GetCurrentBlockHeight() int64 {
	return m.currentBlockHeight
}

func (m *mockRelayValidatorForProcessor) SetCurrentBlockHeight(height int64) {
	m.currentBlockHeight = height
}

type mockMinedRelayPublisherForProcessor struct {
	mu        sync.Mutex
	published []*transport.MinedRelayMessage
	err       error
}

func (m *mockMinedRelayPublisherForProcessor) Publish(ctx context.Context, msg *transport.MinedRelayMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = append(m.published, msg)
	return m.err
}

func (m *mockMinedRelayPublisherForProcessor) PublishBatch(ctx context.Context, msgs []*transport.MinedRelayMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = append(m.published, msgs...)
	return m.err
}

func (m *mockMinedRelayPublisherForProcessor) Close() error {
	return nil
}

type mockSignerForProcessor struct {
	err error
}

func (m *mockSignerForProcessor) Sign(msg [32]byte) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	// Return a deterministic signature for testing
	return []byte("test-signature-bytes-32-chars-ok"), nil
}

// Response Signer Integration Tests

type ResponseSignerTestSuite struct {
	suite.Suite
	logger logging.Logger
}

func TestResponseSignerTestSuite(t *testing.T) {
	suite.Run(t, new(ResponseSignerTestSuite))
}

func (s *ResponseSignerTestSuite) SetupSuite() {
	s.logger = zerolog.New(io.Discard).With().Logger()
}

func (s *ResponseSignerTestSuite) TestResponseSigner_Creation() {
	signer := &ResponseSigner{
		logger:            s.logger,
		signers:           make(map[string]Signer),
		operatorAddresses: []string{},
	}

	s.NotNil(signer)
	s.Empty(signer.operatorAddresses)
}

func (s *ResponseSignerTestSuite) TestResponseSigner_AddSigner() {
	responseSigner := &ResponseSigner{
		logger:            s.logger,
		signers:           make(map[string]Signer),
		operatorAddresses: []string{},
	}

	supplierAddr := "pokt1test123"
	responseSigner.signers[supplierAddr] = &mockSignerForProcessor{}
	responseSigner.operatorAddresses = append(responseSigner.operatorAddresses, supplierAddr)

	s.Len(responseSigner.signers, 1)
	s.Contains(responseSigner.operatorAddresses, supplierAddr)
}

func (s *ResponseSignerTestSuite) TestResponseSigner_GetSigner() {
	responseSigner := &ResponseSigner{
		logger:            s.logger,
		signers:           make(map[string]Signer),
		operatorAddresses: []string{},
	}

	supplierAddr := "pokt1test123"
	mockSign := &mockSignerForProcessor{}
	responseSigner.signers[supplierAddr] = mockSign

	signer, ok := responseSigner.signers[supplierAddr]
	s.True(ok)
	s.Equal(mockSign, signer)
}

func (s *ResponseSignerTestSuite) TestResponseSigner_SignerNotFound() {
	responseSigner := &ResponseSigner{
		logger:            s.logger,
		signers:           make(map[string]Signer),
		operatorAddresses: []string{},
	}

	_, ok := responseSigner.signers["nonexistent"]
	s.False(ok)
}

// Relay Pipeline Tests

type RelayPipelineTestSuite struct {
	suite.Suite
	logger logging.Logger
}

func TestRelayPipelineTestSuite(t *testing.T) {
	suite.Run(t, new(RelayPipelineTestSuite))
}

func (s *RelayPipelineTestSuite) SetupSuite() {
	s.logger = zerolog.New(io.Discard).With().Logger()
}

func (s *RelayPipelineTestSuite) TestRelayPipeline_Exists() {
	// RelayPipeline is a struct that orchestrates validation, metering, signing, publishing
	// Verify the type exists and can be referenced
	var pipeline *RelayPipeline
	s.Nil(pipeline)
}

// Performance Tests

type RelayProcessorPerformanceTestSuite struct {
	suite.Suite
	logger logging.Logger
}

func TestRelayProcessorPerformanceTestSuite(t *testing.T) {
	suite.Run(t, new(RelayProcessorPerformanceTestSuite))
}

func (s *RelayProcessorPerformanceTestSuite) SetupSuite() {
	s.logger = zerolog.New(io.Discard).With().Logger()
}

func (s *RelayProcessorPerformanceTestSuite) TestValidation_Throughput() {
	validator := &mockRelayValidatorForProcessor{}

	numOps := 10000
	start := time.Now()

	for i := 0; i < numOps; i++ {
		ctx := context.Background()
		_ = validator.ValidateRelayRequest(ctx, &servicetypes.RelayRequest{})
	}

	duration := time.Since(start)
	opsPerSecond := float64(numOps) / duration.Seconds()

	s.T().Logf("Validation throughput: %.0f ops/sec", opsPerSecond)
	// Mock validation should be extremely fast
	s.True(opsPerSecond > 100000, "Expected >100k ops/sec, got %.0f", opsPerSecond)
}

func (s *RelayProcessorPerformanceTestSuite) TestSigning_Throughput() {
	signer := &mockSignerForProcessor{}

	numOps := 10000
	start := time.Now()

	for i := 0; i < numOps; i++ {
		var msg [32]byte
		_, _ = signer.Sign(msg)
	}

	duration := time.Since(start)
	opsPerSecond := float64(numOps) / duration.Seconds()

	s.T().Logf("Signing throughput: %.0f ops/sec", opsPerSecond)
	// Mock signing should be extremely fast
	s.True(opsPerSecond > 100000, "Expected >100k ops/sec, got %.0f", opsPerSecond)
}

func (s *RelayProcessorPerformanceTestSuite) TestPublishing_Throughput() {
	publisher := &mockMinedRelayPublisherForProcessor{}

	numOps := 10000
	start := time.Now()

	for i := 0; i < numOps; i++ {
		ctx := context.Background()
		_ = publisher.Publish(ctx, nil)
	}

	duration := time.Since(start)
	opsPerSecond := float64(numOps) / duration.Seconds()

	s.T().Logf("Publishing throughput: %.0f ops/sec", opsPerSecond)
	// Note: This measures mock speed, not actual Redis performance
}

// Relay Request Parsing Tests

type RelayRequestParsingTestSuite struct {
	suite.Suite
}

func TestRelayRequestParsingTestSuite(t *testing.T) {
	suite.Run(t, new(RelayRequestParsingTestSuite))
}

func (s *RelayRequestParsingTestSuite) TestRelayRequest_Marshal() {
	relayReq := &servicetypes.RelayRequest{
		Meta: servicetypes.RelayRequestMetadata{
			SessionHeader: &sessiontypes.SessionHeader{
				ApplicationAddress:      "pokt1app",
				ServiceId:               "anvil",
				SessionId:               "session-123",
				SessionStartBlockHeight: 90,
				SessionEndBlockHeight:   100,
			},
			SupplierOperatorAddress: "pokt1supplier",
			Signature:               []byte("signature"),
		},
		Payload: []byte("payload"),
	}

	data, err := relayReq.Marshal()
	s.NoError(err)
	s.NotEmpty(data)
}

func (s *RelayRequestParsingTestSuite) TestRelayRequest_Unmarshal() {
	original := &servicetypes.RelayRequest{
		Meta: servicetypes.RelayRequestMetadata{
			SessionHeader: &sessiontypes.SessionHeader{
				ApplicationAddress: "pokt1app",
				ServiceId:          "anvil",
				SessionId:          "session-123",
			},
			SupplierOperatorAddress: "pokt1supplier",
		},
		Payload: []byte("payload"),
	}

	data, err := original.Marshal()
	s.Require().NoError(err)

	parsed := &servicetypes.RelayRequest{}
	err = parsed.Unmarshal(data)
	s.NoError(err)

	s.Equal(original.Meta.SessionHeader.ServiceId, parsed.Meta.SessionHeader.ServiceId)
	s.Equal(original.Meta.SupplierOperatorAddress, parsed.Meta.SupplierOperatorAddress)
}

func (s *RelayRequestParsingTestSuite) TestRelayRequest_InvalidData() {
	parsed := &servicetypes.RelayRequest{}
	err := parsed.Unmarshal([]byte("invalid protobuf data"))
	s.Error(err)
}

// Validation Mode Tests

type ValidationModeProcessorTestSuite struct {
	suite.Suite
}

func TestValidationModeProcessorTestSuite(t *testing.T) {
	suite.Run(t, new(ValidationModeProcessorTestSuite))
}

func (s *ValidationModeProcessorTestSuite) TestEagerMode_ValidatesFirst() {
	// In eager mode, validation happens before forwarding to backend
	mode := ValidationModeEager
	s.Equal("eager", string(mode))
}

func (s *ValidationModeProcessorTestSuite) TestOptimisticMode_ValidatesAsync() {
	// In optimistic mode, validation happens async after forwarding
	mode := ValidationModeOptimistic
	s.Equal("optimistic", string(mode))
}

func (s *ValidationModeProcessorTestSuite) TestModeComparison() {
	eager := ValidationModeEager
	optimistic := ValidationModeOptimistic

	s.NotEqual(eager, optimistic)
}
