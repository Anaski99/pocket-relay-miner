//go:build test

package miner

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alitto/pond/v2"
	"github.com/hashicorp/go-version"
	"github.com/stretchr/testify/suite"

	localclient "github.com/pokt-network/pocket-relay-miner/client"
	"github.com/pokt-network/pocket-relay-miner/logging"
	redisutil "github.com/pokt-network/pocket-relay-miner/transport/redis"
	"github.com/pokt-network/poktroll/pkg/client"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
)

// Test constants to avoid import cycle with testutil
const (
	slcTestSupplierAddr = "pokt1supplier1234567890abcdef"
	slcTestAppAddr      = "pokt1app1234567890abcdef"
	slcTestServiceID    = "anvil"
)

// SessionLifecycleTestSuite tests the session lifecycle state machine.
// This suite covers:
// - All 10 SessionState values and their transitions
// - Table-driven determineTransition() tests
// - Happy path scenarios (settled, probabilistic, failed)
// - Session tracking operations (add, remove, get by state)
type SessionLifecycleTestSuite struct {
	suite.Suite

	// Redis infrastructure
	miniRedis   *miniredis.Miniredis
	redisClient *redisutil.Client

	// Mock dependencies
	sessionStore *slcMockSessionStore
	sharedClient *slcMockSharedQueryClient
	blockClient  *slcMockBlockClient
	callback     *slcMockCallback

	// Worker pool for testing
	workerPool pond.Pool

	// Manager under test
	manager *SessionLifecycleManager

	// Context
	ctx context.Context
}

// Helper function to create deterministic session snapshots
func slcNewSession(seed int) *SessionSnapshot {
	sessionID := fmt.Sprintf("session-%d", seed)
	baseHeight := int64(100 + (seed * 4))

	return &SessionSnapshot{
		SessionID:               sessionID,
		SupplierOperatorAddress: slcTestSupplierAddr,
		ServiceID:               slcTestServiceID,
		ApplicationAddress:      slcTestAppAddr,
		SessionStartHeight:      baseHeight,
		SessionEndHeight:        baseHeight + 4,
		State:                   SessionStateActive,
		RelayCount:              int64(seed*10 + 50),
		TotalComputeUnits:       uint64((seed*10 + 50) * 100),
		CreatedAt:               time.Now(),
		LastUpdatedAt:           time.Now(),
	}
}

// slcMockSessionStore implements SessionStore for testing
type slcMockSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*SessionSnapshot
	saveErr  error
	getErr   error
}

func newSLCMockSessionStore() *slcMockSessionStore {
	return &slcMockSessionStore{
		sessions: make(map[string]*SessionSnapshot),
	}
}

func (m *slcMockSessionStore) Save(ctx context.Context, snapshot *SessionSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	m.sessions[snapshot.SessionID] = snapshot
	return nil
}

func (m *slcMockSessionStore) Get(ctx context.Context, sessionID string) (*SessionSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	snapshot, exists := m.sessions[sessionID]
	if !exists {
		return nil, nil
	}
	// Return a copy to avoid race conditions
	cp := *snapshot
	return &cp, nil
}

func (m *slcMockSessionStore) GetBySupplier(ctx context.Context) ([]*SessionSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*SessionSnapshot, 0, len(m.sessions))
	for _, snapshot := range m.sessions {
		result = append(result, snapshot)
	}
	return result, nil
}

func (m *slcMockSessionStore) GetByState(ctx context.Context, state SessionState) ([]*SessionSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*SessionSnapshot, 0)
	for _, snapshot := range m.sessions {
		if snapshot.State == state {
			result = append(result, snapshot)
		}
	}
	return result, nil
}

func (m *slcMockSessionStore) Delete(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionID)
	return nil
}

func (m *slcMockSessionStore) UpdateState(ctx context.Context, sessionID string, newState SessionState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if snapshot, exists := m.sessions[sessionID]; exists {
		snapshot.State = newState
		snapshot.LastUpdatedAt = time.Now()
	}
	return nil
}

func (m *slcMockSessionStore) UpdateSettlementMetadata(ctx context.Context, sessionID string, outcome string, height int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if snapshot, exists := m.sessions[sessionID]; exists {
		snapshot.SettlementOutcome = &outcome
		snapshot.SettlementHeight = &height
	}
	return nil
}

func (m *slcMockSessionStore) UpdateWALPosition(ctx context.Context, sessionID string, walEntryID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if snapshot, exists := m.sessions[sessionID]; exists {
		snapshot.LastWALEntryID = walEntryID
	}
	return nil
}

func (m *slcMockSessionStore) IncrementRelayCount(ctx context.Context, sessionID string, computeUnits uint64) error {
	// Note: In production, this modifies the Redis store (separate from in-memory activeSessions).
	// Here we do nothing to avoid race conditions with in-memory state.
	// The in-memory activeSessions update happens synchronously in UpdateSessionRelayCount.
	return nil
}

func (m *slcMockSessionStore) Close() error {
	return nil
}

