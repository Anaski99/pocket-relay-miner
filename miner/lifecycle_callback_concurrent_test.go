//go:build test

package miner

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hashicorp/go-version"
	"github.com/stretchr/testify/suite"

	localclient "github.com/pokt-network/pocket-relay-miner/client"
	"github.com/pokt-network/pocket-relay-miner/logging"
	redisutil "github.com/pokt-network/pocket-relay-miner/transport/redis"
	pocktclient "github.com/pokt-network/poktroll/pkg/client"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
)

// getConcurrency returns the concurrency level for tests.
// Default: 100 for CI, can be increased via TEST_CONCURRENCY env var.
func getConcurrency() int {
	if envVal := os.Getenv("TEST_CONCURRENCY"); envVal != "" {
		if n, err := strconv.Atoi(envVal); err == nil && n > 0 {
			return n
		}
	}
	return 100 // Default for CI (500+ for nightly)
}

// LifecycleCallbackConcurrentSuite tests concurrent operations in LifecycleCallback.
// Uses miniredis for real Redis operations (Rule #1: no mocks).
// Validates no race conditions occur under high concurrency.
type LifecycleCallbackConcurrentSuite struct {
	suite.Suite

	// Miniredis instance shared across tests
	miniRedis   *miniredis.Miniredis
	redisClient *redisutil.Client
	ctx         context.Context

	// System under test
	callback *LifecycleCallback

	// Thread-safe mocks
	smstManager        *concTestSMSTManager
	txClient           *concTestTxClient
	sharedClient       *concTestSharedQueryClient
	blockClient        *concTestBlockClient
	sessionCoordinator *SessionCoordinator
	sessionStore       *RedisSessionStore
	deduplicator       *concTestDeduplicator
}

// SetupSuite initializes the shared miniredis instance.
func (s *LifecycleCallbackConcurrentSuite) SetupSuite() {
	mr, err := miniredis.Run()
	s.Require().NoError(err, "failed to create miniredis")
	s.miniRedis = mr

	s.ctx = context.Background()

	redisURL := fmt.Sprintf("redis://%s", mr.Addr())
	client, err := redisutil.NewClient(s.ctx, redisutil.ClientConfig{URL: redisURL})
	s.Require().NoError(err, "failed to create Redis client")
	s.redisClient = client
}

// SetupTest runs before each test, creating fresh mocks and callback.
func (s *LifecycleCallbackConcurrentSuite) SetupTest() {
	s.miniRedis.FlushAll()

	// Create thread-safe mocks
	s.smstManager = newConcTestSMSTManager()
	s.txClient = newConcTestTxClient()
	s.sharedClient = newConcTestSharedQueryClient()
	s.blockClient = newConcTestBlockClient(102) // Start at claim window open height

	// Create real session store with Redis
	logger := logging.NewLoggerFromConfig(logging.DefaultConfig())
	s.sessionStore = NewRedisSessionStore(logger, s.redisClient, SessionStoreConfig{
		KeyPrefix:       "conc:sessions",
		SupplierAddress: testSupplierAddr,
	})

	// Create session coordinator
	s.sessionCoordinator = NewSessionCoordinator(logger, s.sessionStore, SMSTRecoveryConfig{
		SupplierAddress: testSupplierAddr,
	})

	// Create deduplicator
	s.deduplicator = newConcTestDeduplicator()

	// Create lifecycle callback
	s.callback = NewLifecycleCallback(
		logger,
		s.txClient,
		s.sharedClient,
		s.blockClient,
		nil, // sessionClient - not needed for concurrent tests
		s.smstManager,
		s.sessionCoordinator,
		nil, // proofChecker
		LifecycleCallbackConfig{
			SupplierAddress:    testSupplierAddr,
			ClaimRetryAttempts: 1,
			ClaimRetryDelay:    0,
			ProofRetryAttempts: 1,
			ProofRetryDelay:    0,
		},
	)
	s.callback.SetDeduplicator(s.deduplicator)
}

// TearDownSuite cleans up the shared miniredis.
func (s *LifecycleCallbackConcurrentSuite) TearDownSuite() {
	if s.miniRedis != nil {
		s.miniRedis.Close()
	}
	if s.redisClient != nil {
		s.redisClient.Close()
	}
}

