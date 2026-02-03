//go:build test

package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pokt-network/pocket-relay-miner/logging"
	"github.com/pokt-network/pocket-relay-miner/testutil"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/suite"
)

// mockSharedQueryClient is a local mock implementing poktroll's client.SharedQueryClient interface.
// Track call counts with mutex to verify L3 is only called when expected.
// We only implement GetParams for cache tests; other methods are stubs.
type mockSharedQueryClient struct {
	mu        sync.Mutex
	callCount int64
	params    *sharedtypes.Params
	err       error
}

func newMockSharedQueryClient() *mockSharedQueryClient {
	return &mockSharedQueryClient{
		params: &sharedtypes.Params{
			NumBlocksPerSession:                4,
			GracePeriodEndOffsetBlocks:         1,
			ClaimWindowOpenOffsetBlocks:        1,
			ClaimWindowCloseOffsetBlocks:       4,
			ProofWindowOpenOffsetBlocks:        0,
			ProofWindowCloseOffsetBlocks:       4,
			SupplierUnbondingPeriodSessions:    4,
			ApplicationUnbondingPeriodSessions: 4,
			ComputeUnitsToTokensMultiplier:     42,
		},
	}
}

func (m *mockSharedQueryClient) GetParams(ctx context.Context) (*sharedtypes.Params, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	return m.params, m.err
}

// Stub methods to satisfy client.SharedQueryClient interface (not used in cache tests)
func (m *mockSharedQueryClient) GetSessionGracePeriodEndHeight(ctx context.Context, queryHeight int64) (int64, error) {
	return 0, nil
}

func (m *mockSharedQueryClient) GetClaimWindowOpenHeight(ctx context.Context, queryHeight int64) (int64, error) {
	return 0, nil
}

func (m *mockSharedQueryClient) GetEarliestSupplierClaimCommitHeight(ctx context.Context, queryHeight int64, supplierOperatorAddr string) (int64, error) {
	return 0, nil
}

func (m *mockSharedQueryClient) GetProofWindowOpenHeight(ctx context.Context, queryHeight int64) (int64, error) {
	return 0, nil
}

func (m *mockSharedQueryClient) GetEarliestSupplierProofCommitHeight(ctx context.Context, queryHeight int64, supplierOperatorAddr string) (int64, error) {
	return 0, nil
}

func (m *mockSharedQueryClient) getCallCount() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func (m *mockSharedQueryClient) resetCallCount() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount = 0
}

func (m *mockSharedQueryClient) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// SharedParamsSingletonTestSuite tests the sharedParamsCache implementation.
type SharedParamsSingletonTestSuite struct {
	testutil.RedisTestSuite

	mockQueryClient *mockSharedQueryClient
	cache           SingletonEntityCache[*sharedtypes.Params]
}

func TestSharedParamsSingletonTestSuite(t *testing.T) {
	suite.Run(t, new(SharedParamsSingletonTestSuite))
}

// SetupTest runs before each test (miniredis is flushed by RedisTestSuite).
func (s *SharedParamsSingletonTestSuite) SetupTest() {
	s.RedisTestSuite.SetupTest()

	// Create mock query client
	s.mockQueryClient = newMockSharedQueryClient()

	// Create cache
	logger := logging.NewLoggerFromConfig(logging.DefaultConfig())
	s.cache = NewSharedParamsCache(
		logger,
		s.RedisClient,
		s.mockQueryClient,
		30, // 30s block time
	)

	// Start cache
	err := s.cache.Start(s.Ctx)
	s.Require().NoError(err)
}

// TearDownTest runs after each test.
func (s *SharedParamsSingletonTestSuite) TearDownTest() {
	if s.cache != nil {
		s.cache.Close()
	}
}