func (m *slcMockSessionStore) getSnapshot(sessionID string) *SessionSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if snapshot, exists := m.sessions[sessionID]; exists {
		cp := *snapshot
		return &cp
	}
	return nil
}

func (m *slcMockSessionStore) setSession(snapshot *SessionSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[snapshot.SessionID] = snapshot
}

// slcMockBlockClient implements client.BlockClient for testing
type slcMockBlockClient struct {
	mu            sync.RWMutex
	currentHeight int64
	blockHash     []byte
	subscribers   []chan *localclient.SimpleBlock
	closed        bool
}

func newSLCMockBlockClient(height int64) *slcMockBlockClient {
	return &slcMockBlockClient{
		currentHeight: height,
		blockHash:     []byte("mock-block-hash"),
		subscribers:   make([]chan *localclient.SimpleBlock, 0),
	}
}

func (m *slcMockBlockClient) LastBlock(ctx context.Context) client.Block {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return &slcMockBlock{
		height: m.currentHeight,
		hash:   m.blockHash,
	}
}

func (m *slcMockBlockClient) CommittedBlocksSequence(ctx context.Context) client.BlockReplayObservable {
	return nil
}

func (m *slcMockBlockClient) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for _, ch := range m.subscribers {
		close(ch)
	}
	m.subscribers = nil
}

func (m *slcMockBlockClient) GetChainVersion() *version.Version {
	v, _ := version.NewVersion("0.1.0")
	return v
}

// Subscribe implements fan-out block subscription
func (m *slcMockBlockClient) Subscribe(ctx context.Context, bufferSize int) <-chan *localclient.SimpleBlock {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch := make(chan *localclient.SimpleBlock, bufferSize)
	m.subscribers = append(m.subscribers, ch)
	return ch
}

func (m *slcMockBlockClient) advanceBlock() {
	m.mu.Lock()
	height := m.currentHeight + 1
	m.currentHeight = height
	subscribers := make([]chan *localclient.SimpleBlock, len(m.subscribers))
	copy(subscribers, m.subscribers)
	m.mu.Unlock()

	block := localclient.NewSimpleBlock(height, []byte("mock-block-hash"), time.Now())

	for _, ch := range subscribers {
		select {
		case ch <- block:
		default:
		}
	}
}

func (m *slcMockBlockClient) setHeight(height int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentHeight = height
}

type slcMockBlock struct {
	height int64
	hash   []byte
}

func (b *slcMockBlock) Height() int64 { return b.height }
func (b *slcMockBlock) Hash() []byte  { return b.hash }

// slcMockSharedQueryClient implements client.SharedQueryClient for testing
type slcMockSharedQueryClient struct {
	params *sharedtypes.Params
}

func (m *slcMockSharedQueryClient) GetParams(ctx context.Context) (*sharedtypes.Params, error) {
	return m.params, nil
}

func (m *slcMockSharedQueryClient) GetSessionGracePeriodEndHeight(ctx context.Context, queryHeight int64) (int64, error) {
	return sharedtypes.GetSessionGracePeriodEndHeight(m.params, queryHeight), nil
}

func (m *slcMockSharedQueryClient) GetClaimWindowOpenHeight(ctx context.Context, queryHeight int64) (int64, error) {
	return sharedtypes.GetClaimWindowOpenHeight(m.params, queryHeight), nil
}

func (m *slcMockSharedQueryClient) GetEarliestSupplierClaimCommitHeight(ctx context.Context, queryHeight int64, supplierAddr string) (int64, error) {
	return queryHeight + 1, nil
}

func (m *slcMockSharedQueryClient) GetProofWindowOpenHeight(ctx context.Context, queryHeight int64) (int64, error) {
	return sharedtypes.GetProofWindowOpenHeight(m.params, queryHeight), nil
}

func (m *slcMockSharedQueryClient) GetEarliestSupplierProofCommitHeight(ctx context.Context, queryHeight int64, supplierAddr string) (int64, error) {
	return queryHeight + 1, nil
}

// slcMockCallback implements SessionLifecycleCallback for testing
type slcMockCallback struct {
	mu sync.Mutex

	// Track invocations
	onActiveCount            int
	onNeedClaimCount         int
	onNeedProofCount         int
	onProvedCount            int
	onProbabilisticCount     int
	onClaimWindowClosedCount int
	onClaimTxErrorCount      int
	onProofWindowClosedCount int
	onProofTxErrorCount      int

	// Track sessions
	activeSessions            []*SessionSnapshot
	claimBatches              [][]*SessionSnapshot
	proofBatches              [][]*SessionSnapshot
	provedSessions            []*SessionSnapshot
	probabilisticSessions     []*SessionSnapshot
	claimWindowClosedSessions []*SessionSnapshot

	// Control behavior
	claimErr        error
	proofErr        error
	claimRootHashes [][]byte
}

func newSLCMockCallback() *slcMockCallback {
	return &slcMockCallback{}
}

func (m *slcMockCallback) OnSessionActive(ctx context.Context, snapshot *SessionSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onActiveCount++
	m.activeSessions = append(m.activeSessions, snapshot)
	return nil
}

