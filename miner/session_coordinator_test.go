//go:build test

package miner

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/suite"

	"github.com/pokt-network/pocket-relay-miner/testutil"
)

// SessionCoordinatorTestSuite tests the SessionCoordinator implementation.
// This characterizes how sessions are discovered from relay traffic and how the coordinator
// orchestrates session lifecycle with the session store.
type SessionCoordinatorTestSuite struct {
	testutil.RedisTestSuite
	coordinator  *SessionCoordinator
	sessionStore *RedisSessionStore
}

// SetupTest creates a fresh coordinator and store for each test.
func (s *SessionCoordinatorTestSuite) SetupTest() {
	// Call parent to flush Redis
	s.RedisTestSuite.SetupTest()

	// Create session store
	storeConfig := SessionStoreConfig{
		KeyPrefix:       "ha:miner:sessions",
		SupplierAddress: testutil.TestSupplierAddress(),
	}
	logger := zerolog.Nop()
	s.sessionStore = NewRedisSessionStore(logger, s.RedisClient, storeConfig)

	// Create coordinator
	coordinatorConfig := SMSTRecoveryConfig{
		SupplierAddress: testutil.TestSupplierAddress(),
	}
	s.coordinator = NewSessionCoordinator(logger, s.sessionStore, coordinatorConfig)
}

// TearDownTest cleans up resources.
func (s *SessionCoordinatorTestSuite) TearDownTest() {
	if s.coordinator != nil {
		_ = s.coordinator.Close()
	}
	if s.sessionStore != nil {
		_ = s.sessionStore.Close()
	}
}

// TestSessionCoordinator_DiscoverNewSession verifies that the first relay for a session triggers creation.
func (s *SessionCoordinatorTestSuite) TestSessionCoordinator_DiscoverNewSession() {
	ctx := context.Background()
	sessionID := "new_session_001"

	// Verify session doesn't exist
	snapshot, err := s.sessionStore.Get(ctx, sessionID)
	s.Require().NoError(err)
	s.Require().Nil(snapshot, "session should not exist before first relay")

	// Process first relay (should create session)
	err = s.coordinator.OnRelayProcessed(
		ctx,
		sessionID,
		100, // compute units
		testutil.TestSupplierAddress(),
		testutil.TestServiceID,
		testutil.TestAppAddress(),
		100, // start height
		104, // end height
	)
	s.Require().NoError(err)

	// Verify session was created
	snapshot, err = s.sessionStore.Get(ctx, sessionID)
	s.Require().NoError(err)
	s.Require().NotNil(snapshot, "session should be created after first relay")
	s.Require().Equal(sessionID, snapshot.SessionID)
	s.Require().Equal(SessionStateActive, snapshot.State)
	s.Require().Equal(testutil.TestServiceID, snapshot.ServiceID)
	s.Require().Equal(int64(100), snapshot.SessionStartHeight)
	s.Require().Equal(int64(104), snapshot.SessionEndHeight)
}

// TestSessionCoordinator_RouteRelayToExisting verifies subsequent relays route to existing session.
func (s *SessionCoordinatorTestSuite) TestSessionCoordinator_RouteRelayToExisting() {
	ctx := context.Background()
	sessionID := "existing_session_002"

	// Create session via first relay
	err := s.coordinator.OnRelayProcessed(
		ctx, sessionID, 100,
		testutil.TestSupplierAddress(),
		testutil.TestServiceID,
		testutil.TestAppAddress(),
		100, 104,
	)
	s.Require().NoError(err)

	// Get initial relay count
	snapshot, err := s.sessionStore.Get(ctx, sessionID)
	s.Require().NoError(err)
	initialCount := snapshot.RelayCount

	// Process second relay (should increment count)
	err = s.coordinator.OnRelayProcessed(
		ctx, sessionID, 100,
		testutil.TestSupplierAddress(),
		testutil.TestServiceID,
		testutil.TestAppAddress(),
		100, 104,
	)
	s.Require().NoError(err)

	// Verify relay count incremented
	snapshot, err = s.sessionStore.Get(ctx, sessionID)
	s.Require().NoError(err)
	s.Require().Equal(initialCount+1, snapshot.RelayCount, "relay count should increment")
}

