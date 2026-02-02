# Testing Features for Distributed Go Systems

**Research Date:** 2026-02-02
**Context:** Pocket RelayMiner (HA) - Production-grade distributed relay mining with Redis-backed state
**Goal:** Identify testing patterns essential for safe refactoring of complex distributed systems

## Executive Summary

This document categorizes testing patterns from a production Go distributed system handling 1000+ RPS with complex state machines, Redis-backed storage, and multi-transport support. Patterns are organized by criticality: **Table Stakes** (must-have), **Differentiators** (add value), and **Anti-Patterns** (avoid).

---

## Table Stakes (Must Have for Production Confidence)

### 1. Real Implementation Testing

**Pattern:** Use real implementations instead of mocks wherever possible.

**Why Critical:**
- Mock-based tests can pass while production code fails
- Real implementations catch integration bugs that mocks hide
- Reduces test maintenance burden (no mock behavior updates)

**Implementation:**
```go
// ✅ GOOD: Real Redis via miniredis
type RedisSMSTTestSuite struct {
    suite.Suite
    miniRedis   *miniredis.Miniredis  // Real Redis in-process
    redisClient *redisutil.Client
    ctx         context.Context
}

func (s *RedisSMSTTestSuite) SetupSuite() {
    mr, err := miniredis.Run()
    s.Require().NoError(err)
    s.miniRedis = mr

    redisURL := fmt.Sprintf("redis://%s", mr.Addr())
    client, err := redisutil.NewClient(s.ctx, redisutil.ClientConfig{URL: redisURL})
    s.Require().NoError(err)
    s.redisClient = client
}

// ❌ BAD: Mock Redis client
type mockRedis struct {
    mock.Mock
    // Complex mock logic that diverges from real Redis behavior
}
```

**When to Use Real:**
- Redis: `miniredis/v2` (in-process, full Redis protocol)
- Protocol types: Real proto messages from dependencies
- HTTP servers: `httptest.NewServer()` for real HTTP handling
- WebSocket: Real connections via test servers
- Crypto: Real signing/verification (fast enough for tests)

**When Mocking is Acceptable:**
- External blockchain RPC (unpredictable, rate-limited)
- Time sources (for deterministic timing tests)
- Random sources (for deterministic behavior)

**Dependencies:** `github.com/alicebob/miniredis/v2`, `net/http/httptest`

---

### 2. Race-Free Concurrency Testing

**Pattern:** All concurrent code must pass `-race` detector with zero warnings.

**Why Critical:**
- Production system handles 1000+ RPS with concurrent goroutines
- Race conditions manifest as silent data corruption in production
- `-race` detector catches bugs that manual review misses

**Implementation:**
```go
// TestRedisMapStore_Concurrency verifies thread safety
func (s *RedisSMSTTestSuite) TestRedisMapStore_Concurrency() {
    store := s.createTestRedisStore("test-session-concurrency")

    numGoroutines := 10
    numOpsPerGoroutine := 100

    var wg sync.WaitGroup
    wg.Add(numGoroutines)

    // Launch concurrent writers
    for g := 0; g < numGoroutines; g++ {
        goroutineID := g
        go func() {
            defer wg.Done()

            for i := 0; i < numOpsPerGoroutine; i++ {
                key := []byte(fmt.Sprintf("key_g%d_i%d", goroutineID, i))
                value := []byte(fmt.Sprintf("value_g%d_i%d", goroutineID, i))

                err := store.Set(key, value)
                s.Require().NoError(err)

                gotValue, err := store.Get(key)
                s.Require().NoError(err)
                s.Require().Equal(value, gotValue)
            }
        }()
    }

    wg.Wait()

    // Verify final state
    expectedKeys := numGoroutines * numOpsPerGoroutine
    length, err := store.Len()
    s.Require().NoError(err)
    s.Require().Equal(expectedKeys, length)
}
```

**Critical for:**
- Shared state access (caches, counters, registries)
- Mock objects used by concurrent tests (must use `sync.Mutex`)
- Channel operations
- Map access without sync

**Command:**
```bash
make test_miner  # Runs: go test -race -count=1 ./miner/...
go test -race ./...
```

**Rule #1 Compliance:**
- No flaky tests: Races are non-deterministic failures
- No shortcuts: Every concurrent path must be tested with `-race`
- Zero tolerance: Fix all race warnings before merging

---

### 3. Deterministic Test Data

**Pattern:** Use seed-based or constant data generation for reproducible tests.

**Why Critical:**
- Flaky tests destroy confidence in CI/CD
- Random data makes debugging impossible
- Production bugs must be reproducible in tests