// newConcTestSnapshot creates a deterministic SessionSnapshot for concurrent testing.
func (s *LifecycleCallbackConcurrentSuite) newConcTestSnapshot(seed int) *SessionSnapshot {
	// Use fixed heights: session ends at 100, so:
	// - Claim window opens at: 102
	// - Claim window closes at: 106
	// - Proof window opens at: 106
	// - Proof window closes at: 110
	return &SessionSnapshot{
		SessionID:               fmt.Sprintf("conc-session-%08x", seed),
		SupplierOperatorAddress: testSupplierAddr,
		ServiceID:               testServiceID,
		ApplicationAddress:      testAppAddr,
		SessionStartHeight:      96,
		SessionEndHeight:        100,
		State:                   SessionStateActive,
		RelayCount:              int64(seed*10 + 50),
		TotalComputeUnits:       uint64((seed*10 + 50) * 100),
		CreatedAt:               time.Now(),
		LastUpdatedAt:           time.Now(),
	}
}

// =============================================================================
// Concurrent Claim Tests
// =============================================================================

func (s *LifecycleCallbackConcurrentSuite) TestConcurrentClaimSubmissions() {
	numGoroutines := getConcurrency()
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			// Create unique snapshot for each goroutine
			snapshot := s.newConcTestSnapshot(idx)

			// Save to Redis
			if err := s.sessionStore.Save(s.ctx, snapshot); err != nil {
				errors <- fmt.Errorf("goroutine %d: save error: %w", idx, err)
				return
			}

			// Execute claim
			_, err := s.callback.OnSessionsNeedClaim(s.ctx, []*SessionSnapshot{snapshot})
			if err != nil {
				errors <- fmt.Errorf("goroutine %d: claim error: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Collect any errors
	var errList []error
	for err := range errors {
		errList = append(errList, err)
	}

	// Verify no errors occurred
	s.Require().Empty(errList, "concurrent claim submissions should not fail: %v", errList)

	// Verify all claims were processed
	s.Require().GreaterOrEqual(s.smstManager.getFlushCalls(), numGoroutines,
		"expected at least %d flush calls", numGoroutines)
}

func (s *LifecycleCallbackConcurrentSuite) TestClaimDuringProof() {
	// Test claim and proof operations interleaved - using different session heights
	// so they can both work at the same block height.
	// Claims use session end 100 (claim window 102-106)
	// Proofs use session end 96 (proof window 102-106) - so both work at height 102
	numGoroutines := getConcurrency() / 2 // Half for claims, half for proofs
	var wg sync.WaitGroup
	wg.Add(numGoroutines * 2)

	errors := make(chan error, numGoroutines*2)

	// Launch claim goroutines
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			snapshot := s.newConcTestSnapshot(idx)
			// Session ends at 100, claim window 102-106
			if err := s.sessionStore.Save(s.ctx, snapshot); err != nil {
				errors <- fmt.Errorf("claim goroutine %d: save error: %w", idx, err)
				return
			}

			_, err := s.callback.OnSessionsNeedClaim(s.ctx, []*SessionSnapshot{snapshot})
			if err != nil {
				errors <- fmt.Errorf("claim goroutine %d: error: %w", idx, err)
			}
		}(i)
	}

	// Launch proof goroutines (with different seed range and session heights)
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			// Use different seed and session end height for proofs
			// Session ends at 96, so proof window opens at 102 (same as claim window)
			snapshot := s.newConcTestSnapshot(1000 + idx)
			snapshot.SessionStartHeight = 92
			snapshot.SessionEndHeight = 96 // Earlier session - proof window at 102
			snapshot.State = SessionStateClaimed
			snapshot.ClaimedRootHash = []byte(fmt.Sprintf("claimed-root-%d", idx))

			if err := s.sessionStore.Save(s.ctx, snapshot); err != nil {
				errors <- fmt.Errorf("proof goroutine %d: save error: %w", idx, err)
				return
			}

			err := s.callback.OnSessionsNeedProof(s.ctx, []*SessionSnapshot{snapshot})
			if err != nil {
				errors <- fmt.Errorf("proof goroutine %d: error: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	var errList []error
	for err := range errors {
		errList = append(errList, err)
	}

	s.Require().Empty(errList, "interleaved claim/proof should not fail: %v", errList)
}

func (s *LifecycleCallbackConcurrentSuite) TestConcurrentSameSession() {
	// Test multiple goroutines trying to claim the same session
	// This tests that concurrent access to the same session doesn't cause races.
	numGoroutines := getConcurrency()
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Create single shared session
	sharedSnapshot := s.newConcTestSnapshot(9999)
	err := s.sessionStore.Save(s.ctx, sharedSnapshot)
	s.Require().NoError(err)

	// Use shared mutex for thread-safe success counting
	var mu sync.Mutex
	var successCount int64

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			// Create a copy of the snapshot for each goroutine
			// (avoid sharing mutable state)
			localSnapshot := &SessionSnapshot{
				SessionID:               sharedSnapshot.SessionID,
				SupplierOperatorAddress: sharedSnapshot.SupplierOperatorAddress,
				ServiceID:               sharedSnapshot.ServiceID,
				ApplicationAddress:      sharedSnapshot.ApplicationAddress,
				SessionStartHeight:      sharedSnapshot.SessionStartHeight,
				SessionEndHeight:        sharedSnapshot.SessionEndHeight,
				State:                   sharedSnapshot.State,
				RelayCount:              sharedSnapshot.RelayCount,
				TotalComputeUnits:       sharedSnapshot.TotalComputeUnits,
				CreatedAt:               sharedSnapshot.CreatedAt,
				LastUpdatedAt:           sharedSnapshot.LastUpdatedAt,
			}

			// All goroutines try to claim the same session
			_, claimErr := s.callback.OnSessionsNeedClaim(s.ctx, []*SessionSnapshot{localSnapshot})
			if claimErr == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
			// Errors are expected for some goroutines due to contention
		}(i)
	}

	wg.Wait()

	// At least one should succeed, but due to deduplication after first claim,
	// subsequent attempts may be filtered out
	s.Require().GreaterOrEqual(successCount, int64(1),
		"at least one claim should succeed")
}