// TestSessionCoordinator_MultipleSuppliers verifies sessions are isolated per supplier.
func (s *SessionCoordinatorTestSuite) TestSessionCoordinator_MultipleSuppliers() {
	ctx := context.Background()
	sessionID := "multi_supplier_003"

	// Coordinator 1 with Supplier 1
	config1 := SMSTRecoveryConfig{
		SupplierAddress: testutil.TestSupplierAddress(),
	}
	store1 := NewRedisSessionStore(zerolog.Nop(), s.RedisClient, SessionStoreConfig{
		KeyPrefix:       "ha:miner:sessions",
		SupplierAddress: testutil.TestSupplierAddress(),
	})
	coordinator1 := NewSessionCoordinator(zerolog.Nop(), store1, config1)
	defer coordinator1.Close()
	defer store1.Close()

	// Coordinator 2 with Supplier 2
	config2 := SMSTRecoveryConfig{
		SupplierAddress: testutil.TestSupplier2Address(),
	}
	store2 := NewRedisSessionStore(zerolog.Nop(), s.RedisClient, SessionStoreConfig{
		KeyPrefix:       "ha:miner:sessions",
		SupplierAddress: testutil.TestSupplier2Address(),
	})
	coordinator2 := NewSessionCoordinator(zerolog.Nop(), store2, config2)
	defer coordinator2.Close()
	defer store2.Close()

	// Process relay for supplier 1
	err := coordinator1.OnRelayProcessed(
		ctx, sessionID, 100,
		testutil.TestSupplierAddress(),
		testutil.TestServiceID,
		testutil.TestAppAddress(),
		100, 104,
	)
	s.Require().NoError(err)

	// Process relay for supplier 2 (same session ID but different supplier)
	err = coordinator2.OnRelayProcessed(
		ctx, sessionID, 100,
		testutil.TestSupplier2Address(),
		testutil.TestServiceID,
		testutil.TestAppAddress(),
		100, 104,
	)
	s.Require().NoError(err)

	// Verify both sessions exist independently
	snapshot1, err := store1.Get(ctx, sessionID)
	s.Require().NoError(err)
	s.Require().NotNil(snapshot1)
	s.Require().Equal(testutil.TestSupplierAddress(), snapshot1.SupplierOperatorAddress)

	snapshot2, err := store2.Get(ctx, sessionID)
	s.Require().NoError(err)
	s.Require().NotNil(snapshot2)
	s.Require().Equal(testutil.TestSupplier2Address(), snapshot2.SupplierOperatorAddress)
}

// TestSessionCoordinator_InvalidSession verifies handling of relay without metadata.
func (s *SessionCoordinatorTestSuite) TestSessionCoordinator_InvalidSession() {
	ctx := context.Background()
	sessionID := "invalid_session_004"

	// Process relay with missing metadata (empty supplier address)
	// This should log a warning but not crash
	err := s.coordinator.OnRelayProcessed(
		ctx, sessionID, 100,
		"", // empty supplier address
		"", // empty service ID
		testutil.TestAppAddress(),
		100, 104,
	)
	s.Require().NoError(err, "should not error but log warning")

	// Verify session was not created (metadata missing)
	snapshot, err := s.sessionStore.Get(ctx, sessionID)
	s.Require().NoError(err)
	s.Require().Nil(snapshot, "session should not be created without metadata")
}

// TestSessionCoordinator_SessionExpired verifies behavior with expired session.
func (s *SessionCoordinatorTestSuite) TestSessionCoordinator_SessionExpired() {
	ctx := context.Background()
	sessionID := "expired_session_005"

	// Create session in terminal state (proved = expired)
	snapshot := &SessionSnapshot{
		SessionID:               sessionID,
		SupplierOperatorAddress: testutil.TestSupplierAddress(),
		ServiceID:               testutil.TestServiceID,
		ApplicationAddress:      testutil.TestAppAddress(),
		SessionStartHeight:      100,
		SessionEndHeight:        104,
		State:                   SessionStateProved, // Terminal state
		RelayCount:              10,
		TotalComputeUnits:       1000,
	}
	err := s.sessionStore.Save(ctx, snapshot)
	s.Require().NoError(err)

	// Try to process relay for expired session
	// This should fail because IncrementRelayCount rejects terminal states
	err = s.coordinator.OnRelayProcessed(
		ctx, sessionID, 100,
		testutil.TestSupplierAddress(),
		testutil.TestServiceID,
		testutil.TestAppAddress(),
		100, 104,
	)
	// Characterization: OnRelayProcessed logs warning but does not return error
	s.Require().NoError(err, "OnRelayProcessed logs warning for terminal state but does not error")

	// Verify relay count did not change
	snapshot, err = s.sessionStore.Get(ctx, sessionID)
	s.Require().NoError(err)
	s.Require().Equal(int64(10), snapshot.RelayCount, "relay count should not change for terminal session")
}

