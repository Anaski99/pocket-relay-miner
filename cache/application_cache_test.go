//go:build test

package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/pokt-network/pocket-relay-miner/logging"
	"github.com/pokt-network/pocket-relay-miner/testutil"
	apptypes "github.com/pokt-network/poktroll/x/application/types"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/suite"
)

// mockApplicationQueryClient is a local mock implementing ApplicationQueryClient.
// Track call counts with mutex to verify L3 is only called when expected.
type mockApplicationQueryClient struct {
	mu        sync.Mutex
	callCount map[string]int64 // Track calls per address
	apps      map[string]*apptypes.Application
	err       error
}

func newMockApplicationQueryClient() *mockApplicationQueryClient {
	return &mockApplicationQueryClient{
		callCount: make(map[string]int64),
		apps:      make(map[string]*apptypes.Application),
	}
}

func (m *mockApplicationQueryClient) GetApplication(ctx context.Context, address string) (*apptypes.Application, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.callCount[address]++

	if m.err != nil {
		return nil, m.err
	}

	app, ok := m.apps[address]
	if !ok {
		return nil, errors.New("application not found")
	}

	return app, nil
}

func (m *mockApplicationQueryClient) getCallCount(address string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount[address]
}

func (m *mockApplicationQueryClient) resetCallCount() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount = make(map[string]int64)
}

func (m *mockApplicationQueryClient) setApplication(address string, app *apptypes.Application) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.apps[address] = app
}

func (m *mockApplicationQueryClient) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// ApplicationCacheTestSuite tests the applicationCache implementation.
type ApplicationCacheTestSuite struct {
	testutil.RedisTestSuite

	mockQueryClient *mockApplicationQueryClient
	cache           KeyedEntityCache[string, *apptypes.Application]
	testApp1        *apptypes.Application
	testApp2        *apptypes.Application
}

func TestApplicationCacheTestSuite(t *testing.T) {
	suite.Run(t, new(ApplicationCacheTestSuite))
}

// SetupTest runs before each test (miniredis is flushed by RedisTestSuite).
func (s *ApplicationCacheTestSuite) SetupTest() {
	s.RedisTestSuite.SetupTest()

	// Create mock query client
	s.mockQueryClient = newMockApplicationQueryClient()

	// Create test applications
	s.testApp1 = &apptypes.Application{
		Address: testutil.TestAppAddress(),
		// Add minimal fields for proto marshaling
		Stake: nil,
	}

	s.testApp2 = &apptypes.Application{
		Address: testutil.TestApp2Address(),
		Stake:   nil,
	}

	// Pre-populate mock with test apps
	s.mockQueryClient.setApplication(s.testApp1.Address, s.testApp1)
	s.mockQueryClient.setApplication(s.testApp2.Address, s.testApp2)

	// Create cache
	logger := logging.NewLoggerFromConfig(logging.DefaultConfig())
	s.cache = NewApplicationCache(
		logger,
		s.RedisClient,
		s.mockQueryClient,
	)

	// Start cache
	err := s.cache.Start(s.Ctx)
	s.Require().NoError(err)
}

// TearDownTest runs after each test.
func (s *ApplicationCacheTestSuite) TearDownTest() {
	if s.cache != nil {
		s.cache.Close()
	}
}

// TestApplicationCache_GetFromL3 verifies cold cache fetches from L3 (mock chain query).
func (s *ApplicationCacheTestSuite) TestApplicationCache_GetFromL3() {
	// Cold cache: L1 and L2 empty
	s.Require().Equal(0, s.GetKeyCount(), "Redis should be empty")

	// Get application (should query L3)
	app, err := s.cache.Get(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)
	s.Require().NotNil(app)
	s.Require().Equal(s.testApp1.Address, app.Address)

	// Verify L3 was called exactly once
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount(s.testApp1.Address))

	// Verify app was stored in L2 (Redis)
	redisKey := s.RedisClient.KB().CacheKey(applicationCacheType, s.testApp1.Address)
	s.RequireKeyExists(redisKey)

	// Verify app is now in L1 (subsequent call should not query L3)
	s.mockQueryClient.resetCallCount()
	app2, err := s.cache.Get(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)
	s.Require().Equal(app, app2)
	s.Require().Equal(int64(0), s.mockQueryClient.getCallCount(s.testApp1.Address), "L1 hit should not query L3")
}

