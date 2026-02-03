//go:build test

package relayer

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/pokt-network/pocket-relay-miner/testutil"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
)

// ValidatorInterfaceTestSuite characterizes the RelayValidator interface behavior.
type ValidatorInterfaceTestSuite struct {
	suite.Suite
	supplierAddr string
	appAddr      string
	serviceID    string
}

func TestValidatorInterfaceTestSuite(t *testing.T) {
	suite.Run(t, new(ValidatorInterfaceTestSuite))
}

func (s *ValidatorInterfaceTestSuite) SetupTest() {
	s.supplierAddr = testutil.TestSupplierAddress()
	s.appAddr = testutil.TestAppAddress()
	s.serviceID = testutil.TestServiceID
}

func (s *ValidatorInterfaceTestSuite) TestValidator_BlockHeightGetSet() {
	var validator RelayValidator
	validator = &mockValidatorForBlockHeight{}
	validator.SetCurrentBlockHeight(150)
	height := validator.GetCurrentBlockHeight()
	s.Equal(int64(150), height)
}

func (s *ValidatorInterfaceTestSuite) TestValidator_BlockHeightConcurrency() {
	validator := &mockValidatorForBlockHeight{}
	done := make(chan bool, 200)
	for i := 0; i < 100; i++ {
		go func(h int64) {
			validator.SetCurrentBlockHeight(h)
			done <- true
		}(int64(i))
	}
	for i := 0; i < 100; i++ {
		go func() {
			_ = validator.GetCurrentBlockHeight()
			done <- true
		}()
	}
	for i := 0; i < 200; i++ {
		<-done
	}
	final := validator.GetCurrentBlockHeight()
	s.True(final >= 0 && final < 100)
}

func (s *ValidatorInterfaceTestSuite) TestValidator_BasicValidation() {
	validator := &mockValidatorForValidation{}
	relayReq := &servicetypes.RelayRequest{
		Meta: servicetypes.RelayRequestMetadata{
			SessionHeader: &sessiontypes.SessionHeader{
				ApplicationAddress:      s.appAddr,
				ServiceId:               "",
				SessionId:               "session-123",
				SessionStartBlockHeight: 90,
				SessionEndBlockHeight:   100,
			},
			SupplierOperatorAddress: s.supplierAddr,
			Signature:               []byte("sig"),
		},
		Payload: []byte("payload"),
	}
	err := validator.ValidateRelayRequest(context.Background(), relayReq)
	s.Error(err)
}

func (s *ValidatorInterfaceTestSuite) TestValidator_RewardEligibility() {
	validator := &mockValidatorForValidation{}
	relayReq := &servicetypes.RelayRequest{
		Meta: servicetypes.RelayRequestMetadata{
			SessionHeader: &sessiontypes.SessionHeader{
				ApplicationAddress:      s.appAddr,
				ServiceId:               s.serviceID,
				SessionId:               "session-123",
				SessionStartBlockHeight: 90,
				SessionEndBlockHeight:   100,
			},
			SupplierOperatorAddress: s.supplierAddr,
			Signature:               []byte("sig"),
		},
		Payload: []byte("payload"),
	}
	err := validator.CheckRewardEligibility(context.Background(), relayReq)
	s.Error(err)
}

func (s *ValidatorInterfaceTestSuite) TestValidator_ValidRequest() {
	validator := &mockValidatorForValidation{}
	relayReq := &servicetypes.RelayRequest{
		Meta: servicetypes.RelayRequestMetadata{
			SessionHeader: &sessiontypes.SessionHeader{
				ApplicationAddress:      s.appAddr,
				ServiceId:               s.serviceID,
				SessionId:               "session-123",
				SessionStartBlockHeight: 90,
				SessionEndBlockHeight:   100,
			},
			SupplierOperatorAddress: s.supplierAddr,
			Signature:               []byte("sig"),
		},
		Payload: []byte("payload"),
	}
	err := validator.ValidateRelayRequest(context.Background(), relayReq)
	s.NoError(err)
}

func (s *ValidatorInterfaceTestSuite) TestConfig_AllowedSuppliers() {
	config := &ValidatorConfig{
		AllowedSupplierAddresses: []string{
			testutil.TestSupplierAddress(),
			testutil.TestSupplier2Address(),
		},
	}
	s.Equal(2, len(config.AllowedSupplierAddresses))
	s.Contains(config.AllowedSupplierAddresses, testutil.TestSupplierAddress())
	s.Contains(config.AllowedSupplierAddresses, testutil.TestSupplier2Address())
}

func (s *ValidatorInterfaceTestSuite) TestConfig_EmptyAllowedSuppliers() {
	config := &ValidatorConfig{
		AllowedSupplierAddresses: []string{},
	}
	s.Equal(0, len(config.AllowedSupplierAddresses))
}

// Mock implementations
type mockValidatorForBlockHeight struct {
	mu            sync.RWMutex
	currentHeight int64
}

func (m *mockValidatorForBlockHeight) ValidateRelayRequest(ctx context.Context, relayRequest *servicetypes.RelayRequest) error {
	return nil
}

func (m *mockValidatorForBlockHeight) CheckRewardEligibility(ctx context.Context, relayRequest *servicetypes.RelayRequest) error {
	return nil
}

func (m *mockValidatorForBlockHeight) GetCurrentBlockHeight() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentHeight
}

func (m *mockValidatorForBlockHeight) SetCurrentBlockHeight(height int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentHeight = height
}

type mockValidatorForValidation struct {
	mu            sync.RWMutex
	currentHeight int64
}

func (m *mockValidatorForValidation) ValidateRelayRequest(ctx context.Context, relayRequest *servicetypes.RelayRequest) error {
	if relayRequest.Meta.SessionHeader == nil {
		return context.Canceled
	}
	if relayRequest.Meta.SessionHeader.ServiceId == "" {
		return context.Canceled
	}
	return nil
}

func (m *mockValidatorForValidation) CheckRewardEligibility(ctx context.Context, relayRequest *servicetypes.RelayRequest) error {
	return context.DeadlineExceeded
}

func (m *mockValidatorForValidation) GetCurrentBlockHeight() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentHeight
}

func (m *mockValidatorForValidation) SetCurrentBlockHeight(height int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentHeight = height
}