// TestSessionCoordinator_ConcurrentDiscovery verifies concurrent relays create exactly one session.
func (s *SessionCoordinatorTestSuite) TestSessionCoordinator_ConcurrentDiscovery() {
	ctx := context.Background()
	sessionID := "concurrent_discovery_006"

	var wg sync.WaitGroup
	goroutines := 10
	wg.Add(goroutines)

	// Multiple goroutines try to process first relay for same session concurrently
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_ = s.coordinator.OnRelayProcessed(
				ctx, sessionID, 100,
				testutil.TestSupplierAddress(),
				testutil.TestServiceID,
				testutil.TestAppAddress(),
				100, 104,
			)
		}()
	}

	wg.Wait()

	// Verify exactly one session exists
	snapshot, err := s.sessionStore.Get(ctx, sessionID)
	s.Require().NoError(err)
	s.Require().NotNil(snapshot, "session should exist after concurrent discovery")
	s.Require().Equal(sessionID, snapshot.SessionID)

	// Verify all relays were counted (may be less than 10 due to race, but at least 1)
	s.Require().Greater(snapshot.RelayCount, int64(0), "relay count should be > 0")
	s.Require().LessOrEqual(snapshot.RelayCount, int64(goroutines), "relay count should be <= goroutines")
}

// TestSessionCoordinator_RelayAccumulation verifies multiple relays increase relay count correctly.
func (s *SessionCoordinatorTestSuite) TestSessionCoordinator_RelayAccumulation() {
	ctx := context.Background()
	sessionID := "accumulation_007"

	// Process 10 relays sequentially
	for i := 0; i < 10; i++ {
		err := s.coordinator.OnRelayProcessed(
			ctx, sessionID, 100,
			testutil.TestSupplierAddress(),
			testutil.TestServiceID,
			testutil.TestAppAddress(),
			100, 104,
		)
		s.Require().NoError(err)
	}

	// Verify relay count and compute units
	snapshot, err := s.sessionStore.Get(ctx, sessionID)
	s.Require().NoError(err)
	s.Require().Equal(int64(10), snapshot.RelayCount)
	s.Require().Equal(uint64(1000), snapshot.TotalComputeUnits)
}

// TestSessionCoordinator_OnSessionClaimed verifies claim state transition.
func (s *SessionCoordinatorTestSuite) TestSessionCoordinator_OnSessionClaimed() {
	ctx := context.Background()
	sessionID := "claimed_008"

	// Create session
	err := s.coordinator.OnSessionCreated(
		ctx, sessionID,
		testutil.TestSupplierAddress(),
		testutil.TestServiceID,
		testutil.TestAppAddress(),
		100, 104,
	)
	s.Require().NoError(err)

	// Mark as claimed
	claimRootHash := []byte("claim_root_hash_123")
	claimTxHash := "tx_hash_abc"
	err = s.coordinator.OnSessionClaimed(ctx, sessionID, claimRootHash, claimTxHash)
	s.Require().NoError(err)

	// Verify state
	snapshot, err := s.sessionStore.Get(ctx, sessionID)
	s.Require().NoError(err)
	s.Require().Equal(SessionStateClaimed, snapshot.State)
	s.Require().Equal(claimRootHash, snapshot.ClaimedRootHash)
	s.Require().Equal(claimTxHash, snapshot.ClaimTxHash)
}

// TestSessionCoordinator_OnProofSubmitted verifies proof TX hash is stored.
func (s *SessionCoordinatorTestSuite) TestSessionCoordinator_OnProofSubmitted() {
	ctx := context.Background()
	sessionID := "proof_submitted_009"

	// Create session
	err := s.coordinator.OnSessionCreated(
		ctx, sessionID,
		testutil.TestSupplierAddress(),
		testutil.TestServiceID,
		testutil.TestAppAddress(),
		100, 104,
	)
	s.Require().NoError(err)

	// Store proof TX hash
	proofTxHash := "proof_tx_hash_xyz"
	err = s.coordinator.OnProofSubmitted(ctx, sessionID, proofTxHash)
	s.Require().NoError(err)

	// Verify stored
	snapshot, err := s.sessionStore.Get(ctx, sessionID)
	s.Require().NoError(err)
	s.Require().Equal(proofTxHash, snapshot.ProofTxHash)
}