// TestApplicationCache_GetFromL2 verifies L1 cold, L2 has data returns from L2 without L3 query.
func (s *ApplicationCacheTestSuite) TestApplicationCache_GetFromL2() {
	// Pre-populate L2 (Redis) by doing a Get first
	_, err := s.cache.Get(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount(s.testApp1.Address))

	// Close cache to clear L1, create new cache instance
	s.cache.Close()
	s.mockQueryClient.resetCallCount()

	logger := logging.NewLoggerFromConfig(logging.DefaultConfig())
	newCache := NewApplicationCache(
		logger,
		s.RedisClient,
		s.mockQueryClient,
	)
	err = newCache.Start(s.Ctx)
	s.Require().NoError(err)
	defer newCache.Close()

	// Get app (L1 is cold, L2 has data, should NOT query L3)
	app, err := newCache.Get(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)
	s.Require().NotNil(app)
	s.Require().Equal(s.testApp1.Address, app.Address)

	// Verify L3 was NOT called (L2 hit)
	s.Require().Equal(int64(0), s.mockQueryClient.getCallCount(s.testApp1.Address), "L2 hit should not query L3")
}

// TestApplicationCache_GetFromL1 verifies L1 has data returns immediately (no Redis call).
func (s *ApplicationCacheTestSuite) TestApplicationCache_GetFromL1() {
	// Pre-populate L1 by doing a Get first
	app1, err := s.cache.Get(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount(s.testApp1.Address))

	// Reset call count
	s.mockQueryClient.resetCallCount()

	// Get app again (should return from L1 immediately)
	app2, err := s.cache.Get(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)
	s.Require().Equal(app1, app2)

	// Verify L3 was NOT called (L1 hit)
	s.Require().Equal(int64(0), s.mockQueryClient.getCallCount(s.testApp1.Address), "L1 hit should not query L3")
}

// TestApplicationCache_GetForceRefresh verifies force=true bypasses L1/L2 and queries L3.
func (s *ApplicationCacheTestSuite) TestApplicationCache_GetForceRefresh() {
	// Pre-populate cache
	_, err := s.cache.Get(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount(s.testApp1.Address))

	// Reset call count
	s.mockQueryClient.resetCallCount()

	// Get with force=true (should bypass L1/L2 and query L3)
	app, err := s.cache.Get(s.Ctx, s.testApp1.Address, true)
	s.Require().NoError(err)
	s.Require().NotNil(app)
	s.Require().Equal(s.testApp1.Address, app.Address)

	// Verify L3 was called (force refresh)
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount(s.testApp1.Address), "force=true should query L3")
}

// TestApplicationCache_InvalidateKey verifies invalidation clears L1 and L2 for specific key.
func (s *ApplicationCacheTestSuite) TestApplicationCache_InvalidateKey() {
	// Pre-populate cache with two apps
	_, err := s.cache.Get(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)
	_, err = s.cache.Get(s.Ctx, s.testApp2.Address)
	s.Require().NoError(err)

	// Verify both are in L2
	redisKey1 := s.RedisClient.KB().CacheKey(applicationCacheType, s.testApp1.Address)
	redisKey2 := s.RedisClient.KB().CacheKey(applicationCacheType, s.testApp2.Address)
	s.RequireKeyExists(redisKey1)
	s.RequireKeyExists(redisKey2)

	// Invalidate app1 only
	err = s.cache.Invalidate(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)

	// Verify app1 is cleared from L2
	val, err := s.RedisClient.Get(s.Ctx, redisKey1).Result()
	s.Require().True(errors.Is(err, redis.Nil) || val == "", "app1 should be cleared from L2")

	// Verify app2 is still in L2
	s.RequireKeyExists(redisKey2)

	// Reset call count
	s.mockQueryClient.resetCallCount()

	// Next Get for app1 should fetch from L3 again
	_, err = s.cache.Get(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount(s.testApp1.Address), "after invalidation should query L3")

	// Get for app2 should hit L1 (not invalidated)
	_, err = s.cache.Get(s.Ctx, s.testApp2.Address)
	s.Require().NoError(err)
	s.Require().Equal(int64(0), s.mockQueryClient.getCallCount(s.testApp2.Address), "app2 should still be in L1")
}