func (m *slcMockCallback) OnSessionsNeedClaim(ctx context.Context, snapshots []*SessionSnapshot) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onNeedClaimCount++
	m.claimBatches = append(m.claimBatches, snapshots)

	if m.claimErr != nil {
		return nil, m.claimErr
	}

	hashes := m.claimRootHashes
	if hashes == nil {
		hashes = make([][]byte, len(snapshots))
		for i := range snapshots {
			hashes[i] = []byte("mock-root-hash")
		}
	}
	return hashes, nil
}

func (m *slcMockCallback) OnSessionsNeedProof(ctx context.Context, snapshots []*SessionSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onNeedProofCount++
	m.proofBatches = append(m.proofBatches, snapshots)
	return m.proofErr
}

func (m *slcMockCallback) OnSessionProved(ctx context.Context, snapshot *SessionSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onProvedCount++
	m.provedSessions = append(m.provedSessions, snapshot)
	return nil
}

func (m *slcMockCallback) OnProbabilisticProved(ctx context.Context, snapshot *SessionSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onProbabilisticCount++
	m.probabilisticSessions = append(m.probabilisticSessions, snapshot)
	return nil
}

func (m *slcMockCallback) OnClaimWindowClosed(ctx context.Context, snapshot *SessionSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onClaimWindowClosedCount++
	m.claimWindowClosedSessions = append(m.claimWindowClosedSessions, snapshot)
	return nil
}

func (m *slcMockCallback) OnClaimTxError(ctx context.Context, snapshot *SessionSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onClaimTxErrorCount++
	return nil
}

func (m *slcMockCallback) OnProofWindowClosed(ctx context.Context, snapshot *SessionSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onProofWindowClosedCount++
	return nil
}

func (m *slcMockCallback) OnProofTxError(ctx context.Context, snapshot *SessionSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onProofTxErrorCount++
	return nil
}

func (m *slcMockCallback) getOnNeedClaimCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.onNeedClaimCount
}

func (m *slcMockCallback) getOnClaimWindowClosedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.onClaimWindowClosedCount
}

func (m *slcMockCallback) getOnProofWindowClosedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.onProofWindowClosedCount
}

func (m *slcMockCallback) getOnProbabilisticCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.onProbabilisticCount
}

// SetupSuite initializes the test suite
func (s *SessionLifecycleTestSuite) SetupSuite() {
	var err error
	s.miniRedis, err = miniredis.Run()
	s.Require().NoError(err)

	s.ctx = context.Background()
	redisURL := fmt.Sprintf("redis://%s", s.miniRedis.Addr())
	s.redisClient, err = redisutil.NewClient(s.ctx, redisutil.ClientConfig{URL: redisURL})
	s.Require().NoError(err)

	s.workerPool = pond.NewPool(10)
}

// TearDownSuite cleans up the test suite
func (s *SessionLifecycleTestSuite) TearDownSuite() {
	if s.workerPool != nil {
		s.workerPool.StopAndWait()
	}
	if s.miniRedis != nil {
		s.miniRedis.Close()
	}
	if s.redisClient != nil {
		s.redisClient.Close()
	}
}

// SetupTest prepares each test
func (s *SessionLifecycleTestSuite) SetupTest() {
	s.miniRedis.FlushAll()

	// Create fresh mocks
	s.sessionStore = newSLCMockSessionStore()
	s.sharedClient = &slcMockSharedQueryClient{
		params: &sharedtypes.Params{
			NumBlocksPerSession:            4,
			GracePeriodEndOffsetBlocks:     1,
			ClaimWindowOpenOffsetBlocks:    1,
			ClaimWindowCloseOffsetBlocks:   4,
			ProofWindowOpenOffsetBlocks:    0,
			ProofWindowCloseOffsetBlocks:   4,
			ComputeUnitsToTokensMultiplier: 42,
		},
	}
	s.blockClient = newSLCMockBlockClient(100)
	s.callback = newSLCMockCallback()

	logger := logging.NewLoggerFromConfig(logging.DefaultConfig())
	config := SessionLifecycleConfig{
		SupplierAddress:          slcTestSupplierAddr,
		CheckIntervalBlocks:     1,
		MaxConcurrentTransitions: 10,
		CheckInterval:           50 * time.Millisecond,
	}

	s.manager = NewSessionLifecycleManager(
		logger,
		s.sessionStore,
		s.sharedClient,
		s.blockClient,
		s.callback,
		config,
		nil,
		s.workerPool,
	)
}

// TearDownTest cleans up after each test
func (s *SessionLifecycleTestSuite) TearDownTest() {
	if s.manager != nil {
		s.manager.Close()
	}
}