// =============================================================================
// Concurrent Proof Tests
// =============================================================================

func (s *LifecycleCallbackConcurrentSuite) TestConcurrentProofSubmissions() {
	numGoroutines := getConcurrency()
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Use earlier session end heights so proof window opens at current block (102)
	// Session ends at 96, proof window opens at 102
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			snapshot := s.newConcTestSnapshot(2000 + idx)
			snapshot.SessionStartHeight = 92
			snapshot.SessionEndHeight = 96 // Proof window opens at 102
			snapshot.State = SessionStateClaimed
			snapshot.ClaimedRootHash = []byte(fmt.Sprintf("claimed-root-%d", idx))

			if err := s.sessionStore.Save(s.ctx, snapshot); err != nil {
				errors <- fmt.Errorf("goroutine %d: save error: %w", idx, err)
				return
			}

			err := s.callback.OnSessionsNeedProof(s.ctx, []*SessionSnapshot{snapshot})
			if err != nil {
				errors <- fmt.Errorf("goroutine %d: proof error: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	var errList []error
	for err := range errors {
		errList = append(errList, err)
	}

	s.Require().Empty(errList, "concurrent proof submissions should not fail: %v", errList)

	s.Require().GreaterOrEqual(s.smstManager.getProveCalls(), numGoroutines,
		"expected at least %d prove calls", numGoroutines)
}

func (s *LifecycleCallbackConcurrentSuite) TestProofDuringCleanup() {
	// Test proof generation while terminal callbacks are running
	numGoroutines := getConcurrency() / 2
	var wg sync.WaitGroup
	wg.Add(numGoroutines * 2)

	errors := make(chan error, numGoroutines*2)

	// Launch proof goroutines with earlier session end height
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			snapshot := s.newConcTestSnapshot(3000 + idx)
			snapshot.SessionStartHeight = 92
			snapshot.SessionEndHeight = 96 // Proof window opens at 102
			snapshot.State = SessionStateClaimed
			snapshot.ClaimedRootHash = []byte(fmt.Sprintf("root-%d", idx))

			if err := s.sessionStore.Save(s.ctx, snapshot); err != nil {
				errors <- fmt.Errorf("proof goroutine %d: save error: %w", idx, err)
				return
			}

			err := s.callback.OnSessionsNeedProof(s.ctx, []*SessionSnapshot{snapshot})
			if err != nil {
				errors <- fmt.Errorf("proof goroutine %d: error: %w", idx, err)
			}
		}(i)
	}

	// Launch cleanup goroutines
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			// Different seed range
			snapshot := s.newConcTestSnapshot(4000 + idx)
			snapshot.State = SessionStateProved

			err := s.callback.OnSessionProved(s.ctx, snapshot)
			if err != nil {
				errors <- fmt.Errorf("cleanup goroutine %d: error: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	var errList []error
	for err := range errors {
		errList = append(errList, err)
	}

	s.Require().Empty(errList, "proof during cleanup should not fail: %v", errList)
}

// =============================================================================
// Race Condition Tests
// =============================================================================

func (s *LifecycleCallbackConcurrentSuite) TestSessionLockContention() {
	// Test multiple goroutines accessing the same session coordinator
	numGoroutines := getConcurrency()
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Create multiple sessions
	for i := 0; i < numGoroutines; i++ {
		snapshot := s.newConcTestSnapshot(5000 + i)
		err := s.sessionStore.Save(s.ctx, snapshot)
		s.Require().NoError(err)
	}

	errors := make(chan error, numGoroutines)

	// All goroutines try to update sessions via coordinator
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			sessionID := fmt.Sprintf("conc-session-%08x", 5000+idx)

			// Simulate claim completion update
			if err := s.sessionCoordinator.OnSessionClaimed(s.ctx, sessionID, []byte("root-hash"), "tx-hash"); err != nil {
				errors <- fmt.Errorf("goroutine %d: coordinator error: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	var errList []error
	for err := range errors {
		errList = append(errList, err)
	}

	s.Require().Empty(errList, "session lock contention should not cause errors: %v", errList)
}

func (s *LifecycleCallbackConcurrentSuite) TestConcurrentCallbacksNoRace() {
	// Run all callback types concurrently to verify no races
	numPerType := getConcurrency() / 5 // Divide among 5 callback types
	var wg sync.WaitGroup

	errors := make(chan error, numPerType*5)

	// 1. Claim callbacks (session end 100, claim window 102-106)
	wg.Add(numPerType)
	for i := 0; i < numPerType; i++ {
		go func(idx int) {
			defer wg.Done()
			snapshot := s.newConcTestSnapshot(6000 + idx)
			if err := s.sessionStore.Save(s.ctx, snapshot); err != nil {
				errors <- err
				return
			}
			if _, err := s.callback.OnSessionsNeedClaim(s.ctx, []*SessionSnapshot{snapshot}); err != nil {
				errors <- err
			}
		}(i)
	}

	// 2. Proof callbacks (session end 96, proof window 102-106)
	wg.Add(numPerType)
	for i := 0; i < numPerType; i++ {
		go func(idx int) {
			defer wg.Done()
			snapshot := s.newConcTestSnapshot(7000 + idx)
			snapshot.SessionStartHeight = 92
			snapshot.SessionEndHeight = 96 // Proof window at 102
			snapshot.State = SessionStateClaimed
			snapshot.ClaimedRootHash = []byte("root")
			if err := s.sessionStore.Save(s.ctx, snapshot); err != nil {
				errors <- err
				return
			}
			if err := s.callback.OnSessionsNeedProof(s.ctx, []*SessionSnapshot{snapshot}); err != nil {
				errors <- err
			}
		}(i)
	}

	// 3. OnSessionProved callbacks
	wg.Add(numPerType)
	for i := 0; i < numPerType; i++ {
		go func(idx int) {
			defer wg.Done()
			snapshot := s.newConcTestSnapshot(8000 + idx)
			snapshot.State = SessionStateProved
			if err := s.callback.OnSessionProved(s.ctx, snapshot); err != nil {
				errors <- err
			}
		}(i)
	}

	// 4. OnClaimWindowClosed callbacks
	wg.Add(numPerType)
	for i := 0; i < numPerType; i++ {
		go func(idx int) {
			defer wg.Done()
			snapshot := s.newConcTestSnapshot(9000 + idx)
			snapshot.State = SessionStateClaimWindowClosed
			if err := s.callback.OnClaimWindowClosed(s.ctx, snapshot); err != nil {
				errors <- err
			}
		}(i)
	}

	// 5. OnProofWindowClosed callbacks
	wg.Add(numPerType)
	for i := 0; i < numPerType; i++ {
		go func(idx int) {
			defer wg.Done()
			snapshot := s.newConcTestSnapshot(10000 + idx)
			snapshot.State = SessionStateProofWindowClosed
			if err := s.callback.OnProofWindowClosed(s.ctx, snapshot); err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	var errList []error
	for err := range errors {
		errList = append(errList, err)
	}

	s.Require().Empty(errList, "concurrent callbacks should not fail: %v", errList)
}

func (s *LifecycleCallbackConcurrentSuite) TestCleanupDuringActiveOperation() {
	// Test terminal cleanup called while claim/proof in progress
	numGoroutines := getConcurrency() / 2
	var wg sync.WaitGroup
	wg.Add(numGoroutines * 2)

	errors := make(chan error, numGoroutines*2)

	// Create sessions that will be used for both active ops and cleanup
	for i := 0; i < numGoroutines; i++ {
		snapshot := s.newConcTestSnapshot(11000 + i)
		snapshot.SessionStartHeight = 92
		snapshot.SessionEndHeight = 96 // Proof window at 102
		snapshot.State = SessionStateClaimed
		snapshot.ClaimedRootHash = []byte("root")
		_ = s.sessionStore.Save(s.ctx, snapshot)
	}

	// Launch active operations (proofs)
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			snapshot := s.newConcTestSnapshot(11000 + idx)
			snapshot.SessionStartHeight = 92
			snapshot.SessionEndHeight = 96 // Proof window at 102
			snapshot.State = SessionStateClaimed
			snapshot.ClaimedRootHash = []byte("root")

			err := s.callback.OnSessionsNeedProof(s.ctx, []*SessionSnapshot{snapshot})
			// Errors may occur if cleanup wins the race - that's OK
			_ = err
		}(i)
	}

	// Launch cleanup operations
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			// Same session ID - compete for the same resource
			snapshot := s.newConcTestSnapshot(11000 + idx)
			snapshot.State = SessionStateProved

			err := s.callback.OnSessionProved(s.ctx, snapshot)
			// Errors may occur if proof wins the race - that's OK
			_ = err
		}(i)
	}

	wg.Wait()
	close(errors)

	// No panics, no race conditions detected - test passes
	// Some operations may fail due to race, but no crashes
}

// =============================================================================
// Thread-safe Mock Implementations
// =============================================================================

// concTestSMSTManager implements SMSTManager with thread-safe counters.
type concTestSMSTManager struct {
	mu          sync.Mutex
	flushCalls  int
	proveCalls  int
	deleteCalls int
}

func newConcTestSMSTManager() *concTestSMSTManager {
	return &concTestSMSTManager{}
}

func (m *concTestSMSTManager) FlushTree(ctx context.Context, sessionID string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushCalls++
	return []byte("mock-root-hash-" + sessionID), nil
}

func (m *concTestSMSTManager) GetTreeRoot(ctx context.Context, sessionID string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return []byte("mock-root-hash-" + sessionID), nil
}

func (m *concTestSMSTManager) ProveClosest(ctx context.Context, sessionID string, path []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.proveCalls++
	return []byte("mock-proof-" + sessionID), nil
}

func (m *concTestSMSTManager) DeleteTree(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteCalls++
	return nil
}

func (m *concTestSMSTManager) getFlushCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.flushCalls
}

func (m *concTestSMSTManager) getProveCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.proveCalls
}