**Implementation:**
```go
// ✅ GOOD: Deterministic relay generation
func (s *RedisSMSTTestSuite) generateTestRelays(count int, seed byte) []testRelay {
    relays := make([]testRelay, count)
    for i := 0; i < count; i++ {
        relays[i] = testRelay{
            key:    []byte(fmt.Sprintf("relay_key_%d_%d", seed, i)),
            value:  []byte(fmt.Sprintf("relay_value_%d_%d", seed, i)),
            weight: uint64((i + 1) * 100), // 100, 200, 300...
        }
    }
    return relays
}

// Usage: Same seed = same output every time
func (s *RedisSMSTTestSuite) TestSMST_Reproducibility() {
    relays1 := s.generateTestRelays(50, 42)
    relays2 := s.generateTestRelays(50, 42)

    // Build trees with identical data
    // Both trees should produce IDENTICAL root hashes
}

// ❌ BAD: Random data
func generateRandomRelays(count int) []testRelay {
    relays := make([]testRelay, count)
    for i := 0; i < count; i++ {
        relays[i] = testRelay{
            key:   randomBytes(32),  // ❌ Non-reproducible
            value: randomBytes(64),  // ❌ Non-reproducible
        }
    }
    return relays
}
```

**Deterministic Patterns:**
- Counter-based IDs: `fmt.Sprintf("session_%d", i)`
- Seeded pseudo-random: Only for testing randomness handling
- Fixed timestamps: `time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)`
- Known bit patterns: `generateKnownBitPath("all_zeros")`

**Avoid:**
- `time.Now()` in tests (use fixed times)
- `rand.Intn()` without seed (use deterministic data)
- UUIDs (use predictable IDs: "test-session-1", "test-session-2")

---

### 4. Test Suite Pattern (Shared Setup)

**Pattern:** Use `testify/suite` for shared test infrastructure and lifecycle management.

**Why Critical:**
- Reduces resource exhaustion (1 miniredis instance vs 100s)
- Ensures proper cleanup (no leaked goroutines/connections)
- Provides test isolation via `SetupTest()` flush

**Implementation:**
```go
type RedisSMSTTestSuite struct {
    suite.Suite
    miniRedis   *miniredis.Miniredis  // Shared across ALL tests
    redisClient *redisutil.Client
    ctx         context.Context
}

// SetupSuite: Run ONCE before all tests
func (s *RedisSMSTTestSuite) SetupSuite() {
    mr, err := miniredis.Run()
    s.Require().NoError(err)
    s.miniRedis = mr

    redisURL := fmt.Sprintf("redis://%s", mr.Addr())
    client, err := redisutil.NewClient(s.ctx, redisutil.ClientConfig{URL: redisURL})
    s.Require().NoError(err)
    s.redisClient = client
}

// SetupTest: Run BEFORE each test (isolation)
func (s *RedisSMSTTestSuite) SetupTest() {
    s.miniRedis.FlushAll()  // Clean slate for each test
}

// TearDownSuite: Run ONCE after all tests
func (s *RedisSMSTTestSuite) TearDownSuite() {
    if s.miniRedis != nil {
        s.miniRedis.Close()
    }
    if s.redisClient != nil {
        s.redisClient.Close()
    }
}

// Helper methods as suite methods
func (s *RedisSMSTTestSuite) createTestRedisStore(sessionID string) *RedisMapStore {
    store := NewRedisMapStore(s.ctx, s.redisClient, sessionID)
    redisStore, ok := store.(*RedisMapStore)
    s.Require().True(ok)
    return redisStore
}

// All tests as suite methods
func (s *RedisSMSTTestSuite) TestRedisMapStore_SetGet() {
    store := s.createTestRedisStore("test-session")
    // Test logic using s.Require()
}

// Register suite
func TestRedisSMSTTestSuite(t *testing.T) {
    suite.Run(t, new(RedisSMSTTestSuite))
}
```

**Benefits:**
- **Performance:** 143 cache tests share 1 miniredis (vs creating 143 instances)
- **Reliability:** Proper cleanup prevents test interference
- **Maintainability:** Helpers (`createTestRedisStore()`) reused across all tests

**Trade-offs:**
- Sequential execution: Cache tests run `-p 1 -parallel 1` due to shared miniredis
- Learning curve: Suite pattern is less familiar than standalone tests

**Dependencies:** `github.com/stretchr/testify/suite`

---

### 5. State Machine Verification

**Pattern:** Test all state transitions and lifecycle hooks for complex state machines.

**Why Critical:**
- Session lifecycle has 7 states (new → active → claimed → proved → settled)
- Invalid transitions cause data corruption or lost rewards
- Lifecycle callbacks (SMST cleanup, deduplication) must fire correctly