// TestDetermineTransition uses table-driven tests for the 10 states
func (s *SessionLifecycleTestSuite) TestDetermineTransition() {
	params := s.sharedClient.params

	// Session with end height 104
	// Claim window: opens at 106 (104+1+1), closes at 110 (104+1+4+1)
	// Proof window: opens at 110, closes at 114

	tests := []struct {
		name           string
		initialState   SessionState
		currentHeight  int64
		sessionEnd     int64
		expectedState  SessionState
		expectedAction string
	}{
		// Active state transitions
		{"active_stays_active_before_claim_window", SessionStateActive, 105, 104, "", ""},
		{"active_to_claiming_when_claim_window_opens", SessionStateActive, 106, 104, SessionStateClaiming, "claim_window_open"},
		{"active_to_claiming_mid_claim_window", SessionStateActive, 108, 104, SessionStateClaiming, "claim_window_open"},
		{"active_to_claim_window_closed_after_timeout", SessionStateActive, 110, 104, SessionStateClaimWindowClosed, "claim_window_timeout"},

		// Claiming state transitions
		{"claiming_stays_claiming_in_window", SessionStateClaiming, 108, 104, "", ""},
		{"claiming_to_claim_window_closed_on_timeout", SessionStateClaiming, 110, 104, SessionStateClaimWindowClosed, "claim_timeout"},

		// Claimed state transitions
		{"claimed_stays_claimed_before_proof_window", SessionStateClaimed, 109, 104, "", ""},
		{"claimed_to_proving_when_proof_window_opens", SessionStateClaimed, 110, 104, SessionStateProving, "proof_window_open"},
		{"claimed_to_proving_mid_proof_window", SessionStateClaimed, 112, 104, SessionStateProving, "proof_window_open"},
		{"claimed_to_proof_window_closed_after_timeout", SessionStateClaimed, 114, 104, SessionStateProofWindowClosed, "proof_timeout"},

		// Proving state transitions
		{"proving_stays_proving_in_window", SessionStateProving, 112, 104, "", ""},
		{"proving_to_proof_window_closed_on_timeout", SessionStateProving, 114, 104, SessionStateProofWindowClosed, "proof_timeout"},

		// Terminal states - no transitions
		{"proved_no_transition", SessionStateProved, 150, 104, "", ""},
		{"probabilistic_proved_no_transition", SessionStateProbabilisticProved, 150, 104, "", ""},
		{"claim_window_closed_no_transition", SessionStateClaimWindowClosed, 150, 104, "", ""},
		{"claim_tx_error_no_transition", SessionStateClaimTxError, 150, 104, "", ""},
		{"proof_window_closed_no_transition", SessionStateProofWindowClosed, 150, 104, "", ""},
		{"proof_tx_error_no_transition", SessionStateProofTxError, 150, 104, "", ""},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			snapshot := &SessionSnapshot{
				SessionID:               "test-session",
				SupplierOperatorAddress: slcTestSupplierAddr,
				ServiceID:               slcTestServiceID,
				ApplicationAddress:      slcTestAppAddr,
				SessionStartHeight:      tt.sessionEnd - 4,
				SessionEndHeight:        tt.sessionEnd,
				State:                   tt.initialState,
			}

			newState, action := s.manager.determineTransition(snapshot, tt.currentHeight, params)

			s.Equal(tt.expectedState, newState, "unexpected state for %s", tt.name)
			s.Equal(tt.expectedAction, action, "unexpected action for %s", tt.name)
		})
	}
}

// TestSessionStateHelpers tests IsTerminal, IsSuccess, IsFailure methods
func (s *SessionLifecycleTestSuite) TestSessionStateHelpers() {
	// Test IsTerminal
	terminalStates := []SessionState{
		SessionStateProved,
		SessionStateProbabilisticProved,
		SessionStateClaimWindowClosed,
		SessionStateClaimTxError,
		SessionStateProofWindowClosed,
		SessionStateProofTxError,
	}
	for _, state := range terminalStates {
		s.True(state.IsTerminal(), "%s should be terminal", state)
	}

	nonTerminalStates := []SessionState{
		SessionStateActive,
		SessionStateClaiming,
		SessionStateClaimed,
		SessionStateProving,
	}
	for _, state := range nonTerminalStates {
		s.False(state.IsTerminal(), "%s should not be terminal", state)
	}

	// Test IsSuccess
	s.True(SessionStateProved.IsSuccess())
	s.True(SessionStateProbabilisticProved.IsSuccess())
	s.False(SessionStateClaimWindowClosed.IsSuccess())

	// Test IsFailure
	s.True(SessionStateClaimWindowClosed.IsFailure())
	s.True(SessionStateClaimTxError.IsFailure())
	s.True(SessionStateProofWindowClosed.IsFailure())
	s.True(SessionStateProofTxError.IsFailure())
	s.False(SessionStateProved.IsFailure())
}

// TestTrackSession_AddsToActiveMap tests adding sessions
func (s *SessionLifecycleTestSuite) TestTrackSession_AddsToActiveMap() {
	snapshot := slcNewSession(1)

	err := s.manager.TrackSession(s.ctx, snapshot)
	s.Require().NoError(err)

	tracked := s.manager.GetSession(snapshot.SessionID)
	s.Require().NotNil(tracked)
	s.Equal(snapshot.SessionID, tracked.SessionID)
}