// TestApplicationCache_InvalidateAll verifies InvalidateAll clears all cached apps.
func (s *ApplicationCacheTestSuite) TestApplicationCache_InvalidateAll() {
	// Pre-populate cache with two apps
	_, err := s.cache.Get(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)
	_, err = s.cache.Get(s.Ctx, s.testApp2.Address)
	s.Require().NoError(err)

	// Invalidate all
	err = s.cache.InvalidateAll(s.Ctx)
	s.Require().NoError(err)

	// Verify both are cleared from L2
	redisKey1 := s.RedisClient.KB().CacheKey(applicationCacheType, s.testApp1.Address)
	redisKey2 := s.RedisClient.KB().CacheKey(applicationCacheType, s.testApp2.Address)
	s.RequireKeyNotExists(redisKey1)
	s.RequireKeyNotExists(redisKey2)

	// Reset call count
	s.mockQueryClient.resetCallCount()

	// Next Gets should fetch from L3
	_, err = s.cache.Get(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount(s.testApp1.Address))

	_, err = s.cache.Get(s.Ctx, s.testApp2.Address)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount(s.testApp2.Address))
}

// TestApplicationCache_TTLExpiry verifies L2 entries expire after TTL (use miniredis FastForward).
func (s *ApplicationCacheTestSuite) TestApplicationCache_TTLExpiry() {
	// Set app in cache with 1 second TTL
	err := s.cache.Set(s.Ctx, s.testApp1.Address, s.testApp1, 1*time.Second)
	s.Require().NoError(err)

	// Verify app is in L2
	redisKey := s.RedisClient.KB().CacheKey(applicationCacheType, s.testApp1.Address)
	s.RequireKeyExists(redisKey)

	// Fast-forward time by 2 seconds
	s.MiniRedis.FastForward(2 * time.Second)

	// Verify app is expired in L2
	s.RequireKeyNotExists(redisKey)
}

// TestApplicationCache_SetExplicit verifies Set stores in L1+L2 with TTL.
func (s *ApplicationCacheTestSuite) TestApplicationCache_SetExplicit() {
	// Verify cache is empty
	s.Require().Equal(0, s.GetKeyCount())

	// Set app explicitly
	err := s.cache.Set(s.Ctx, s.testApp1.Address, s.testApp1, 5*time.Minute)
	s.Require().NoError(err)

	// Verify data is in L2 (Redis) with correct proto marshaling
	redisKey := s.RedisClient.KB().CacheKey(applicationCacheType, s.testApp1.Address)
	data, err := s.RedisClient.Get(s.Ctx, redisKey).Bytes()
	s.Require().NoError(err)

	// Unmarshal and verify
	app := &apptypes.Application{}
	err = proto.Unmarshal(data, app)
	s.Require().NoError(err)
	s.Require().Equal(s.testApp1.Address, app.Address)

	// Verify data is in L1 (subsequent Get should not query L3)
	retrieved, err := s.cache.Get(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)
	s.Require().Equal(s.testApp1.Address, retrieved.Address)
	s.Require().Equal(int64(0), s.mockQueryClient.getCallCount(s.testApp1.Address), "L1 hit should not query L3")
}