**Implementation:**
```go
// Test lifecycle transition: Proved → Cleanup
func TestLifecycleCallback_OnSessionProved_CleansDeduplicator(t *testing.T) {
    smstManager := &mockSMSTManager{}
    dedup := &mockDeduplicator{}

    lc := createTestLifecycleCallback(smstManager)
    lc.SetDeduplicator(dedup)

    snapshot := &SessionSnapshot{
        SessionID: "test-session-123",
        State:     SessionStateProved,
    }

    // Execute state transition
    err := lc.OnSessionProved(context.Background(), snapshot)

    // Verify side effects
    require.NoError(t, err)
    require.Len(t, dedup.cleanedSessions, 1)
    require.Equal(t, "test-session-123", dedup.cleanedSessions[0])
    require.Len(t, smstManager.deletedTrees, 1)
}

// Test invalid transition protection
func (s *RedisSMSTTestSuite) TestRedisSMSTManager_ClaimedTreeImmutable() {
    manager := s.createTestRedisSMSTManager("pokt1test")
    sessionID := "session-immutable"

    // Add relay
    err := manager.UpdateTree(s.ctx, sessionID, []byte("key1"), []byte("value1"), 100)
    s.Require().NoError(err)

    // Flush tree (marks as claimed)
    _, err = manager.FlushTree(s.ctx, sessionID)
    s.Require().NoError(err)

    // Attempt update after flush (should fail)
    err = manager.UpdateTree(s.ctx, sessionID, []byte("key2"), []byte("value2"), 200)
    s.Require().Error(err)
    s.Require().Contains(err.Error(), "already been claimed")
}
```

**Critical States to Test:**
- Initial state
- All valid transitions
- All invalid transitions (should error)
- Side effects on each transition (cleanup, notifications)
- Concurrent transition attempts (race conditions)

**Example States:**
- Session: new → active → claimed → proved → settled
- Cache: empty → l1_hit → invalidated → refreshing → l1_hit
- Leader: follower → candidate → leader → follower

---

### 6. Root Hash Equivalence (Critical Invariant)

**Pattern:** Verify distributed storage produces identical results to in-memory reference.

**Why Critical:**
- SMST root hash determines blockchain rewards (~$10k+ per session)
- Redis-backed SMST must match in-memory SMST exactly
- Any divergence = lost rewards or rejected claims

**Implementation:**
```go
// CRITICAL TEST: Root Hash Equivalence
func (s *RedisSMSTTestSuite) TestRedisSMSTManager_RootHashEquivalence() {
    manager := s.createTestRedisSMSTManager("pokt1test")
    sessionID := "session-equivalence"

    // Create in-memory SMST for comparison
    inMemorySMST := s.createInMemorySMST()

    // Generate test relays
    relays := s.generateTestRelays(50, 77)

    // Apply identical operations to both trees
    for _, relay := range relays {
        // In-memory SMST
        err := inMemorySMST.Update(relay.key, relay.value, relay.weight)
        s.Require().NoError(err)
        err = inMemorySMST.Commit()
        s.Require().NoError(err)

        // Redis-backed SMST
        err = manager.UpdateTree(s.ctx, sessionID, relay.key, relay.value, relay.weight)
        s.Require().NoError(err)
    }

    // Get root hashes
    inMemoryRoot := inMemorySMST.Root()
    redisRoot, err := manager.FlushTree(s.ctx, sessionID)
    s.Require().NoError(err)

    // CRITICAL ASSERTION: Root hashes MUST match
    s.assertRootHashEqual(inMemoryRoot, redisRoot,
        "Redis-backed SMST MUST match in-memory SMST for identical operations")
}
```

**Pattern for Other Invariants:**
- Distributed counters: Verify sum matches expected
- Cache consistency: L1 and L2 return identical data
- Merkle trees: Verify proof validation
- Signatures: Verify recovery matches expected address

**When to Use:**
- Any operation with financial implications
- Cryptographic operations (hashing, signing)
- Distributed consensus algorithms
- Data integrity checks

---

### 7. Build Constraints for Test-Only Code

**Pattern:** Use `//go:build test` to exclude test utilities from production builds.

**Why Critical:**
- Reduces binary size (excludes test infrastructure)
- Prevents accidental dependency on test-only code
- Documents test-only interfaces clearly

**Implementation:**
```go
//go:build test

package miner

import (
    "testing"
    "github.com/stretchr/testify/suite"
)

// RedisSMSTTestSuite: Test-only infrastructure
type RedisSMSTTestSuite struct {
    suite.Suite
    miniRedis   *miniredis.Miniredis
    redisClient *redisutil.Client
    ctx         context.Context
}

// Only compiled when running tests
func (s *RedisSMSTTestSuite) createTestRedisStore(sessionID string) *RedisMapStore {
    // Helper for tests only
}
```