// TestTrackSession_Duplicate tests duplicate session handling
func (s *SessionLifecycleTestSuite) TestTrackSession_Duplicate() {
	snapshot1 := slcNewSession(1)
	snapshot1.SessionID = "duplicate-session"
	snapshot1.RelayCount = 10

	snapshot2 := slcNewSession(2)
	snapshot2.SessionID = "duplicate-session"
	snapshot2.RelayCount = 20

	err := s.manager.TrackSession(s.ctx, snapshot1)
	s.Require().NoError(err)

	err = s.manager.TrackSession(s.ctx, snapshot2)
	s.Require().NoError(err)

	tracked := s.manager.GetSession("duplicate-session")
	s.Require().NotNil(tracked)
	s.Equal(int64(20), tracked.RelayCount)
}

// TestRemoveSession_RemovesFromActiveMap tests session removal
func (s *SessionLifecycleTestSuite) TestRemoveSession_RemovesFromActiveMap() {
	snapshot := slcNewSession(1)

	err := s.manager.TrackSession(s.ctx, snapshot)
	s.Require().NoError(err)

	s.manager.RemoveSession(snapshot.SessionID)

	tracked := s.manager.GetSession(snapshot.SessionID)
	s.Nil(tracked)
}

// TestGetSessionsByState_FiltersCorrectly tests state-based filtering
func (s *SessionLifecycleTestSuite) TestGetSessionsByState_FiltersCorrectly() {
	active1 := slcNewSession(1)
	active1.State = SessionStateActive

	active2 := slcNewSession(2)
	active2.State = SessionStateActive

	claiming := slcNewSession(3)
	claiming.State = SessionStateClaiming

	claimed := slcNewSession(4)
	claimed.State = SessionStateClaimed

	for _, snap := range []*SessionSnapshot{active1, active2, claiming, claimed} {
		err := s.manager.TrackSession(s.ctx, snap)
		s.Require().NoError(err)
	}

	// Test GetActiveSessions
	activeSessions := s.manager.GetActiveSessions()
	s.Len(activeSessions, 2)

	// Test GetSessionsByState
	byClaiming := s.manager.GetSessionsByState(SessionStateClaiming)
	s.Len(byClaiming, 1)

	byClaimed := s.manager.GetSessionsByState(SessionStateClaimed)
	s.Len(byClaimed, 1)

	byProved := s.manager.GetSessionsByState(SessionStateProved)
	s.Empty(byProved)
}

// TestGetSession_NotFound tests getting non-existent session
func (s *SessionLifecycleTestSuite) TestGetSession_NotFound() {
	tracked := s.manager.GetSession("nonexistent")
	s.Nil(tracked)
}

// TestHasPendingSessions tests pending session detection
func (s *SessionLifecycleTestSuite) TestHasPendingSessions() {
	s.False(s.manager.HasPendingSessions())
	s.Equal(0, s.manager.GetPendingSessionCount())

	snapshot := slcNewSession(1)
	err := s.manager.TrackSession(s.ctx, snapshot)
	s.Require().NoError(err)

	s.True(s.manager.HasPendingSessions())
	s.Equal(1, s.manager.GetPendingSessionCount())

	s.manager.RemoveSession(snapshot.SessionID)

	s.False(s.manager.HasPendingSessions())
	s.Equal(0, s.manager.GetPendingSessionCount())
}

// TestUpdateSessionRelayCount tests relay count updates
func (s *SessionLifecycleTestSuite) TestUpdateSessionRelayCount() {
	snapshot := slcNewSession(1)
	snapshot.RelayCount = 0
	snapshot.TotalComputeUnits = 0

	err := s.manager.TrackSession(s.ctx, snapshot)
	s.Require().NoError(err)

	err = s.manager.UpdateSessionRelayCount(s.ctx, snapshot.SessionID, 100)
	s.Require().NoError(err)

	// Verify in-memory state immediately (UpdateSessionRelayCount updates in-memory synchronously)
	// The persist to store is async but we're testing the in-memory state which is what GetSession returns
	tracked := s.manager.GetSession(snapshot.SessionID)
	s.Equal(int64(1), tracked.RelayCount)
	s.Equal(uint64(100), tracked.TotalComputeUnits)
}

// TestUpdateSessionRelayCount_SessionNotFound tests error handling
func (s *SessionLifecycleTestSuite) TestUpdateSessionRelayCount_SessionNotFound() {
	err := s.manager.UpdateSessionRelayCount(s.ctx, "nonexistent", 100)
	s.Error(err)
	s.Contains(err.Error(), "not found")
}

// TestClose_GracefulShutdown tests graceful shutdown
func (s *SessionLifecycleTestSuite) TestClose_GracefulShutdown() {
	err := s.manager.Start(s.ctx)
	s.Require().NoError(err)

	err = s.manager.Close()
	s.NoError(err)

	err = s.manager.Close()
	s.NoError(err)
}

// TestClose_BeforeStart tests closing before starting
func (s *SessionLifecycleTestSuite) TestClose_BeforeStart() {
	err := s.manager.Close()
	s.NoError(err)
}