// TestSessionCoordinator_OnSessionProved verifies terminal state transition to proved.
func (s *SessionCoordinatorTestSuite) TestSessionCoordinator_OnSessionProved() {
	ctx := context.Background()
	sessionID := "proved_010"

	// Create session
	err := s.coordinator.OnSessionCreated(
		ctx, sessionID,
		testutil.TestSupplierAddress(),
		testutil.TestServiceID,
		testutil.TestAppAddress(),
		100, 104,
	)
	s.Require().NoError(err)

	// Mark as proved
	err = s.coordinator.OnSessionProved(ctx, sessionID)
	s.Require().NoError(err)

	// Verify terminal state
	snapshot, err := s.sessionStore.Get(ctx, sessionID)
	s.Require().NoError(err)
	s.Require().Equal(SessionStateProved, snapshot.State)
	s.Require().True(snapshot.State.IsTerminal())
	s.Require().True(snapshot.State.IsSuccess())
}

// TestSessionCoordinator_TerminalStateCallbacks verifies terminal callbacks are invoked.
func (s *SessionCoordinatorTestSuite) TestSessionCoordinator_TerminalStateCallbacks() {
	ctx := context.Background()

	// Create coordinator with callback
	var callbackInvoked atomic.Bool
	var callbackSessionID string
	var callbackState SessionState
	var mu sync.Mutex

	s.coordinator.SetOnSessionTerminalCallback(func(sessionID string, state SessionState) {
		mu.Lock()
		defer mu.Unlock()
		callbackInvoked.Store(true)
		callbackSessionID = sessionID
		callbackState = state
	})

	// Create session
	sessionID := "terminal_callback_011"
	err := s.coordinator.OnSessionCreated(
		ctx, sessionID,
		testutil.TestSupplierAddress(),
		testutil.TestServiceID,
		testutil.TestAppAddress(),
		100, 104,
	)
	s.Require().NoError(err)

	// Transition to terminal state
	err = s.coordinator.OnSessionProved(ctx, sessionID)
	s.Require().NoError(err)

	// Verify callback was invoked
	s.Require().True(callbackInvoked.Load(), "terminal callback should be invoked")
	mu.Lock()
	s.Require().Equal(sessionID, callbackSessionID)
	s.Require().Equal(SessionStateProved, callbackState)
	mu.Unlock()
}

// TestSessionCoordinator_OnClaimWindowClosed verifies claim window timeout handling.
func (s *SessionCoordinatorTestSuite) TestSessionCoordinator_OnClaimWindowClosed() {
	ctx := context.Background()
	sessionID := "claim_window_closed_012"

	// Create session
	err := s.coordinator.OnSessionCreated(
		ctx, sessionID,
		testutil.TestSupplierAddress(),
		testutil.TestServiceID,
		testutil.TestAppAddress(),
		100, 104,
	)
	s.Require().NoError(err)

	// Mark claim window closed
	err = s.coordinator.OnClaimWindowClosed(ctx, sessionID)
	s.Require().NoError(err)

	// Verify failure state
	snapshot, err := s.sessionStore.Get(ctx, sessionID)
	s.Require().NoError(err)
	s.Require().Equal(SessionStateClaimWindowClosed, snapshot.State)
	s.Require().True(snapshot.State.IsTerminal())
	s.Require().True(snapshot.State.IsFailure())
}

// TestSessionCoordinator_SessionCreatedCallback verifies OnSessionCreated callback.
func (s *SessionCoordinatorTestSuite) TestSessionCoordinator_SessionCreatedCallback() {
	ctx := context.Background()

	// Set callback
	var callbackInvoked atomic.Bool
	var callbackSnapshot *SessionSnapshot
	var mu sync.Mutex

	s.coordinator.SetOnSessionCreatedCallback(func(ctx context.Context, snapshot *SessionSnapshot) error {
		mu.Lock()
		defer mu.Unlock()
		callbackInvoked.Store(true)
		callbackSnapshot = snapshot
		return nil
	})

	// Create session
	sessionID := "created_callback_013"
	err := s.coordinator.OnSessionCreated(
		ctx, sessionID,
		testutil.TestSupplierAddress(),
		testutil.TestServiceID,
		testutil.TestAppAddress(),
		100, 104,
	)
	s.Require().NoError(err)

	// Verify callback was invoked
	s.Require().True(callbackInvoked.Load(), "created callback should be invoked")
	mu.Lock()
	s.Require().NotNil(callbackSnapshot)
	s.Require().Equal(sessionID, callbackSnapshot.SessionID)
	mu.Unlock()
}

// TestSessionCoordinatorTestSuite runs the test suite.
func TestSessionCoordinatorTestSuite(t *testing.T) {
	suite.Run(t, new(SessionCoordinatorTestSuite))
}