**Files Requiring Build Constraints:**
- Test suites: `redis_smst_utils_test.go`
- Test fixtures: `redis_smst_bench_test.go`
- Test-only implementations: `mock_*.go` (if using mocks)

**Command:**
```bash
go test -tags test ./...  # Includes test-tagged code
go build .               # Excludes test-tagged code
```

**Usage Count:** 31 files in pocket-relay-miner use `//go:build test`

---

### 8. Warmup/Failover Testing

**Pattern:** Test distributed system can resume from shared state after restart.

**Why Critical:**
- HA systems must survive instance crashes
- Redis stores ALL session state
- New instance must load existing sessions without data loss

**Implementation:**
```go
// TestRedisSMSTManager_WarmupFromRedis tests restoring state after restart
func (s *RedisSMSTTestSuite) TestRedisSMSTManager_WarmupFromRedis() {
    manager1 := s.createTestRedisSMSTManager("pokt1test")
    sessionID := "session-warmup"

    // Manager 1: Create and populate tree
    relays := s.generateTestRelays(10, 88)
    for _, relay := range relays {
        err := manager1.UpdateTree(s.ctx, sessionID, relay.key, relay.value, relay.weight)
        s.Require().NoError(err)
    }

    // Flush to get root hash
    root1, err := manager1.FlushTree(s.ctx, sessionID)
    s.Require().NoError(err)

    // Create second manager instance (simulates restart/failover)
    manager2 := s.createTestRedisSMSTManager("pokt1test")

    // Warmup from Redis (loads existing trees)
    loadedCount, err := manager2.WarmupFromRedis(s.ctx)
    s.Require().NoError(err)
    s.Require().Equal(1, loadedCount)

    // Verify tree exists in manager2
    s.Require().Equal(1, manager2.GetTreeCount())

    // Get root from manager2 (should match manager1)
    root2, err := manager2.GetTreeRoot(s.ctx, sessionID)
    s.Require().NoError(err)
    s.assertRootHashEqual(root1, root2, "warmed-up tree must have same root")

    // Verify stats match
    count1, sum1, err := manager1.GetTreeStats(sessionID)
    s.Require().NoError(err)
    count2, sum2, err := manager2.GetTreeStats(sessionID)
    s.Require().NoError(err)

    s.Require().Equal(count1, count2)
    s.Require().Equal(sum1, sum2)
}
```

**Critical Scenarios:**
- Leader election handoff
- Cache refresh after restart
- Session continuation after crash
- Consumer group resumption (Redis Streams)

**Dependencies:**
- Shared state in Redis/database
- Idempotent operations (replay-safe)

---

## Differentiators (Add Value)

### 9. Table-Driven Tests

**Pattern:** Use data tables for testing multiple similar scenarios.

**Why Valuable:**
- Reduces code duplication
- Easy to add new test cases
- Clear separation of data vs logic

**Implementation:**
```go
func TestCalculateProofWindow(t *testing.T) {
    tests := []struct {
        name             string
        sessionEnd       int64
        proofOpenOffset  uint64
        proofCloseOffset uint64
        expected         int64
    }{
        {
            name:             "standard window",
            sessionEnd:       100,
            proofOpenOffset:  4,
            proofCloseOffset: 12,
            expected:         104, // 100 + 4
        },
        {
            name:             "zero offset",
            sessionEnd:       200,
            proofOpenOffset:  0,
            proofCloseOffset: 8,
            expected:         200,
        },
        {
            name:             "large session",
            sessionEnd:       1000000,
            proofOpenOffset:  100,
            proofCloseOffset: 500,
            expected:         1000100,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            params := &sharedtypes.Params{
                ProofWindowOpenOffsetBlocks:  tt.proofOpenOffset,
                ProofWindowCloseOffsetBlocks: tt.proofCloseOffset,
            }

            got := CalculateProofWindowOpen(params, tt.sessionEnd)
            require.Equal(t, tt.expected, got)
        })
    }
}
```

**Best For:**
- Configuration validation
- Error cases with different inputs
- Mathematical calculations
- Protocol edge cases

**Avoid For:**
- Complex setup/teardown (use suite pattern instead)
- Tests requiring shared state
- Tests with different assertion logic per case

---

### 10. Benchmarking Critical Paths

**Pattern:** Benchmark hot paths to catch performance regressions.

**Why Valuable:**
- Performance is a feature (1000+ RPS target)
- Catches O(n²) bugs before production
- Documents performance characteristics

**Implementation:**
```go
//go:build test

package miner

// BenchmarkRedisMapStore_Get benchmarks HGET operation
func BenchmarkRedisMapStore_Get(b *testing.B) {
    suite := setupBenchSuite(b)
    defer suite.Cleanup()

    store := suite.createTestRedisStore("bench-get")
    key := []byte("test-key")
    value := []byte("test-value")

    // Setup
    err := store.Set(key, value)
    if err != nil {
        b.Fatalf("failed to set value: %v", err)
    }

    // Benchmark
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, err := store.Get(key)
        if err != nil {
            b.Fatalf("Get failed: %v", err)
        }
    }
}
```

