# Testing and Refactoring Pitfalls

**Research Date:** 2026-02-02
**Scope:** Common mistakes in Go testing and refactoring projects
**Context:** Subsequent milestone adding tests and refactoring pocket-relay-miner codebase

---

## Executive Summary

This document catalogs common pitfalls when adding tests and refactoring Go codebases, with specific focus on avoiding violations of **Rule #1: No flaky tests, no race conditions, no mock/fake tests**. Each pitfall includes warning signs for early detection, prevention strategies, and phase assignments for the roadmap.

**Critical Context:**
- Production system handling real money (1000+ RPS sustained)
- Uses miniredis for Redis (not mocks)
- Must pass `go test -race` without warnings
- Zero tolerance for non-deterministic tests

---

## Category 1: Flaky Test Anti-Patterns

### Pitfall 1.1: Time-Based Synchronization (`time.Sleep()`)

**Description:**
Using `time.Sleep()` to wait for async operations creates timing dependencies that fail under different CPU loads, CI environments, or concurrency levels.

**Current Status in Codebase:**
⚠️ **VIOLATIONS DETECTED**: 64+ instances of `time.Sleep()` across test files:
- `observability/server_test.go`: 10 instances (100-200ms sleeps)
- `client/block_subscriber_integration_test.go`: 17 instances (100-500ms sleeps)
- `miner/claim_pipeline_test.go`: 7 instances (200-500ms sleeps)
- `query/*_test.go`: Multiple 100ms sleeps for cache invalidation

**Warning Signs:**
```go
// BAD: Non-deterministic timing
time.Sleep(100 * time.Millisecond)
err := checkSomething()

// BAD: Tests fail in CI but pass locally
time.Sleep(200 * time.Millisecond)  // "Should be enough time"
```

**Why It Fails:**
- CI machines slower than dev machines → sleeps too short
- Under load, 100ms may not be enough → random failures
- No guarantee operation completes before sleep expires
- Creates test suite slowdown (death by a thousand sleeps)

**Prevention Strategy:**

1. **Use Synchronization Primitives:**
```go
// GOOD: Wait for explicit signal
done := make(chan struct{})
go func() {
    doWork()
    close(done)
}()
select {
case <-done:
    // Work completed
case <-time.After(5 * time.Second):
    t.Fatal("timeout waiting for work")
}
```

2. **Use Context Timeouts:**
```go
// GOOD: Context with timeout
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
result, err := operation(ctx)
```

3. **Poll with Retry:**
```go
// GOOD: Retry with backoff
require.Eventually(t, func() bool {
    return checkCondition()
}, 5*time.Second, 10*time.Millisecond, "condition not met")
```

4. **Use Go 1.25's `testing/synctest`:**
```go
// GOOD: Deterministic concurrency testing (Go 1.25+)
synctest.Wait() // Waits for all goroutines to complete or block
```

**Migration Path:**
- Phase 1 (Foundation): Audit all `time.Sleep()` usage, document legitimate vs problematic
- Phase 2 (Core Components): Replace sleep-based waits in miner/cache tests with channels/contexts
- Phase 3 (Edge Cases): Replace sleeps in observability/client tests with Eventually assertions
- Phase 4 (Integration): Validate no timing dependencies remain in full test suite