// concTestTxClient implements pocktclient.SupplierClient with thread-safe counters.
type concTestTxClient struct {
	mu         sync.Mutex
	claimCalls int
	proofCalls int
}

func newConcTestTxClient() *concTestTxClient {
	return &concTestTxClient{}
}

func (c *concTestTxClient) CreateClaims(ctx context.Context, timeoutHeight int64, claims ...pocktclient.MsgCreateClaim) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.claimCalls++
	return nil
}

func (c *concTestTxClient) SubmitProofs(ctx context.Context, timeoutHeight int64, proofs ...pocktclient.MsgSubmitProof) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.proofCalls++
	return nil
}

func (c *concTestTxClient) OperatorAddress() string {
	return testSupplierAddr
}

// concTestSharedQueryClient implements SharedQueryClient.
type concTestSharedQueryClient struct {
	params *sharedtypes.Params
}

func newConcTestSharedQueryClient() *concTestSharedQueryClient {
	return &concTestSharedQueryClient{
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
}

func (c *concTestSharedQueryClient) GetParams(ctx context.Context) (*sharedtypes.Params, error) {
	return c.params, nil
}

func (c *concTestSharedQueryClient) GetSessionGracePeriodEndHeight(ctx context.Context, queryHeight int64) (int64, error) {
	return sharedtypes.GetSessionGracePeriodEndHeight(c.params, queryHeight), nil
}

func (c *concTestSharedQueryClient) GetClaimWindowOpenHeight(ctx context.Context, queryHeight int64) (int64, error) {
	return sharedtypes.GetClaimWindowOpenHeight(c.params, queryHeight), nil
}

func (c *concTestSharedQueryClient) GetEarliestSupplierClaimCommitHeight(ctx context.Context, queryHeight int64, supplierAddr string) (int64, error) {
	return queryHeight + 1, nil
}

func (c *concTestSharedQueryClient) GetProofWindowOpenHeight(ctx context.Context, queryHeight int64) (int64, error) {
	return sharedtypes.GetProofWindowOpenHeight(c.params, queryHeight), nil
}

func (c *concTestSharedQueryClient) GetEarliestSupplierProofCommitHeight(ctx context.Context, queryHeight int64, supplierAddr string) (int64, error) {
	return queryHeight + 1, nil
}

// concTestBlockClient implements BlockClient with thread-safe height.
type concTestBlockClient struct {
	mu            sync.RWMutex
	currentHeight int64
	blockHash     []byte
	subscribers   []chan *localclient.SimpleBlock
}

func newConcTestBlockClient(height int64) *concTestBlockClient {
	return &concTestBlockClient{
		currentHeight: height,
		blockHash:     []byte("test-block-hash"),
	}
}

func (c *concTestBlockClient) LastBlock(ctx context.Context) pocktclient.Block {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return &concTestBlock{
		height: c.currentHeight,
		hash:   c.blockHash,
	}
}

func (c *concTestBlockClient) CommittedBlocksSequence(ctx context.Context) pocktclient.BlockReplayObservable {
	return nil
}

func (c *concTestBlockClient) Close() {}

func (c *concTestBlockClient) GetChainVersion() *version.Version {
	v, _ := version.NewVersion("0.1.0")
	return v
}

func (c *concTestBlockClient) Subscribe(ctx context.Context, bufferSize int) <-chan *localclient.SimpleBlock {
	c.mu.Lock()
	defer c.mu.Unlock()

	ch := make(chan *localclient.SimpleBlock, bufferSize)
	c.subscribers = append(c.subscribers, ch)

	go func() {
		c.mu.RLock()
		block := localclient.NewSimpleBlock(c.currentHeight, c.blockHash, time.Now())
		c.mu.RUnlock()
		ch <- block
	}()

	return ch
}

func (c *concTestBlockClient) GetBlockAtHeight(ctx context.Context, height int64) (pocktclient.Block, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return &concTestBlock{
		height: height,
		hash:   c.blockHash,
	}, nil
}

func (c *concTestBlockClient) setHeight(height int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentHeight = height

	for _, ch := range c.subscribers {
		select {
		case ch <- localclient.NewSimpleBlock(c.currentHeight, c.blockHash, time.Now()):
		default:
		}
	}
}

type concTestBlock struct {
	height int64
	hash   []byte
}

func (b *concTestBlock) Height() int64 {
	return b.height
}

func (b *concTestBlock) Hash() []byte {
	if b.hash == nil {
		return []byte("mock-block-hash")
	}
	return b.hash
}

// concTestDeduplicator implements Deduplicator with thread-safe tracking.
type concTestDeduplicator struct {
	mu              sync.Mutex
	cleanedSessions []string
}

func newConcTestDeduplicator() *concTestDeduplicator {
	return &concTestDeduplicator{}
}

func (d *concTestDeduplicator) IsDuplicate(ctx context.Context, hash []byte, sessionID string) (bool, error) {
	return false, nil
}

func (d *concTestDeduplicator) MarkProcessed(ctx context.Context, hash []byte, sessionID string) error {
	return nil
}

func (d *concTestDeduplicator) MarkProcessedBatch(ctx context.Context, hashes [][]byte, sessionID string) error {
	return nil
}

func (d *concTestDeduplicator) CleanupSession(ctx context.Context, sessionID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cleanedSessions = append(d.cleanedSessions, sessionID)
	return nil
}

func (d *concTestDeduplicator) Start(ctx context.Context) error {
	return nil
}

func (d *concTestDeduplicator) Close() error {
	return nil
}

// =============================================================================
// Test Runner
// =============================================================================

func TestLifecycleCallbackConcurrentSuite(t *testing.T) {
	suite.Run(t, new(LifecycleCallbackConcurrentSuite))
}