**Run:**
```bash
go test -bench=BenchmarkRedisMapStore_Get -benchmem ./miner
# Output: BenchmarkRedisMapStore_Get-8  36651  29734 ns/op  632 B/op  27 allocs/op
```

**Critical Paths:**
- SMST operations (Get, Set, Delete): ~30µs target
- Relay validation: <1ms target
- Cache L1 hit: <100ns target
- Signature verification: <5ms target

**Interpreting Results:**
- `ns/op`: Nanoseconds per operation (lower is better)
- `B/op`: Bytes allocated per operation (lower is better)
- `allocs/op`: Heap allocations per operation (lower is better)

**Dependencies:** Standard `testing` package

---

### 11. Integration Test with Real External Services

**Pattern:** Use test containers or mock servers for external dependencies.

**Why Valuable:**
- Catches protocol mismatches
- Validates error handling against real responses
- Tests reconnection logic

**Implementation:**
```go
// mockCometBFTServer simulates CometBFT RPC server
type mockCometBFTServer struct {
    t              *testing.T
    server         *httptest.Server
    wsUpgrader     websocket.Upgrader
    currentHeight  atomic.Int64
    wsConnections  []*websocket.Conn
    subscriptions  map[string]chan blockEvent
}

func newMockCometBFTServer(t *testing.T) *mockCometBFTServer {
    mock := &mockCometBFTServer{
        t:             t,
        subscriptions: make(map[string]chan blockEvent),
    }
    mock.currentHeight.Store(100)

    mock.server = httptest.NewServer(http.HandlerFunc(mock.handler))

    t.Cleanup(func() {
        mock.Close()
    })

    return mock
}

func TestBlockSubscriber_WebSocketSubscription(t *testing.T) {
    mockServer := newMockCometBFTServer(t)
    defer mockServer.Close()

    config := BlockSubscriberConfig{
        RPCEndpoint: mockServer.server.URL,
    }

    subscriber, err := NewBlockSubscriber(logger, config)
    require.NoError(t, err)
    defer subscriber.Close()

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    err = subscriber.Start(ctx)
    require.NoError(t, err)

    // Send block events
    mockServer.incrementHeight()
    mockServer.incrementHeight()

    // Verify subscription is active
    require.NotNil(t, subscriber.cometClient)
}
```

**Use Cases:**
- WebSocket connections (block subscriptions)
- HTTP endpoints (relay proxying)
- gRPC services (blockchain queries)

**Trade-offs:**
- More complex than mocks
- Slower than unit tests
- Requires careful cleanup

**Dependencies:** `net/http/httptest`, `github.com/gorilla/websocket`

---

### 12. Sealing Mechanism Tests (Race Condition Prevention)

**Pattern:** Test two-phase commit and locking mechanisms under concurrent load.

**Why Valuable:**
- Prevents double-spending style bugs
- Validates distributed locking
- Catches timing-dependent race conditions

**Implementation:**
```go
// TestRedisSMSTManager_Sealing_ConcurrentUpdateDuringFlush tests that
// UpdateTree is properly rejected when called concurrently with FlushTree
func (s *RedisSMSTTestSuite) TestRedisSMSTManager_Sealing_ConcurrentUpdateDuringFlush() {
    manager := s.createTestRedisSMSTManager("pokt1test")
    sessionID := "session-sealing"

    // Add initial relay
    err := manager.UpdateTree(s.ctx, sessionID, []byte("key1"), []byte("value1"), 100)
    s.Require().NoError(err)

    var wg sync.WaitGroup
    wg.Add(2)

    flushStarted := make(chan struct{})
    updateDuringFlush := make(chan error, 1)

    // Goroutine 1: FlushTree (sets sealing=true)
    go func() {
        defer wg.Done()
        close(flushStarted)
        _, err := manager.FlushTree(s.ctx, sessionID)
        s.Require().NoError(err)
    }()

    // Goroutine 2: UpdateTree (should be rejected during flush)
    go func() {
        defer wg.Done()
        <-flushStarted
        time.Sleep(5 * time.Millisecond)
        err := manager.UpdateTree(s.ctx, sessionID, []byte("key2"), []byte("value2"), 200)
        updateDuringFlush <- err
    }()

    wg.Wait()

    // Verify UpdateTree was rejected
    updateErr := <-updateDuringFlush
    s.Require().Error(updateErr)
    s.Require().Contains(updateErr.Error(), "is sealing")
}
```