// TestTrackSession_AfterClose tests tracking after close
func (s *SessionLifecycleTestSuite) TestTrackSession_AfterClose() {
	err := s.manager.Close()
	s.Require().NoError(err)

	snapshot := slcNewSession(1)
	err = s.manager.TrackSession(s.ctx, snapshot)
	s.Error(err)
	s.Contains(err.Error(), "closed")
}

// TestStart_AfterClose tests starting after close
func (s *SessionLifecycleTestSuite) TestStart_AfterClose() {
	err := s.manager.Close()
	s.Require().NoError(err)

	err = s.manager.Start(s.ctx)
	s.Error(err)
	s.Contains(err.Error(), "closed")
}

// TestSessionWindow_Calculations tests SessionWindow helper methods
func (s *SessionLifecycleTestSuite) TestSessionWindow_Calculations() {
	params := s.sharedClient.params
	sessionEndHeight := int64(104)

	window := CalculateSessionWindow(params, sessionEndHeight)

	s.Equal(int64(104), window.SessionEndHeight)
	s.Equal(int64(105), window.GracePeriodEnd)
	s.Equal(int64(106), window.ClaimWindowOpen)
	s.Equal(int64(110), window.ClaimWindowClose)
	s.Equal(int64(110), window.ProofWindowOpen)
	s.Equal(int64(114), window.ProofWindowClose)

	// Test IsInClaimWindow
	s.False(window.IsInClaimWindow(105))
	s.True(window.IsInClaimWindow(106))
	s.True(window.IsInClaimWindow(108))
	s.False(window.IsInClaimWindow(110))

	// Test IsInProofWindow
	s.False(window.IsInProofWindow(109))
	s.True(window.IsInProofWindow(110))
	s.True(window.IsInProofWindow(112))
	s.False(window.IsInProofWindow(114))

	// Test BlocksUntil methods
	s.Equal(int64(4), window.BlocksUntilClaimWindowClose(106))
	s.Equal(int64(0), window.BlocksUntilClaimWindowClose(115))
	s.Equal(int64(4), window.BlocksUntilProofWindowClose(110))
	s.Equal(int64(0), window.BlocksUntilProofWindowClose(120))
}

// TestLoadExistingSessions_SkipsTerminalStates tests startup loading
func (s *SessionLifecycleTestSuite) TestLoadExistingSessions_SkipsTerminalStates() {
	active := slcNewSession(1)
	active.State = SessionStateActive

	claimed := slcNewSession(2)
	claimed.State = SessionStateClaimed

	proved := slcNewSession(3)
	proved.State = SessionStateProved

	failed := slcNewSession(4)
	failed.State = SessionStateClaimWindowClosed

	for _, snap := range []*SessionSnapshot{active, claimed, proved, failed} {
		s.sessionStore.setSession(snap)
	}

	err := s.manager.Start(s.ctx)
	s.Require().NoError(err)

	s.True(s.manager.HasPendingSessions())
	s.Equal(2, s.manager.GetPendingSessionCount())

	s.NotNil(s.manager.GetSession(active.SessionID))
	s.NotNil(s.manager.GetSession(claimed.SessionID))
	s.Nil(s.manager.GetSession(proved.SessionID))
	s.Nil(s.manager.GetSession(failed.SessionID))
}

// TestSessionMightNeedTransition_Filtering tests optimization filter
func (s *SessionLifecycleTestSuite) TestSessionMightNeedTransition_Filtering() {
	params := s.sharedClient.params
	sessionEndHeight := int64(104)

	tests := []struct {
		name          string
		state         SessionState
		currentHeight int64
		expectNeed    bool
	}{
		{"active_before_claim_window", SessionStateActive, 105, false},
		{"active_at_claim_window", SessionStateActive, 106, true},
		{"active_after_claim_window", SessionStateActive, 110, true},
		{"claiming_in_window", SessionStateClaiming, 108, true},
		{"claimed_before_proof_window", SessionStateClaimed, 109, false},
		{"claimed_at_proof_window", SessionStateClaimed, 110, true},
		{"proving_in_window", SessionStateProving, 112, true},
		{"proved_terminal", SessionStateProved, 150, true},
		{"claim_window_closed_terminal", SessionStateClaimWindowClosed, 150, true},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			snapshot := &SessionSnapshot{
				SessionID:          "test",
				SessionStartHeight: sessionEndHeight - 4,
				SessionEndHeight:   sessionEndHeight,
				State:              tt.state,
			}

			result := s.manager.sessionMightNeedTransition(snapshot, tt.currentHeight, params)
			s.Equal(tt.expectNeed, result, "unexpected result for %s", tt.name)
		})
	}
}