**References:**
- [Flaky Tests from Race Conditions](https://devassure.medium.com/flaky-tests-from-race-conditions-root-causes-and-fixes-eb345bb0c39f)
- [FOSDEM 2026 - Go synctest](https://fosdem.org/2026/schedule/event/BDH7G7-go-testing-synctest/)
- [Why AI won't save your Flaky Tests](https://thetestingpirate.be/posts/2026/2026-01-28_whyaiwontsaveyourflakytests/)

---

### Pitfall 1.2: Shared State Without Isolation

**Description:**
Tests sharing mutable state (globals, package-level vars, singletons) cause order dependencies and non-deterministic failures when run in parallel.

**Current Status in Codebase:**
✅ **MOSTLY CLEAN**: Using testify suites with `SetupTest()` flush:
```go
// miner/redis_smst_utils_test.go - GOOD pattern
func (s *RedisSMSTTestSuite) SetupTest() {
    s.miniRedis.FlushAll() // Isolates each test
}
```

⚠️ **RISK AREA**: Cache tests run sequentially due to shared miniredis (143 tests, `-p 1 -parallel 1`)

**Warning Signs:**
```go
// BAD: Shared package-level state
var testCounter int

func TestA(t *testing.T) {
    testCounter++ // Race condition
    if testCounter != 1 {
        t.Fatal("expected counter to be 1")
    }
}

// BAD: Tests pass individually but fail when run together
go test -run TestA  # PASS
go test -run TestB  # PASS
go test ./...       # FAIL (order dependent)
```

**Why It Fails:**
- `go test` runs tests in parallel by default (`-parallel` flag)
- Test order is non-deterministic across runs
- One test's state pollution affects subsequent tests
- Impossible to debug: "works on my machine" syndrome

**Prevention Strategy:**

1. **Test Suite Isolation:**
```go
// GOOD: Per-test setup/teardown
func (s *MySuite) SetupTest() {
    s.db.FlushAll()           // Clear Redis
    s.cache = NewCache()      // Fresh instance
    s.context = context.Background()
}
```

2. **Avoid `t.Parallel()` with Shared Resources:**
```go
// Current codebase: 0 instances of t.Parallel() - CORRECT
// Reason: Miniredis is shared, parallel access would cause races
```

3. **Use Test-Scoped Resources:**
```go
// GOOD: Each test gets isolated miniredis
func TestFeature(t *testing.T) {
    mr := miniredis.RunT(t) // Auto-cleanup via t.Cleanup()
    // Use mr.Addr() for this test only
}
```

**Migration Path:**
- Phase 1 (Foundation): Audit for global state, document shared resources
- Phase 2 (Core Components): Refactor tests to use suite pattern consistently
- Phase 3 (Optimization): Consider per-test miniredis for true parallelism (benchmark first)

**References:**
- [Go race conditions testing and coverage](https://www.foomo.org/blog/go-race-conditions-testing-and-coverage)

---

### Pitfall 1.3: Non-Deterministic Test Data

**Description:**
Using randomness, timestamps, or system-dependent values without seeding creates tests that fail unpredictably.

**Warning Signs:**
```go
// BAD: Random data without seed
id := rand.Intn(1000)

// BAD: System time dependency
if time.Now().Hour() > 12 {
    // Test behaves differently in morning vs afternoon
}

// BAD: Map iteration order (non-deterministic in Go)
for k, v := range map {
    results = append(results, v) // Order varies
}
```

**Prevention Strategy:**

1. **Seed Random Number Generators:**
```go
// GOOD: Deterministic random
rand.Seed(12345)
id := rand.Intn(1000) // Always same sequence
```

2. **Mock Time Dependencies:**
```go
// GOOD: Inject time source
type TimeSource interface {
    Now() time.Time
}

type testTimeSource struct {
    fixedTime time.Time
}

func (t *testTimeSource) Now() time.Time {
    return t.fixedTime
}
```

3. **Sort Non-Deterministic Results:**
```go
// GOOD: Sort before comparison
results := []string{}
for k := range myMap {
    results = append(results, k)
}
sort.Strings(results)
require.Equal(t, expected, results)
```

**Migration Path:**
- Phase 1 (Foundation): Audit crypto/rand vs math/rand usage
- Phase 2 (Core Components): Add test-time determinism for session IDs, relay hashes
- Phase 3 (Validation): Run each test 100x to verify no randomness

---

## Category 2: Race Condition Testing Mistakes

### Pitfall 2.1: Missing `-race` Flag in CI

**Description:**
Not running `go test -race` in CI allows data races to slip into production. The race detector adds 5-10x slowdown but catches real concurrency bugs.

**Current Status in Codebase:**
✅ **ENFORCED**: `make test_miner` runs with `-race` flag:
```makefile
test_miner: ## Rule #1: no flakes, no races, no mocks
	@go test -v -tags test -race -count=1 -p 1 -parallel 1 ./miner/...
```

⚠️ **GAP**: Other packages (`relayer/`, `cache/`) not explicitly running with `-race` in CI

**Warning Signs:**
```bash
# BAD: Only running race detector locally
go test -race ./...  # Dev runs this, but CI doesn't

# SYMPTOMS: Production crashes with "concurrent map write"
fatal error: concurrent map writes
```

**Prevention Strategy:**

1. **CI Configuration:**
```yaml
# GOOD: CI job with race detection
- name: Test with Race Detector
  run: make test-race
```

```makefile
# GOOD: Makefile target
test-race:
	@go test -race -count=1 ./...
```

2. **Pre-Commit Hook:**
```bash
# GOOD: Block commits with race conditions
#!/bin/bash
if ! go test -race -short ./...; then
    echo "Race condition detected - commit blocked"
    exit 1
fi
```

**Migration Path:**
- Phase 1 (Foundation): Add `make test-race` for all packages
- Phase 2 (CI Integration): Require race-free tests in CI pipeline
- Phase 3 (Pre-Commit): Add race detector to pre-commit hooks

**References:**
- [Data Race Detector](https://go.dev/doc/articles/race_detector)
- [Go race conditions testing and coverage](https://www.foomo.org/blog/go-race-conditions-testing-and-coverage)

---

### Pitfall 2.2: Unprotected Mock State

**Description:**
Mocks with shared counters/state accessed from multiple goroutines without synchronization cause race conditions in tests.

**Current Status in Codebase:**
✅ **GOOD PATTERN**: Mocks use mutexes:
```go
// miner/proof_pipeline_test.go - CORRECT
type mockSMSTProver struct {
	mu               sync.Mutex
	proveCallCount   int
}

func (m *mockSMSTProver) ProveClosest(...) {
	m.mu.Lock()
	m.proveCallCount++
	m.mu.Unlock()
	// ...
}

func (m *mockSMSTProver) getProveCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.proveCallCount
}
```

**Warning Signs:**
```go
// BAD: Unprotected mock state
type BadMock struct {
    callCount int  // Race: read/write from multiple goroutines
}

func (m *BadMock) Method() {
    m.callCount++  // RACE DETECTED
}

func (m *BadMock) GetCount() int {
    return m.callCount  // RACE DETECTED
}
```

**Prevention Strategy:**

1. **Always Protect Shared State:**
```go
// GOOD: Mutex-protected mock
type GoodMock struct {
    mu        sync.Mutex
    callCount int
}

func (m *GoodMock) Method() {
    m.mu.Lock()
    m.callCount++
    m.mu.Unlock()
}
```

2. **Use Atomic Operations:**
```go
// GOOD: Atomic counter
type GoodMock struct {
    callCount atomic.Int64
}

func (m *GoodMock) Method() {
    m.callCount.Add(1)
}
```

3. **Channel-Based Mocks:**
```go
// GOOD: Channel for call tracking
type GoodMock struct {
    calls chan Call
}

func (m *GoodMock) Method() {
    m.calls <- Call{Method: "Method", Time: time.Now()}
}
```

**Migration Path:**
- Phase 1 (Foundation): Audit all mock implementations for race safety
- Phase 2 (Standardization): Create mock templates with built-in synchronization

**References:**
- [test flaky because of race condition](https://github.com/instana/go-sensor/issues/51)

---

### Pitfall 2.3: Channel Deadlocks in Tests

**Description:**
Unbuffered channels or incorrect send/receive patterns cause tests to hang indefinitely.

**Warning Signs:**
```go
// BAD: Unbuffered channel with no receiver
ch := make(chan int)
ch <- 1  // Blocks forever

// BAD: Waiting on wrong channel
done := make(chan struct{})
go func() {
    // Never closes 'done'
}()
<-done  // Hangs forever

// BAD: Circular dependency
select {
case ch1 <- val:
case <-ch2:  // But ch2 depends on ch1
}
```

**Prevention Strategy:**

1. **Use Buffered Channels:**
```go
// GOOD: Buffer size matches expected sends
ch := make(chan int, 1)
ch <- 1  // Non-blocking
```

2. **Always Have Receiver:**
```go
// GOOD: Start goroutine before sending
ch := make(chan int)
go func() {
    <-ch
}()
ch <- 1
```

3. **Use Select with Timeout:**
```go
// GOOD: Detect deadlocks
select {
case result := <-ch:
    return result
case <-time.After(5 * time.Second):
    t.Fatal("channel receive timeout")
}
```

**Migration Path:**
- Phase 2 (Core Components): Review all channel usage in async tests
- Phase 3 (Edge Cases): Add timeout guards to all channel receives

---

## Category 3: Over-Mocking vs Under-Mocking

### Pitfall 3.1: The "Mockery" Anti-Pattern

**Description:**
Creating elaborate mock hierarchies that test the mocks instead of the actual system behavior. Tests pass but production fails because mocks don't match reality.

**Current Status in Codebase:**
✅ **GOOD**: Minimal mocking, prefer real implementations:
- Redis: `miniredis` (real in-process Redis)
- Protobuf types: Real structs from `poktroll`
- Only mock: External APIs (blockchain RPC, gRPC servers)

**Warning Signs:**
```go
// BAD: Mocking everything
mockCache := new(MockCache)
mockRedis := new(MockRedis)
mockLogger := new(MockLogger)
mockMetrics := new(MockMetrics)
mockClient := new(MockClient)

// Setting up expectations for 50+ method calls
mockCache.On("Get", "key").Return("value", nil)
mockCache.On("Set", "key", "value").Return(nil)
// ... 48 more lines of mock setup

// Test only verifies mocks were called, not actual behavior
mockCache.AssertExpectations(t)
```

**Why It Fails:**
- **Mock drift**: Mocks don't evolve with real implementation
- **False confidence**: Tests pass but production breaks
- **Brittleness**: Any interface change breaks 50+ tests
- **Onboarding burden**: New developers spend days understanding mock setup

**Prevention Strategy:**

1. **Prefer Real Implementations:**
```go
// GOOD: Real miniredis (current pattern)
mr := miniredis.RunT(t)
client, _ := redis.NewClient(redis.Options{Addr: mr.Addr()})
// Tests against real Redis protocol
```

2. **Only Mock External Boundaries:**
```go
// GOOD: Mock only blockchain RPC (external dependency)
mockRPC := &mockBlockchainRPC{
    getBlockFunc: func(height int64) (*Block, error) {
        return &Block{Height: height}, nil
    },
}
```

3. **Use Contract Tests:**
```go
// GOOD: Contract test verifies mock matches real implementation
func TestRedisContract(t *testing.T) {
    implementations := []RedisClient{
        NewRealRedis(),
        NewMiniredis(),
    }
    for _, client := range implementations {
        // Run same tests against both
        testSet(t, client)
        testGet(t, client)
    }
}
```

**Migration Path:**
- Phase 1 (Foundation): Document current mocking strategy, identify over-mocked areas
- Phase 2 (Core Components): Replace mock-heavy tests with miniredis/testcontainers
- Phase 4 (Integration): Add contract tests for remaining mocks

**References:**
- [Your Go tests probably don't need a mocking library](https://rednafi.com/go/mocking-libraries-bleh/)
- [Working without mocks](https://quii.gitbook.io/learn-go-with-tests/testing-fundamentals/working-without-mocks)
- [Anti-patterns - Learn Go with tests](https://quii.gitbook.io/learn-go-with-tests/meta/anti-patterns)
- [Unit Testing Anti-Patterns](https://www.yegor256.com/2018/12/11/unit-testing-anti-patterns.html)

---

### Pitfall 3.2: Under-Mocking External Dependencies

**Description:**
Not mocking external services (blockchain, third-party APIs) makes tests slow, brittle, and dependent on network conditions.

**Warning Signs:**
```go
// BAD: Real blockchain queries in unit tests
func TestGetSession(t *testing.T) {
    client := NewBlockchainClient("https://rpc.mainnet.pokt.network")
    session, err := client.GetSession(...)  // Network call
    // Test fails if RPC down, slow (100ms+), non-deterministic
}

// BAD: Tests require live infrastructure
func TestClaimSubmission(t *testing.T) {
    if os.Getenv("INTEGRATION") != "true" {
        t.Skip("requires live blockchain")
    }
    // Should be mockable for unit tests
}
```

**Prevention Strategy:**

1. **Interface-Based Design:**
```go
// GOOD: Interface allows mocking
type BlockchainClient interface {
    GetSession(ctx context.Context, height int64) (*Session, error)
}

// Production: Real client
type httpBlockchainClient struct { /* ... */ }

// Testing: Simple mock
type mockBlockchainClient struct {
    getSessionFunc func(context.Context, int64) (*Session, error)
}
```

2. **Use testcontainers for Integration:**
```go
// GOOD: Testcontainers for integration tests (not unit)
func TestIntegration(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test")
    }

    ctx := context.Background()
    redisContainer, _ := testcontainers.GenericContainer(ctx, ...)
    // Real Redis in Docker, cleaned up after test
}
```

**Migration Path:**
- Phase 1 (Foundation): Identify all external dependencies (blockchain RPC, CometBFT)
- Phase 2 (Core Components): Extract interfaces for mockable boundaries
- Phase 4 (Integration): Use testcontainers for true integration tests

**References:**
- [Testcontainers for Go](https://golang.testcontainers.org/)
- [Integration testing for Go applications using Testcontainers](https://dev.to/abhirockzz/integration-testing-for-go-applications-using-testcontainers-and-containerized-databases-3bfp)

---

### Pitfall 3.3: Interface Pollution

**Description:**
Creating large interfaces "for testing" forces every mock/fake to implement dozens of methods, even if only 2 are used.

**Warning Signs:**
```go
// BAD: Fat interface
type CacheManager interface {
    GetApplication(ctx, addr) (*App, error)
    SetApplication(ctx, addr, *App) error
    InvalidateApplication(ctx, addr) error
    ListApplications(ctx) ([]*App, error)
    GetService(ctx, id) (*Service, error)
    SetService(ctx, id, *Service) error
    // ... 20 more methods
}

// Now every test needs a mock with 26 methods
type mockCache struct {
    // Must implement ALL methods even if test uses 1
}
```

**Prevention Strategy:**

1. **Interface Segregation:**
```go
// GOOD: Small, focused interfaces
type ApplicationGetter interface {
    GetApplication(ctx context.Context, addr string) (*App, error)
}

type ServiceGetter interface {
    GetService(ctx context.Context, id string) (*Service, error)
}

// Test only mocks what it needs
type mockAppGetter struct {
    app *App
}
func (m *mockAppGetter) GetApplication(...) (*App, error) {
    return m.app, nil
}
```

2. **Discover Interfaces at Use Site:**
```go
// GOOD: Define interface where it's consumed, not where it's implemented
// In consumer package (relayer/)
type sessionQuerier interface {
    QuerySession(ctx context.Context, ...) (*Session, error)
}

// Implementation (query/) doesn't know about interface
type SessionClient struct { /* ... */ }
func (c *SessionClient) QuerySession(...) (*Session, error) { /* ... */ }

// SessionClient automatically satisfies sessionQuerier
```

**Migration Path:**
- Phase 1 (Foundation): Audit large interfaces (>5 methods)
- Phase 2 (Core Components): Split interfaces by responsibility
- Phase 3 (Edge Cases): Move interface definitions to consumer side

**References:**
- [Mocking - Learn Go with tests](https://quii.gitbook.io/learn-go-with-tests/go-fundamentals/mocking)
- [5 Mocking Techniques for Go](https://www.myhatchpad.com/insight/mocking-techniques-for-go/)

---

## Category 4: Test Maintenance Burden

### Pitfall 4.1: Testing Implementation Details

**Description:**
Tests coupled to internal implementation (private methods, internal state) break on every refactor, even when behavior doesn't change.

**Warning Signs:**
```go
// BAD: Testing private implementation
func TestCacheInternals(t *testing.T) {
    cache := NewCache()

    // Accessing private fields via reflection
    field := reflect.ValueOf(cache).Elem().FieldByName("internalMap")
    internalMap := field.Interface().(map[string]interface{})

    if len(internalMap) != 0 {
        t.Fatal("internal map should be empty")
    }
}

// BAD: Testing internal methods
func TestPrivateMethod(t *testing.T) {
    cache := NewCache()
    // Using build tags to expose private methods
    result := cache.internalHelper()  // Only visible with //go:build test
}
```

**Why It Fails:**
- **Brittle**: Refactoring breaks tests even when behavior unchanged
- **High maintenance**: Every internal change requires test updates
- **Blocks refactoring**: Fear of breaking 100+ tests prevents improvements

**Prevention Strategy:**

1. **Test Public API Only:**
```go
// GOOD: Test observable behavior
func TestCacheBehavior(t *testing.T) {
    cache := NewCache()

    // Set value
    cache.Set("key", "value")

    // Verify via public API
    got, err := cache.Get("key")
    require.NoError(t, err)
    require.Equal(t, "value", got)
}
```

2. **Focus on Contracts:**
```go
// GOOD: Test contract, not implementation
func TestCacheEviction(t *testing.T) {
    cache := NewCache(MaxSize: 2)

    cache.Set("key1", "value1")
    cache.Set("key2", "value2")
    cache.Set("key3", "value3")  // Triggers eviction

    // Don't care HOW it evicts (LRU? FIFO? random?)
    // Only care that size constraint is maintained
    require.LessOrEqual(t, cache.Len(), 2)
}
```

3. **Behavioral Tests:**
```go
// GOOD: Test what system does, not how
func TestRelayProcessing(t *testing.T) {
    // Given: Valid relay request
    request := validRelayRequest()

    // When: Process relay
    response, err := processor.Process(request)

    // Then: Response is signed and valid
    require.NoError(t, err)
    require.NotEmpty(t, response.Signature)
    require.True(t, verifySignature(response))

    // Don't test: Which signer was used, internal caching, metric recording
}
```

**Migration Path:**
- Phase 2 (Core Components): Refactor tests to use public APIs only
- Phase 3 (Edge Cases): Remove reflection-based tests
- Phase 5 (Validation): Measure test-to-production-code coupling ratio

**References:**
- [Refactoring Checklist - Learn Go with tests](https://quii.gitbook.io/learn-go-with-tests/testing-fundamentals/refactoring-checklist)

---

### Pitfall 4.2: Copy-Paste Test Proliferation

**Description:**
Duplicating test code across hundreds of tests instead of using table-driven tests or test helpers.

**Current Status in Codebase:**
⚠️ **MODERATE**: Some duplication in query tests, good table-driven patterns in others

**Warning Signs:**
```go
// BAD: Copy-paste tests (64+ nearly identical tests)
func TestGetApplication_Success(t *testing.T) {
    setup()
    app, err := client.GetApplication(ctx, "addr1")
    require.NoError(t, err)
    require.NotNil(t, app)
    cleanup()
}

func TestGetApplication_NotFound(t *testing.T) {
    setup()
    app, err := client.GetApplication(ctx, "addr2")
    require.Error(t, err)
    require.Nil(t, app)
    cleanup()
}

// ... 62 more similar tests
```

**Prevention Strategy:**

1. **Table-Driven Tests:**
```go
// GOOD: Table-driven pattern
func TestGetApplication(t *testing.T) {
    tests := []struct {
        name    string
        addr    string
        wantErr bool
    }{
        {"success", "addr1", false},
        {"not found", "addr2", true},
        {"invalid address", "invalid", true},
        {"empty address", "", true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            app, err := client.GetApplication(ctx, tt.addr)
            if tt.wantErr {
                require.Error(t, err)
                require.Nil(t, app)
            } else {
                require.NoError(t, err)
                require.NotNil(t, app)
            }
        })
    }
}
```

2. **Test Helpers:**
```go
// GOOD: Shared setup helpers (current pattern)
func (s *RedisSMSTTestSuite) createTestRedisStore(sessionID string) *RedisMapStore {
    store := NewRedisMapStore(s.ctx, s.redisClient, sessionID)
    redisStore, ok := store.(*RedisMapStore)
    s.Require().True(ok, "should return *RedisMapStore")
    return redisStore
}
```

**Migration Path:**
- Phase 2 (Core Components): Convert repetitive tests to table-driven
- Phase 3 (Edge Cases): Extract common setup/teardown into helpers
- Phase 5 (Validation): Measure test code reduction (target: 30% reduction)

---

### Pitfall 4.3: Test Data Builders Hell

**Description:**
Complex test data builders with dozens of chained methods create maintenance burden and obscure test intent.

**Warning Signs:**
```go
// BAD: Over-engineered builder
relay := NewRelayBuilder().
    WithSessionID("session1").
    WithApplicationAddress("app1").
    WithServiceID("svc1").
    WithPayload([]byte("data")).
    WithSignature(sig).
    WithBlockHeight(100).
    WithSupplierAddress("sup1").
    WithComputeUnits(10).
    WithRpcType(protocol.RPCType_JSON_RPC).
    WithMetadata(map[string]string{"key": "val"}).
    Build()
```

**Prevention Strategy:**

1. **Simple Factory Functions:**
```go
// GOOD: Simple factory with defaults
func validRelayRequest() *RelayRequest {
    return &RelayRequest{
        SessionID: "default-session",
        AppAddr:   "default-app",
        ServiceID: "default-service",
        Payload:   []byte("default-payload"),
        // Reasonable defaults
    }
}

// Override only what test needs
func TestSpecialCase(t *testing.T) {
    req := validRelayRequest()
    req.ServiceID = "special-service"
    // ...
}
```

2. **Functional Options (for complex cases):**
```go
// GOOD: Functional options for flexibility
func newTestRelay(opts ...func(*RelayRequest)) *RelayRequest {
    r := validRelayRequest()
    for _, opt := range opts {
        opt(r)
    }
    return r
}

func withServiceID(id string) func(*RelayRequest) {
    return func(r *RelayRequest) { r.ServiceID = id }
}

// Use: Clean and clear
relay := newTestRelay(
    withServiceID("special"),
    withBlockHeight(200),
)
```

**Migration Path:**
- Phase 2 (Core Components): Create simple factory functions for common test data
- Phase 3 (Edge Cases): Use functional options for complex scenarios only

---

## Category 5: Refactoring Without Adequate Coverage

### Pitfall 5.1: "Big Bang" Refactoring

**Description:**
Rewriting large portions of code in one PR without incremental safety nets leads to regressions and impossible code review.

**Warning Signs:**
```bash
# BAD: 5000+ line refactor
git diff main
# 87 files changed, 3421 insertions(+), 2890 deletions(-)
```

**Why It Fails:**
- **Review paralysis**: No reviewer can verify 3000+ lines
- **Hidden regressions**: Small behavior changes go unnoticed
- **Merge conflicts**: Long-lived branch diverges from main
- **Rollback impossible**: Can't isolate what broke

**Prevention Strategy:**

1. **Strangler Fig Pattern:**
```go
// GOOD: Gradual migration
func ProcessRelay(req *Relay) error {
    if useNewPath() {
        return processRelayV2(req)
    }
    return processRelayV1(req)  // Old path still works
}

// Feature flag controls rollout
func useNewPath() bool {
    return os.Getenv("USE_NEW_RELAY_PROCESSOR") == "true"
}
```

2. **Branch by Abstraction:**
```go
// GOOD: Introduce interface, swap implementations
type RelayProcessor interface {
    Process(*Relay) error
}

// Old implementation behind interface
type legacyProcessor struct { /* ... */ }

// New implementation
type modernProcessor struct { /* ... */ }

// Swap via config
var processor RelayProcessor
if config.UseLegacyProcessor {
    processor = &legacyProcessor{}
} else {
    processor = &modernProcessor{}
}
```

3. **Small PRs with Tests:**
```bash
# GOOD: Incremental refactoring
PR #1: Extract interface (50 lines, no behavior change)
PR #2: Add tests for existing behavior (200 lines)
PR #3: Refactor implementation behind interface (300 lines)
PR #4: Optimize (100 lines)
PR #5: Remove old code (50 lines deleted)
```

**Migration Path:**
- Phase 1 (Foundation): Document refactoring plan with milestones
- Phase 2-4 (Incremental): Each phase is independently reviewable
- Phase 5 (Validation): Remove feature flags, cleanup

**References:**
- [Refactoring Checklist - Learn Go with tests](https://quii.gitbook.io/learn-go-with-tests/testing-fundamentals/refactoring-checklist)
- [4 tips to refactor a complex legacy app without automation tools](https://understandlegacycode.com/blog/4-tips-refactor-without-tools/)
- [Would You Refactor Without Tests?](https://medium.com/codetodeploy/would-you-refactor-without-tests-this-is-why-you-have-trust-issues-7ce54fbcec2c)

---

### Pitfall 5.2: Refactoring Without Characterization Tests

**Description:**
Changing code without first capturing current behavior in tests. No way to detect if refactor changed semantics.

**Warning Signs:**
```bash
# BAD: Refactor with <50% coverage
go test -cover ./miner/
ok      github.com/.../miner    0.123s  coverage: 42.3% of statements

# Start refactoring anyway...
```

**Prevention Strategy:**

1. **Add Characterization Tests First:**
```go
// GOOD: Capture current behavior (even if weird)
func TestCurrentBehavior_SessionProcessing(t *testing.T) {
    // Document existing behavior before refactoring
    // Even if behavior seems wrong, test captures it
    session := createSession()
    result := processSession(session)

    // This is how it works NOW (might change later)
    require.Equal(t, "weird-behavior", result.Status)
}
```

2. **Golden Tests for Complex Output:**
```go
// GOOD: Golden file captures exact current output
func TestCurrentOutput(t *testing.T) {
    result := complexFunction()

    // First run: Update golden file
    // go test -update-golden

    // Subsequent runs: Compare to golden
    golden := filepath.Join("testdata", "golden.json")
    if *updateGolden {
        os.WriteFile(golden, result, 0644)
    }

    expected, _ := os.ReadFile(golden)
    require.Equal(t, expected, result)
}
```

3. **Coverage Threshold in CI:**
```yaml
# GOOD: Prevent coverage regression
- name: Test Coverage
  run: |
    go test -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out | grep total | awk '{print $3}' | sed 's/%//' > coverage.txt
    COVERAGE=$(cat coverage.txt)
    if (( $(echo "$COVERAGE < 80.0" | bc -l) )); then
      echo "Coverage $COVERAGE% is below threshold 80%"
      exit 1
    fi
```

**Migration Path:**
- Phase 1 (Foundation): Add characterization tests for all refactor targets
- Phase 2 (Core Components): Increase coverage to 80%+ before refactoring
- Phase 3 (Edge Cases): Golden tests for complex behaviors
- Phase 5 (Validation): Remove characterization tests that no longer apply

**References:**
- [What's the difference between Regression Tests, Characterization Tests, and Approval Tests?](https://understandlegacycode.com/blog/characterization-tests-or-approval-tests/)
- [Testing considerations when refactoring legacy code](https://www.qa-systems.com/blog/testing-considerations-when-refactoring-or-redesigning-your-legacy-code/)

---

### Pitfall 5.3: Trusting IDE Refactorings Blindly

**Description:**
Automated refactors (rename, extract method) can introduce subtle bugs that tests don't catch.

**Warning Signs:**
```go
// Before IDE refactor
func (c *Cache) Get(key string) (interface{}, error) {
    return c.get(key, true)
}

// After IDE "Extract Method"
func (c *Cache) Get(key string) (interface{}, error) {
    return c.performGet(key)  // Bug: lost 'true' parameter
}

func (c *Cache) performGet(key string) (interface{}, error) {
    return c.get(key, false)  // Default to false
}
```

**Prevention Strategy:**

1. **Always Run Tests After IDE Refactor:**
```bash
# GOOD: Verify after every refactor
git diff  # Review changes
go test -race ./...
go vet ./...
```

2. **Benchmark-Driven Refactoring:**
```bash
# GOOD: Ensure refactor didn't break performance
go test -bench=. -benchmem ./miner/ > before.txt
# ... refactor ...
go test -bench=. -benchmem ./miner/ > after.txt
benchstat before.txt after.txt
```

3. **Small Commits:**
```bash
# GOOD: Isolate refactor changes
git commit -m "refactor: rename get to performGet"
# Test, verify, then move on
```

**Migration Path:**
- Apply throughout all phases: Verify after every automated refactor
- Phase 5 (Validation): Benchmark comparison pre/post refactoring

**References:**
- [Comparing 2 approaches of refactoring untested code](https://understandlegacycode.com/blog/comparing-two-approaches-refactoring-untested-code/)
- [Another way of refactoring untested code](https://understandlegacycode.com/blog/another-way-of-refactoring-untested-code/)

---

## Category 6: Integration Test Environment Issues

### Pitfall 6.1: "Works on My Machine" Syndrome

**Description:**
Integration tests pass locally but fail in CI due to environment differences (ports, permissions, Docker versions).

**Current Status in Codebase:**
✅ **GOOD**: Using Tilt with Kubernetes for consistent local environment

**Warning Signs:**
```bash
# BAD: Hardcoded localhost
redis.NewClient(&redis.Options{
    Addr: "localhost:6379",
})

# BAD: Assuming Docker daemon is running
docker run redis:7.2

# Fails in CI: docker: command not found
```

**Prevention Strategy:**

1. **Dynamic Port Allocation:**
```go
// GOOD: Let OS choose available port
listener, _ := net.Listen("tcp", "127.0.0.1:0")
port := listener.Addr().(*net.TCPAddr).Port
listener.Close()

// Use port for test server
```

2. **Testcontainers (OS-agnostic):**
```go
// GOOD: Works in any Docker environment
func setupRedis(t *testing.T) string {
    ctx := context.Background()

    req := testcontainers.ContainerRequest{
        Image:        "redis:7.2",
        ExposedPorts: []string{"6379/tcp"},
        WaitingFor:   wait.ForLog("Ready to accept connections"),
    }

    container, _ := testcontainers.GenericContainer(ctx, req)
    t.Cleanup(func() { container.Terminate(ctx) })

    host, _ := container.Host(ctx)
    port, _ := container.MappedPort(ctx, "6379")

    return fmt.Sprintf("%s:%s", host, port.Port())
}
```

3. **Miniredis for Unit Tests:**
```go
// GOOD: No Docker dependency (current pattern)
func TestWithMiniredis(t *testing.T) {
    mr := miniredis.RunT(t)  // Pure Go, no Docker
    client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
    // ...
}
```

**Migration Path:**
- Phase 1 (Foundation): Standardize test environment setup
- Phase 4 (Integration): Migrate integration tests to testcontainers
- Phase 5 (Validation): Run tests in CI with same containers

**References:**
- [GitHub - alicebob/miniredis](https://github.com/alicebob/miniredis)
- [Testcontainers for Go](https://golang.testcontainers.org/)
- [Redis - Testcontainers for Go](https://golang.testcontainers.org/modules/redis/)

---

### Pitfall 6.2: Test Data Pollution Across Runs

**Description:**
Integration tests don't clean up data, causing failures when run multiple times or in parallel.

**Current Status in Codebase:**
✅ **GOOD**: Using `SetupTest()` with `FlushAll()`:
```go
func (s *RedisSMSTTestSuite) SetupTest() {
    s.miniRedis.FlushAll()  // Clean slate for each test
}
```

**Warning Signs:**
```go
// BAD: No cleanup
func TestCreateUser(t *testing.T) {
    db.Create(User{ID: "test-user-1"})
    // User remains in DB
}

func TestCreateUser_Again(t *testing.T) {
    db.Create(User{ID: "test-user-1"})  // Fails: already exists
}
```

**Prevention Strategy:**

1. **Always Cleanup:**
```go
// GOOD: Cleanup after test
func TestCreateUser(t *testing.T) {
    user := User{ID: "test-user-1"}
    db.Create(user)

    t.Cleanup(func() {
        db.Delete(user)
    })
}
```

2. **Unique Test IDs:**
```go
// GOOD: Unique IDs per test
func TestCreateUser(t *testing.T) {
    userID := fmt.Sprintf("test-user-%s", t.Name())
    db.Create(User{ID: userID})
    // No collision with other tests
}
```

3. **Transaction Rollback:**
```go
// GOOD: Database transactions
func TestInTransaction(t *testing.T) {
    tx := db.Begin()
    t.Cleanup(func() { tx.Rollback() })

    tx.Create(User{ID: "test-user-1"})
    // Rolled back automatically
}
```

**Migration Path:**
- Phase 2 (Core Components): Audit cleanup patterns
- Phase 4 (Integration): Ensure all integration tests clean up
- Phase 5 (Validation): Run test suite 10x to verify no pollution

---

### Pitfall 6.3: Slow Integration Tests Without Isolation

**Description:**
Running full integration suite for every change slows development. No way to run fast unit tests separately.

**Current Status in Codebase:**
✅ **GOOD**: Using build tags and short flag:
```go
//go:build test

// Integration test with skip
func TestIntegration(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test")
    }
    // ...
}
```

**Warning Signs:**
```bash
# BAD: All tests take 5+ minutes
go test ./...
# ... wait 5 minutes ...

# Can't run just fast unit tests
```

**Prevention Strategy:**

1. **Use Build Tags:**
```go
//go:build integration

package miner_test

func TestFullIntegration(t *testing.T) {
    // Only runs with: go test -tags integration
}
```

```bash
# Fast unit tests (no integration)
go test ./...

# Full suite including integration
go test -tags integration ./...
```

2. **Use -short Flag:**
```go
// GOOD: Skip slow tests in short mode
func TestSlowIntegration(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping in short mode")
    }
    // Slow integration logic
}
```

```bash
# Fast unit tests
go test -short ./...

# Full suite
go test ./...
```

3. **Separate Packages:**
```
miner/
  logic.go
  logic_test.go          # Fast unit tests
  integration_test.go    # Slow integration tests
```

**Migration Path:**
- Phase 1 (Foundation): Tag all integration tests
- Phase 4 (Integration): Separate unit and integration in CI
- Phase 5 (Validation): Measure test suite speed (target: <30s for unit tests)

**References:**
- [Go Integration Tests using Testcontainers](https://dev.to/remast/go-integration-tests-using-testcontainers-9o5)

---

## Phase Assignment Matrix

| Pitfall | Foundation | Core Components | Edge Cases | Integration | Validation |
|---------|-----------|----------------|-----------|-------------|-----------|
| **1.1 time.Sleep()** | Audit | Fix miner/cache | Fix observability/client | - | Verify no sleeps |
| **1.2 Shared State** | Audit globals | Suite isolation | - | - | Run in parallel |
| **1.3 Non-Deterministic Data** | Audit rand/time | Fix session IDs | - | - | 100x runs |
| **2.1 Missing -race** | Add test-race | - | - | CI integration | Pre-commit hook |
| **2.2 Unprotected Mocks** | Audit mocks | - | - | - | - |
| **2.3 Channel Deadlocks** | - | Review channels | Add timeouts | - | - |
| **3.1 Over-Mocking** | Document strategy | Replace with real impls | - | Contract tests | - |
| **3.2 Under-Mocking** | Identify externals | Extract interfaces | - | Testcontainers | - |
| **3.3 Interface Pollution** | Audit interfaces | Split interfaces | Move to consumers | - | - |
| **4.1 Implementation Tests** | - | Public API only | Remove reflection | - | Coupling ratio |
| **4.2 Copy-Paste** | - | Table-driven | Extract helpers | - | 30% reduction |
| **4.3 Builder Hell** | - | Factory functions | Functional options | - | - |
| **5.1 Big Bang** | Plan milestones | Incremental PRs | Incremental PRs | Incremental PRs | Remove flags |
| **5.2 No Characterization** | Add char tests | 80%+ coverage | Golden tests | - | Remove old tests |
| **5.3 Blind IDE Trust** | - | Verify always | Verify always | Verify always | Benchmarks |
| **6.1 Works on My Machine** | Std env setup | - | - | Testcontainers | CI same as local |
| **6.2 Data Pollution** | - | Audit cleanup | - | All tests cleanup | 10x runs |
| **6.3 Slow Tests** | Tag integration | - | - | Separate CI jobs | <30s unit tests |

---

## Critical Warnings for Roadmap

### RED FLAGS (Block Progress)

1. **Flaky Tests Introduced**: If any test fails once in 100 runs, STOP and fix before continuing
   - Detection: Run `for i in {1..100}; do go test ./...; done`
   - Resolution: Eliminate time.Sleep(), add proper synchronization

2. **Race Detector Failures**: Any `-race` warning must be fixed immediately
   - Detection: `go test -race ./...` must pass 100%
   - Resolution: Add mutexes, use atomic operations, or redesign concurrency

3. **Coverage Regression**: Coverage drops below current baseline
   - Detection: CI coverage check fails
   - Resolution: Add tests before merging refactor

4. **Performance Regression**: >10% slowdown in benchmarks
   - Detection: `benchstat before.txt after.txt`
   - Resolution: Profile and optimize before merging

### YELLOW FLAGS (Address Soon)

1. **Mock Drift**: Mocks not updated when real implementation changes
   - Prevention: Contract tests between mock and real

2. **Test Maintenance Toil**: >50% of PR is test updates
   - Prevention: Test public behavior, not implementation

3. **Integration Test Pollution**: Tests fail on second run
   - Prevention: Always cleanup, use unique IDs

---

## Recommended Tools

### Testing Tools
- `testify/suite`: Structured test organization ✅ (already using)
- `testify/require`: Assertions ✅ (already using)
- `miniredis`: In-memory Redis ✅ (already using)
- `testcontainers-go`: Docker-based integration tests (recommended for Phase 4)
- `testing/synctest`: Deterministic concurrency (Go 1.25+ - consider adoption)

### Quality Gates
- `go test -race`: Race detector ✅ (already using for miner)
- `go test -cover`: Coverage tracking ✅ (already using)
- `go test -bench`: Performance benchmarks ✅ (already using)
- `benchstat`: Benchmark comparison (recommended)
- `go vet`: Static analysis ✅ (already using)

### CI Integration
- Pre-commit hooks: Race detection, formatting
- CI jobs: Unit tests (fast), integration tests (slow), race detection
- Coverage thresholds: 80%+ for new code
- Benchmark regression detection: ±10% threshold

---

## Success Metrics

### Test Quality
- ✅ Zero flaky tests (100/100 runs pass)
- ✅ Zero race warnings (`go test -race` clean)
- ✅ 80%+ coverage on critical paths (miner, cache, relayer)
- ✅ <30s unit test suite execution
- ✅ <5min full integration suite

### Refactoring Safety
- ✅ Characterization tests before refactoring
- ✅ No "big bang" PRs (max 500 lines changed)
- ✅ Benchmark comparison (no >10% regression)
- ✅ All tests passing after each commit

### Maintenance Burden
- ✅ 30% reduction in test code via table-driven tests
- ✅ <10 minutes to onboard new contributor to test patterns
- ✅ Public API tests only (no reflection/build tags for accessing internals)

---

## Resources

### Go Testing Best Practices
- [Learn Go with tests - Anti-patterns](https://quii.gitbook.io/learn-go-with-tests/meta/anti-patterns)
- [Learn Go with tests - Working without mocks](https://quii.gitbook.io/learn-go-with-tests/testing-fundamentals/working-without-mocks)
- [Learn Go with tests - Refactoring Checklist](https://quii.gitbook.io/learn-go-with-tests/testing-fundamentals/refactoring-checklist)
- [Data Race Detector - Go Documentation](https://go.dev/doc/articles/race_detector)

### Flaky Tests & Race Conditions
- [Flaky Tests from Race Conditions - Root Causes and Fixes](https://devassure.medium.com/flaky-tests-from-race-conditions-root-causes-and-fixes-eb345bb0c39f)
- [Go race conditions testing and coverage](https://www.foomo.org/blog/go-race-conditions-testing-and-coverage)
- [Flaky Tests in 2026: Key Causes, Fixes, and Prevention](https://www.accelq.com/flaky-tests/)
- [Why AI won't save your Flaky Tests](https://thetestingpirate.be/posts/2026/2026-01-28_whyaiwontsaveyourflakytests/)
- [FOSDEM 2026 - Concurrency + Testing = synctest](https://fosdem.org/2026/schedule/event/BDH7G7-go-testing-synctest/)

### Mocking Best Practices
- [Your Go tests probably don't need a mocking library](https://rednafi.com/go/mocking-libraries-bleh/)
- [Unit Testing Anti-Patterns, Full List](https://www.yegor256.com/2018/12/11/unit-testing-anti-patterns.html)
- [5 Mocking Techniques for Go](https://www.myhatchpad.com/insight/mocking-techniques-for-go/)

### Refactoring Without Breaking
- [Comparing 2 approaches of refactoring untested code](https://understandlegacycode.com/blog/comparing-two-approaches-refactoring-untested-code/)
- [Testing considerations when refactoring legacy code](https://www.qa-systems.com/blog/testing-considerations-when-refactoring-or-redesigning-your-legacy-code/)
- [Would You Refactor Without Tests?](https://medium.com/codetodeploy/would-you-refactor-without-tests-this-is-why-you-have-trust-issues-7ce54fbcec2c)
- [4 tips to refactor a complex legacy app without automation tools](https://understandlegacycode.com/blog/4-tips-refactor-without-tools/)
- [What's the difference between Regression Tests, Characterization Tests, and Approval Tests?](https://understandlegacycode.com/blog/characterization-tests-or-approval-tests/)

### Integration Testing
- [GitHub - alicebob/miniredis: Pure Go Redis server for Go unittests](https://github.com/alicebob/miniredis)
- [Testcontainers for Go](https://golang.testcontainers.org/)
- [Redis - Testcontainers for Go](https://golang.testcontainers.org/modules/redis/)
- [Integration testing for Go applications using Testcontainers](https://dev.to/abhirockzz/integration-testing-for-go-applications-using-testcontainers-and-containerized-databases-3bfp)
- [Go Integration Tests using Testcontainers](https://dev.to/remast/go-integration-tests-using-testcontainers-9o5)

---

**Next Steps:**
1. Review pitfalls with team
2. Prioritize fixes in roadmap phases
3. Establish quality gates in CI
4. Document test patterns guide
5. Set up pre-commit hooks for race detection

---

*Generated: 2026-02-02*
*For: pocket-relay-miner testing and refactoring milestone*