**Critical Scenarios:**
- Concurrent claim submissions (only one should succeed)
- Tree sealing during relay arrival
- Cache invalidation during read
- Leader election during operation

---

### 13. High Concurrency Stress Tests

**Pattern:** Test with realistic concurrent load (10+ goroutines).

**Why Valuable:**
- Exposes race conditions not caught by low-concurrency tests
- Validates system behavior under load
- Catches resource leaks (connections, goroutines)

**Implementation:**
```go
func (s *RedisSMSTTestSuite) TestRedisSMSTManager_Sealing_HighConcurrency() {
    manager := s.createTestRedisSMSTManager("pokt1test")
    sessionID := "session-high-concurrency"

    // Add initial relay
    err := manager.UpdateTree(s.ctx, sessionID, []byte("key0"), []byte("value0"), 100)
    s.Require().NoError(err)

    numUpdates := 50
    var wg sync.WaitGroup
    wg.Add(numUpdates + 1)

    updateErrors := make([]error, numUpdates)

    // Goroutine for FlushTree
    go func() {
        defer wg.Done()
        time.Sleep(10 * time.Millisecond)
        _, err := manager.FlushTree(s.ctx, sessionID)
        s.Require().NoError(err)
    }()

    // Goroutines for concurrent updates
    for i := 0; i < numUpdates; i++ {
        idx := i
        go func() {
            defer wg.Done()
            time.Sleep(time.Duration(idx) * time.Millisecond)
            err := manager.UpdateTree(s.ctx, sessionID,
                []byte(fmt.Sprintf("key%d", idx+1)),
                []byte(fmt.Sprintf("value%d", idx+1)),
                uint64((idx+1)*100))
            updateErrors[idx] = err
        }()
    }

    wg.Wait()

    // Count successful vs rejected updates
    successCount := 0
    rejectedCount := 0
    for _, err := range updateErrors {
        if err == nil {
            successCount++
        } else {
            rejectedCount++
        }
    }

    // Some updates should succeed (before flush), others rejected
    s.Require().Greater(successCount, 0)
    s.Require().Greater(rejectedCount, 0)
}
```

**Concurrency Targets:**
- 10 goroutines: Baseline concurrent access
- 50 goroutines: Moderate load simulation
- 100+ goroutines: Stress test resource limits

---

### 14. Error Injection Testing

**Pattern:** Inject failures into dependencies to test error handling.

**Why Valuable:**
- Validates graceful degradation
- Tests retry logic
- Catches unhandled error paths

**Implementation:**
```go
type mockTxClient struct {
    submitProofsFunc func(ctx context.Context, timeoutHeight int64, proofs ...client.MsgSubmitProof) error
    failNextSubmit   atomic.Bool
}

func (m *mockTxClient) SubmitProofs(ctx context.Context, timeoutHeight int64, proofs ...client.MsgSubmitProof) error {
    if m.failNextSubmit.Load() {
        m.failNextSubmit.Store(false)
        return fmt.Errorf("proof submission failed")
    }
    if m.submitProofsFunc != nil {
        return m.submitProofsFunc(ctx, timeoutHeight, proofs...)
    }
    return nil
}

func TestProofBatcher_SubmitBatch_WithFailure(t *testing.T) {
    txClient := &mockTxClient{}
    txClient.failNextSubmit.Store(true)  // Inject failure

    batcher := NewProofBatcher(logger, txClient, "pokt1supplier", 5)

    result := batcher.SubmitBatch(ctx, proofs, 115)
    require.Error(t, result.Error)
    require.Len(t, result.FailedProofs, 1)
}
```

**Error Scenarios:**
- Network failures (timeouts, connection refused)
- Blockchain errors (invalid transaction, insufficient funds)
- Redis failures (connection lost, OOM)
- Rate limiting (429 responses)

---

### 15. Makefile Test Targets

**Pattern:** Provide convenient make targets for common test scenarios.

**Why Valuable:**
- Lowers barrier to running tests
- Documents intended test usage
- Ensures consistent test execution

**Implementation:**
```makefile
test: ## Run tests (PKG=package_name for specific package)
	@echo "Running tests..."
	@if [ -n "$(PKG)" ]; then \
		if [ "$(PKG)" = "cache" ]; then \
			go test -v -tags test -p 1 -parallel 1 ./$(PKG)/...; \
		else \
			go test -v -tags test -p 4 -parallel 4 ./$(PKG)/...; \
		fi; \
	else \
		go test -v -tags test -p 4 -parallel 4 ./...; \
	fi

test_miner: ## Run miner tests with race detection (Rule #1)
	@echo "Running miner tests with race detection..."
	@go test -v -tags test -race -count=1 -p 1 -parallel 1 ./miner/...

test-coverage: ## Run tests with coverage
	@go test -v -tags test -p 4 -parallel 4 -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"
```