// TestCheckSessionTransitions_ActiveToClaimingBatch tests batched claiming
func (s *SessionLifecycleTestSuite) TestCheckSessionTransitions_ActiveToClaimingBatch() {
	// Initialize shared params
	err := s.manager.refreshSharedParams(s.ctx)
	s.Require().NoError(err)

	sessionEndHeight := int64(104)
	sessions := make([]*SessionSnapshot, 3)
	for i := 0; i < 3; i++ {
		sessions[i] = &SessionSnapshot{
			SessionID:               fmt.Sprintf("session-%d", i),
			SupplierOperatorAddress: slcTestSupplierAddr,
			ServiceID:               slcTestServiceID,
			ApplicationAddress:      slcTestAppAddr,
			SessionStartHeight:      sessionEndHeight - 4,
			SessionEndHeight:        sessionEndHeight,
			State:                   SessionStateActive,
		}
		s.sessionStore.setSession(sessions[i])
		s.manager.activeSessions.Store(sessions[i].SessionID, sessions[i])
	}

	claimWindowOpen := int64(106)
	s.blockClient.setHeight(claimWindowOpen)

	s.manager.checkSessionTransitions(s.ctx, claimWindowOpen)

	time.Sleep(100 * time.Millisecond)

	s.Equal(1, s.callback.getOnNeedClaimCount())
	s.Require().Len(s.callback.claimBatches, 1)
	s.Len(s.callback.claimBatches[0], 3)
}

// TestCheckSessionTransitions_CleanupTerminalFromMemory tests terminal cleanup
func (s *SessionLifecycleTestSuite) TestCheckSessionTransitions_CleanupTerminalFromMemory() {
	// Initialize shared params
	err := s.manager.refreshSharedParams(s.ctx)
	s.Require().NoError(err)

	sessionEndHeight := int64(104)
	snapshot := &SessionSnapshot{
		SessionID:               "terminal-cleanup-test",
		SupplierOperatorAddress: slcTestSupplierAddr,
		ServiceID:               slcTestServiceID,
		ApplicationAddress:      slcTestAppAddr,
		SessionStartHeight:      sessionEndHeight - 4,
		SessionEndHeight:        sessionEndHeight,
		State:                   SessionStateActive,
	}

	// Store as terminal in Redis but active in memory
	terminalSnapshot := *snapshot
	terminalSnapshot.State = SessionStateProved
	s.sessionStore.setSession(&terminalSnapshot)
	s.manager.activeSessions.Store(snapshot.SessionID, snapshot)

	currentHeight := int64(150)
	s.manager.checkSessionTransitions(s.ctx, currentHeight)

	time.Sleep(50 * time.Millisecond)

	tracked := s.manager.GetSession(snapshot.SessionID)
	s.Nil(tracked)
}

// TestExecuteTransition_TerminalCallbacks tests terminal state callbacks
func (s *SessionLifecycleTestSuite) TestExecuteTransition_TerminalCallbacks() {
	sessionEndHeight := int64(104)

	tests := []struct {
		name          string
		newState      SessionState
		checkCallback func() int
	}{
		{"claim_window_closed", SessionStateClaimWindowClosed, s.callback.getOnClaimWindowClosedCount},
		{"proof_window_closed", SessionStateProofWindowClosed, s.callback.getOnProofWindowClosedCount},
		{"probabilistic_proved", SessionStateProbabilisticProved, s.callback.getOnProbabilisticCount},
	}

	for i, tt := range tests {
		s.Run(tt.name, func() {
			snapshot := &SessionSnapshot{
				SessionID:               fmt.Sprintf("terminal-%d", 100+i),
				SupplierOperatorAddress: slcTestSupplierAddr,
				ServiceID:               slcTestServiceID,
				ApplicationAddress:      slcTestAppAddr,
				SessionStartHeight:      sessionEndHeight - 4,
				SessionEndHeight:        sessionEndHeight,
				State:                   SessionStateActive,
			}

			s.sessionStore.setSession(snapshot)
			s.manager.activeSessions.Store(snapshot.SessionID, snapshot)

			s.manager.executeTransition(s.ctx, snapshot, tt.newState, "test_action")

			s.GreaterOrEqual(tt.checkCallback(), 1)

			tracked := s.manager.GetSession(snapshot.SessionID)
			s.Nil(tracked, "terminal session should be removed from tracking")
		})
	}
}

// TestWaitForSettlement_NoPendingSessions tests immediate return
func (s *SessionLifecycleTestSuite) TestWaitForSettlement_NoPendingSessions() {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := s.manager.WaitForSettlement(ctx)
	s.NoError(err)
}

// TestWaitForSettlement_Timeout tests context cancellation
func (s *SessionLifecycleTestSuite) TestWaitForSettlement_Timeout() {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	snapshot := slcNewSession(1)
	err := s.manager.TrackSession(context.Background(), snapshot)
	s.Require().NoError(err)

	err = s.manager.WaitForSettlement(ctx)
	s.Error(err)
	s.Equal(context.DeadlineExceeded, err)
}