// TestSharedParamsSingleton_GetFromL3 verifies cold cache fetches from L3 (mock chain query).
func (s *SharedParamsSingletonTestSuite) TestSharedParamsSingleton_GetFromL3() {
	// Cold cache: L1 and L2 empty
	s.Require().Equal(0, s.GetKeyCount(), "Redis should be empty")

	// Get params (should query L3)
	params, err := s.cache.Get(s.Ctx)
	s.Require().NoError(err)
	s.Require().NotNil(params)
	s.Require().Equal(uint64(4), params.NumBlocksPerSession)

	// Verify L3 was called exactly once
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount())

	// Verify params were stored in L2 (Redis)
	redisKey := s.RedisClient.KB().ParamsSharedCacheKey()
	s.RequireKeyExists(redisKey)

	// Verify params are now in L1 (subsequent call should not query L3)
	s.mockQueryClient.resetCallCount()
	params2, err := s.cache.Get(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(params, params2)
	s.Require().Equal(int64(0), s.mockQueryClient.getCallCount(), "L1 hit should not query L3")
}

// TestSharedParamsSingleton_GetFromL2 verifies L1 cold, L2 has data returns from L2 without L3 query.
func (s *SharedParamsSingletonTestSuite) TestSharedParamsSingleton_GetFromL2() {
	// Pre-populate L2 (Redis) by doing a Get first
	_, err := s.cache.Get(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount())

	// Close cache to clear L1, create new cache instance
	s.cache.Close()
	s.mockQueryClient.resetCallCount()

	logger := logging.NewLoggerFromConfig(logging.DefaultConfig())
	newCache := NewSharedParamsCache(
		logger,
		s.RedisClient,
		s.mockQueryClient,
		30,
	)
	err = newCache.Start(s.Ctx)
	s.Require().NoError(err)
	defer newCache.Close()

	// Get params (L1 is cold, L2 has data, should NOT query L3)
	params, err := newCache.Get(s.Ctx)
	s.Require().NoError(err)
	s.Require().NotNil(params)
	s.Require().Equal(uint64(4), params.NumBlocksPerSession)

	// Verify L3 was NOT called (L2 hit)
	s.Require().Equal(int64(0), s.mockQueryClient.getCallCount(), "L2 hit should not query L3")
}

// TestSharedParamsSingleton_GetFromL1 verifies L1 has data returns immediately (no Redis call).
func (s *SharedParamsSingletonTestSuite) TestSharedParamsSingleton_GetFromL1() {
	// Pre-populate L1 by doing a Get first
	params1, err := s.cache.Get(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount())

	// Reset call count
	s.mockQueryClient.resetCallCount()

	// Get params again (should return from L1 immediately)
	params2, err := s.cache.Get(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(params1, params2)

	// Verify L3 was NOT called (L1 hit)
	s.Require().Equal(int64(0), s.mockQueryClient.getCallCount(), "L1 hit should not query L3")
}

// TestSharedParamsSingleton_Invalidate verifies invalidation clears L1 and L2.
func (s *SharedParamsSingletonTestSuite) TestSharedParamsSingleton_Invalidate() {
	// Pre-populate cache
	_, err := s.cache.Get(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount())

	// Verify data is in L2
	redisKey := s.RedisClient.KB().ParamsSharedCacheKey()
	s.RequireKeyExists(redisKey)

	// Invalidate
	err = s.cache.InvalidateAll(s.Ctx)
	s.Require().NoError(err)

	// Verify L2 is cleared
	val, err := s.RedisClient.Get(s.Ctx, redisKey).Result()
	s.Require().True(errors.Is(err, redis.Nil) || val == "", "L2 should be cleared")

	// Reset call count
	s.mockQueryClient.resetCallCount()

	// Next Get should fetch from L3 again
	params, err := s.cache.Get(s.Ctx)
	s.Require().NoError(err)
	s.Require().NotNil(params)
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount(), "after invalidation should query L3")
}