**Usage:**
```bash
make test                 # Run all tests
make test PKG=miner       # Run miner tests
make test_miner           # Run miner tests with race detection
make test-coverage        # Generate coverage report
make test PKG=cache       # Run cache tests sequentially
```

---

## Anti-Patterns (Avoid)

### 16. ❌ time.Sleep() for Synchronization

**Why Bad:**
- Flaky: Race conditions manifest non-deterministically
- Slow: Adds unnecessary test latency
- Brittle: Breaks on slow CI machines

**Bad Example:**
```go
// ❌ BAD: Using sleep for synchronization
func TestConcurrentOperation(t *testing.T) {
    go doSomething()
    time.Sleep(100 * time.Millisecond)  // ❌ Hope it's done by now
    result := checkResult()
    require.True(t, result)
}
```

**Good Alternative:**
```go
// ✅ GOOD: Use channels or WaitGroups
func TestConcurrentOperation(t *testing.T) {
    done := make(chan error, 1)
    go func() {
        done <- doSomething()
    }()

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    select {
    case err := <-done:
        require.NoError(t, err)
    case <-ctx.Done():
        t.Fatal("operation timed out")
    }
}
```

**Acceptable Use:**
- Small delays in stress tests (staggered start times)
- Rate limiting simulation
- Always combined with deterministic synchronization

---

### 17. ❌ Global State Without Cleanup

**Why Bad:**
- Test order dependency (flaky failures)
- Parallel test conflicts
- Hard to debug failures

**Bad Example:**
```go
// ❌ BAD: Global state without cleanup
var globalCache = NewCache()

func TestCacheOperation(t *testing.T) {
    globalCache.Set("key", "value")
    // Test logic
    // ❌ No cleanup - affects next test
}
```

**Good Alternative:**
```go
// ✅ GOOD: Per-test state with cleanup
func TestCacheOperation(t *testing.T) {
    cache := NewCache()
    t.Cleanup(func() {
        cache.Close()
    })

    cache.Set("key", "value")
    // Test logic
}

// Or use suite pattern
func (s *RedisSMSTTestSuite) SetupTest() {
    s.miniRedis.FlushAll()  // Clean slate
}
```

---

### 18. ❌ Overly Complex Mocks

**Why Bad:**
- Diverges from real implementation behavior
- High maintenance burden
- False confidence (tests pass, production fails)

**Bad Example:**
```go
// ❌ BAD: Complex mock with state machine
type mockRedis struct {
    mock.Mock
    state      map[string]string
    pipelines  [][]command
    txMode     bool
    lockState  map[string]bool
    ttls       map[string]time.Duration
}

func (m *mockRedis) Set(key, value string) error {
    if m.txMode {
        m.pipelines[len(m.pipelines)-1] = append(m.pipelines[len(m.pipelines)-1], setCmd{key, value})
        return nil
    }
    // 50+ lines of mock logic
}
```

**Good Alternative:**
```go
// ✅ GOOD: Use miniredis (real Redis implementation)
func (s *RedisSMSTTestSuite) SetupSuite() {
    mr, err := miniredis.Run()
    s.Require().NoError(err)
    s.miniRedis = mr
}
```

**When Mocking is Acceptable:**
- External APIs (blockchain RPC)
- Simple function pointers (1-2 line implementations)

---

### 19. ❌ Testing Implementation Details

**Why Bad:**
- Brittle: Breaks on refactoring
- Couples tests to internal structure
- Reduces refactoring confidence

**Bad Example:**
```go
// ❌ BAD: Testing internal cache structure
func TestCache_InternalStructure(t *testing.T) {
    cache := NewCache()

    // ❌ Accessing private fields via reflection
    field := reflect.ValueOf(cache).Elem().FieldByName("l1Cache")
    l1Cache := field.Interface().(map[string]interface{})
    require.Len(t, l1Cache, 0)
}
```

**Good Alternative:**
```go
// ✅ GOOD: Test observable behavior
func TestCache_Behavior(t *testing.T) {
    cache := NewCache()

    // Test public API
    cache.Set("key", "value")

    got, err := cache.Get("key")
    require.NoError(t, err)
    require.Equal(t, "value", got)
}
```

---

### 20. ❌ Skipped Tests Without TODO

**Why Bad:**
- Technical debt accumulates invisibly
- Lost context for why test is skipped
- No tracking of when to re-enable

**Bad Example:**
```go
// ❌ BAD: Skipped without explanation
func TestReconnection(t *testing.T) {
    t.Skip()
    // Test logic
}
```

**Good Alternative:**
```go
// ✅ GOOD: Skip with TODO and context
func TestReconnection(t *testing.T) {
    t.Skip("Skipping reconnection test - requires dynamic server recovery")
    // TODO(e2e): Re-implement as e2e test with testcontainers
    // Test logic
}
```