// TestRefreshSharedParams tests shared params refresh
func (s *SessionLifecycleTestSuite) TestRefreshSharedParams() {
	// First refresh to load initial params
	err := s.manager.refreshSharedParams(s.ctx)
	s.Require().NoError(err)

	params := s.manager.getSharedParams()
	s.NotNil(params)
	s.Equal(uint64(4), params.NumBlocksPerSession)

	// Update mock params
	s.sharedClient.params = &sharedtypes.Params{
		NumBlocksPerSession:            8,
		GracePeriodEndOffsetBlocks:     2,
		ClaimWindowOpenOffsetBlocks:    2,
		ClaimWindowCloseOffsetBlocks:   6,
		ProofWindowOpenOffsetBlocks:    0,
		ProofWindowCloseOffsetBlocks:   6,
		ComputeUnitsToTokensMultiplier: 50,
	}

	// Refresh again
	err = s.manager.refreshSharedParams(s.ctx)
	s.NoError(err)

	params = s.manager.getSharedParams()
	s.Equal(uint64(8), params.NumBlocksPerSession)
}

// TestAllTenSessionStates verifies all 10 states are defined
func (s *SessionLifecycleTestSuite) TestAllTenSessionStates() {
	allStates := []SessionState{
		SessionStateActive,
		SessionStateClaiming,
		SessionStateClaimed,
		SessionStateProving,
		SessionStateProved,
		SessionStateProbabilisticProved,
		SessionStateClaimWindowClosed,
		SessionStateClaimTxError,
		SessionStateProofWindowClosed,
		SessionStateProofTxError,
	}

	s.Len(allStates, 10, "should have exactly 10 states")

	nonTerminal := 0
	terminal := 0
	success := 0
	failure := 0

	for _, state := range allStates {
		if state.IsTerminal() {
			terminal++
			if state.IsSuccess() {
				success++
			}
			if state.IsFailure() {
				failure++
			}
		} else {
			nonTerminal++
		}
	}

	s.Equal(4, nonTerminal, "should have 4 non-terminal states")
	s.Equal(6, terminal, "should have 6 terminal states")
	s.Equal(2, success, "should have 2 success states")
	s.Equal(4, failure, "should have 4 failure states")
}

// TestEventDrivenBlockEvents tests Subscribe-based block handling
func (s *SessionLifecycleTestSuite) TestEventDrivenBlockEvents() {
	sessionEndHeight := int64(104)
	snapshot := &SessionSnapshot{
		SessionID:               "event-driven-test",
		SupplierOperatorAddress: slcTestSupplierAddr,
		ServiceID:               slcTestServiceID,
		ApplicationAddress:      slcTestAppAddr,
		SessionStartHeight:      sessionEndHeight - 4,
		SessionEndHeight:        sessionEndHeight,
		State:                   SessionStateActive,
	}

	s.sessionStore.setSession(snapshot)
	s.blockClient.setHeight(100)

	err := s.manager.Start(s.ctx)
	s.Require().NoError(err)

	s.NotNil(s.manager.GetSession(snapshot.SessionID))

	// Advance blocks to claim window open (106)
	for i := 0; i < 6; i++ {
		s.blockClient.advanceBlock()
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(200 * time.Millisecond)

	s.GreaterOrEqual(s.callback.getOnNeedClaimCount(), 1, "claim callback should be invoked")
}

// TestSetMeterCleanupPublisher tests meter cleanup publisher setup
func (s *SessionLifecycleTestSuite) TestSetMeterCleanupPublisher() {
	// Initialize shared params
	err := s.manager.refreshSharedParams(s.ctx)
	s.Require().NoError(err)

	var publishedSessions []string
	var mu sync.Mutex

	publisher := &slcMockMeterCleanupPublisher{
		publishFunc: func(ctx context.Context, sessionID string) error {
			mu.Lock()
			publishedSessions = append(publishedSessions, sessionID)
			mu.Unlock()
			return nil
		},
	}

	s.manager.SetMeterCleanupPublisher(publisher)

	sessionEndHeight := int64(104)
	snapshot := &SessionSnapshot{
		SessionID:               "meter-cleanup-test",
		SupplierOperatorAddress: slcTestSupplierAddr,
		ServiceID:               slcTestServiceID,
		ApplicationAddress:      slcTestAppAddr,
		SessionStartHeight:      sessionEndHeight - 4,
		SessionEndHeight:        sessionEndHeight,
		State:                   SessionStateActive,
	}

	s.sessionStore.setSession(snapshot)
	s.manager.activeSessions.Store(snapshot.SessionID, snapshot)

	claimWindowOpen := int64(106)
	s.manager.checkSessionTransitions(s.ctx, claimWindowOpen)

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	s.Contains(publishedSessions, snapshot.SessionID)
}

// slcMockMeterCleanupPublisher implements MeterCleanupPublisher for testing
type slcMockMeterCleanupPublisher struct {
	publishFunc func(ctx context.Context, sessionID string) error
}

func (m *slcMockMeterCleanupPublisher) PublishMeterCleanup(ctx context.Context, sessionID string) error {
	if m.publishFunc != nil {
		return m.publishFunc(ctx, sessionID)
	}
	return nil
}

// Ensure interface compliance
var _ SessionStore = (*slcMockSessionStore)(nil)
var _ SessionLifecycleCallback = (*slcMockCallback)(nil)
var _ MeterCleanupPublisher = (*slcMockMeterCleanupPublisher)(nil)

func TestSessionLifecycleSuite(t *testing.T) {
	suite.Run(t, new(SessionLifecycleTestSuite))
}