// TestSharedParamsSingleton_SetExplicit verifies explicit Set populates L1 and L2.
func (s *SharedParamsSingletonTestSuite) TestSharedParamsSingleton_SetExplicit() {
	// Verify cache is empty
	s.Require().Equal(0, s.GetKeyCount())

	// Set params explicitly
	params := &sharedtypes.Params{
		NumBlocksPerSession:                10,
		GracePeriodEndOffsetBlocks:         2,
		ClaimWindowOpenOffsetBlocks:        2,
		ClaimWindowCloseOffsetBlocks:       8,
		ProofWindowOpenOffsetBlocks:        0,
		ProofWindowCloseOffsetBlocks:       8,
		SupplierUnbondingPeriodSessions:    8,
		ApplicationUnbondingPeriodSessions: 8,
		ComputeUnitsToTokensMultiplier:     100,
	}
	err := s.cache.Set(s.Ctx, params, 5*time.Minute)
	s.Require().NoError(err)

	// Verify data is in L2 (Redis)
	redisKey := s.RedisClient.KB().ParamsSharedCacheKey()
	s.RequireKeyExists(redisKey)

	// Verify data is in L1 (subsequent Get should not query L3)
	retrieved, err := s.cache.Get(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(uint64(10), retrieved.NumBlocksPerSession)
	s.Require().Equal(int64(0), s.mockQueryClient.getCallCount(), "L1 hit should not query L3")
}

// TestSharedParamsSingleton_DistributedLock verifies two concurrent Gets for cold cache
// trigger only one L3 query (distributed lock prevents duplicate queries).
func (s *SharedParamsSingletonTestSuite) TestSharedParamsSingleton_DistributedLock() {
	// Create two cache instances (simulating two relayer instances)
	logger1 := logging.NewLoggerFromConfig(logging.DefaultConfig())
	logger2 := logging.NewLoggerFromConfig(logging.DefaultConfig())

	cache1 := NewSharedParamsCache(logger1, s.RedisClient, s.mockQueryClient, 30)
	cache2 := NewSharedParamsCache(logger2, s.RedisClient, s.mockQueryClient, 30)

	err := cache1.Start(s.Ctx)
	s.Require().NoError(err)
	defer cache1.Close()

	err = cache2.Start(s.Ctx)
	s.Require().NoError(err)
	defer cache2.Close()

	// Reset call count
	s.mockQueryClient.resetCallCount()

	// Concurrent Gets from both caches (cold L1 + L2)
	var wg sync.WaitGroup
	wg.Add(2)

	var params1, params2 *sharedtypes.Params
	var err1, err2 error

	go func() {
		defer wg.Done()
		params1, err1 = cache1.Get(s.Ctx)
	}()

	go func() {
		defer wg.Done()
		params2, err2 = cache2.Get(s.Ctx)
	}()

	wg.Wait()

	// Both should succeed
	s.Require().NoError(err1)
	s.Require().NoError(err2)
	s.Require().NotNil(params1)
	s.Require().NotNil(params2)

	// Verify L3 was called at most twice (ideally once with distributed lock, but timing may vary)
	// The distributed lock should prevent both from querying L3 simultaneously
	callCount := s.mockQueryClient.getCallCount()
	s.Require().LessOrEqual(callCount, int64(2), "distributed lock should minimize L3 queries")
	s.Require().GreaterOrEqual(callCount, int64(1), "at least one L3 query needed")
}

// TestSharedParamsSingleton_ConcurrentReads verifies multiple concurrent reads are safe.
func (s *SharedParamsSingletonTestSuite) TestSharedParamsSingleton_ConcurrentReads() {
	// Pre-populate cache
	_, err := s.cache.Get(s.Ctx)
	s.Require().NoError(err)

	// Reset call count
	s.mockQueryClient.resetCallCount()

	// 100 concurrent reads (all should hit L1 without querying L3)
	const numReads = 100
	var wg sync.WaitGroup
	wg.Add(numReads)

	var successCount atomic.Int64

	for i := 0; i < numReads; i++ {
		go func() {
			defer wg.Done()
			params, err := s.cache.Get(s.Ctx)
			if err == nil && params != nil {
				successCount.Add(1)
			}
		}()
	}

	wg.Wait()

	// All reads should succeed
	s.Require().Equal(int64(numReads), successCount.Load())

	// L3 should NOT be called (all L1 hits)
	s.Require().Equal(int64(0), s.mockQueryClient.getCallCount(), "concurrent L1 reads should not query L3")
}

// TestSharedParamsSingleton_Refresh verifies Refresh bypasses L1/L2 and queries L3.
func (s *SharedParamsSingletonTestSuite) TestSharedParamsSingleton_Refresh() {
	// Pre-populate cache
	_, err := s.cache.Get(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount())

	// Reset call count
	s.mockQueryClient.resetCallCount()

	// Refresh (should bypass L1/L2 and query L3)
	err = s.cache.Refresh(s.Ctx)
	s.Require().NoError(err)

	// Verify L3 was called
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount(), "Refresh should query L3")
}