---

### 21. ❌ Ignoring Race Detector Warnings

**Why Bad:**
- Silent data corruption in production
- Non-deterministic failures
- Lost debugging time

**Bad Example:**
```bash
# ❌ BAD: Ignoring race warnings
go test ./...
# WARNING: DATA RACE
# ... (ignored)
```

**Good Alternative:**
```bash
# ✅ GOOD: Always run with -race and fix all warnings
go test -race ./...
make test_miner  # Includes -race flag
```

**Rule #1:** Zero tolerance for race conditions.

---

### 22. ❌ Non-Isolated Integration Tests

**Why Bad:**
- Requires manual cleanup between runs
- Can't run tests in parallel
- Fails when developer has local Redis running

**Bad Example:**
```go
// ❌ BAD: Connects to real Redis on localhost
func TestRedisOperations(t *testing.T) {
    client := redis.NewClient(&redis.Options{
        Addr: "localhost:6379",  // ❌ Assumes local Redis
    })

    client.Set("key", "value", 0)
    // Test logic
    // ❌ No cleanup
}
```

**Good Alternative:**
```go
// ✅ GOOD: Use miniredis for isolation
func (s *RedisSMSTTestSuite) SetupSuite() {
    mr, err := miniredis.Run()
    s.Require().NoError(err)
    s.miniRedis = mr
}

func (s *RedisSMSTTestSuite) SetupTest() {
    s.miniRedis.FlushAll()  // Automatic cleanup
}
```

---

## Pattern Dependencies

### Dependency Graph

```
Real Implementation Testing (1)
  └─→ Test Suite Pattern (4)
       └─→ Build Constraints (7)
       └─→ Deterministic Test Data (3)

Race-Free Concurrency Testing (2)
  └─→ High Concurrency Stress Tests (13)
  └─→ Sealing Mechanism Tests (12)

State Machine Verification (5)
  └─→ Integration Test with Real Services (11)
  └─→ Warmup/Failover Testing (8)

Root Hash Equivalence (6)
  └─→ Deterministic Test Data (3)
  └─→ Real Implementation Testing (1)

Table-Driven Tests (9)
  └─→ Deterministic Test Data (3)

Benchmarking Critical Paths (10)
  └─→ Test Suite Pattern (4)
  └─→ Real Implementation Testing (1)
```

### Critical Path (Minimum for Production)

1. **Real Implementation Testing** - Foundation for all tests
2. **Race-Free Concurrency Testing** - Catch silent corruption
3. **Deterministic Test Data** - Prevent flakiness
4. **Test Suite Pattern** - Manage shared resources
5. **State Machine Verification** - Protect lifecycle logic
6. **Root Hash Equivalence** - Validate financial correctness
7. **Build Constraints** - Clean separation of test code
8. **Warmup/Failover Testing** - HA system requirement

---

## Applying to Pocket RelayMiner

### Current Test Coverage (13,757 lines)

**Strong Areas:**
- Redis storage testing (miniredis-based)
- SMST root hash equivalence
- Concurrency testing with race detection
- Suite pattern for shared infrastructure
- Deterministic test data generators

**Growth Opportunities:**
- E2E tests with testcontainers (marked as TODO)
- Load testing automation (currently manual scripts)
- Chaos testing (Redis failures, network partitions)
- Property-based testing (QuickCheck-style)

### Recommended Next Steps

1. **E2E Test Infrastructure:** Implement `TODO(e2e)` tests using testcontainers
2. **Load Test Automation:** Convert `scripts/test-*.sh` to programmatic tests
3. **Chaos Engineering:** Add Redis failure injection tests
4. **Property-Based Testing:** Add QuickCheck-style tests for SMST operations

---

## Conclusion

Testing distributed Go systems requires discipline around **real implementations**, **race-free concurrency**, and **deterministic behavior**. The pocket-relay-miner codebase demonstrates these patterns effectively:

- **13,757 lines of test code** across 36+ test files
- **100% miniredis usage** (no Redis mocks)
- **Race detection** enforced via `make test_miner`
- **Shared test suites** prevent resource exhaustion
- **Critical invariant testing** (root hash equivalence)

The anti-patterns section documents common pitfalls that would break a distributed system at scale. Following these patterns provides confidence for safe refactoring of complex state machines in production systems handling real financial value.

---

**Research Completed:** 2026-02-02
**Codebase:** Pocket RelayMiner (HA) - github.com/pokt-network/pocket-relay-miner
**Test Lines:** 13,757 across 36+ files
**Key Insight:** Use real implementations (miniredis), not mocks. Test concurrency with `-race`. Make tests deterministic.