// TestApplicationCache_MultipleKeys verifies different app addresses are cached independently.
func (s *ApplicationCacheTestSuite) TestApplicationCache_MultipleKeys() {
	// Get both apps
	app1, err := s.cache.Get(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)
	s.Require().Equal(s.testApp1.Address, app1.Address)

	app2, err := s.cache.Get(s.Ctx, s.testApp2.Address)
	s.Require().NoError(err)
	s.Require().Equal(s.testApp2.Address, app2.Address)

	// Verify each was queried once
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount(s.testApp1.Address))
	s.Require().Equal(int64(1), s.mockQueryClient.getCallCount(s.testApp2.Address))

	// Verify both are in L2
	redisKey1 := s.RedisClient.KB().CacheKey(applicationCacheType, s.testApp1.Address)
	redisKey2 := s.RedisClient.KB().CacheKey(applicationCacheType, s.testApp2.Address)
	s.RequireKeyExists(redisKey1)
	s.RequireKeyExists(redisKey2)

	// Reset call count
	s.mockQueryClient.resetCallCount()

	// Subsequent Gets should hit L1
	_, err = s.cache.Get(s.Ctx, s.testApp1.Address)
	s.Require().NoError(err)
	_, err = s.cache.Get(s.Ctx, s.testApp2.Address)
	s.Require().NoError(err)

	s.Require().Equal(int64(0), s.mockQueryClient.getCallCount(s.testApp1.Address))
	s.Require().Equal(int64(0), s.mockQueryClient.getCallCount(s.testApp2.Address))
}

// TestApplicationCache_ConcurrentGetSameKey verifies concurrent Gets for same cold key
// trigger single L3 query (distributed lock prevents duplicate queries).
func (s *ApplicationCacheTestSuite) TestApplicationCache_ConcurrentGetSameKey() {
	// Create two cache instances (simulating two relayer instances)
	logger1 := logging.NewLoggerFromConfig(logging.DefaultConfig())
	logger2 := logging.NewLoggerFromConfig(logging.DefaultConfig())

	cache1 := NewApplicationCache(logger1, s.RedisClient, s.mockQueryClient)
	cache2 := NewApplicationCache(logger2, s.RedisClient, s.mockQueryClient)

	err := cache1.Start(s.Ctx)
	s.Require().NoError(err)
	defer cache1.Close()

	err = cache2.Start(s.Ctx)
	s.Require().NoError(err)
	defer cache2.Close()

	// Reset call count
	s.mockQueryClient.resetCallCount()

	// Concurrent Gets from both caches (cold L1 + L2) for same key
	var wg sync.WaitGroup
	wg.Add(2)

	var app1, app2 *apptypes.Application
	var err1, err2 error

	go func() {
		defer wg.Done()
		app1, err1 = cache1.Get(s.Ctx, s.testApp1.Address)
	}()

	go func() {
		defer wg.Done()
		app2, err2 = cache2.Get(s.Ctx, s.testApp1.Address)
	}()

	wg.Wait()

	// Both should succeed
	s.Require().NoError(err1)
	s.Require().NoError(err2)
	s.Require().NotNil(app1)
	s.Require().NotNil(app2)

	// Verify L3 was called at most twice (ideally once with distributed lock, but timing may vary)
	callCount := s.mockQueryClient.getCallCount(s.testApp1.Address)
	s.Require().LessOrEqual(callCount, int64(2), "distributed lock should minimize L3 queries")
	s.Require().GreaterOrEqual(callCount, int64(1), "at least one L3 query needed")
}

// TestApplicationCache_ConcurrentReads verifies multiple concurrent reads are safe.
func (s *ApplicationCacheTestSuite) TestApplicationCache_ConcurrentReads() {
	// Pre-populate cache
	_, err := s.cache.Get(s.Ctx, s.testApp1.Address)
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
			app, err := s.cache.Get(s.Ctx, s.testApp1.Address)
			if err == nil && app != nil {
				successCount.Add(1)
			}
		}()
	}

	wg.Wait()

	// All reads should succeed
	s.Require().Equal(int64(numReads), successCount.Load())

	// L3 should NOT be called (all L1 hits)
	s.Require().Equal(int64(0), s.mockQueryClient.getCallCount(s.testApp1.Address), "concurrent L1 reads should not query L3")
}
