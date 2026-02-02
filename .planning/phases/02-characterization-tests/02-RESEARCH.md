# Phase 2: Characterization Tests - Research

**Researched:** 2026-02-02
**Domain:** Go Testing for State Machines (miner/, relayer/, cache/)
**Confidence:** HIGH

## Summary

This phase focuses on adding comprehensive test coverage for three large state machine files: `lifecycle_callback.go` (1898 lines), `session_lifecycle.go` (1207 lines), and `proxy.go` (1842 lines). The research examined existing test patterns in the codebase, the testing infrastructure established in Phase 1, and Go testing best practices for concurrent state machines.

The codebase already has excellent test infrastructure with miniredis-based suite patterns, testify assertions, and production-quality concurrency tests passing the race detector. The key challenge is testing complex state machines with multiple dependencies while maintaining Rule #1 compliance: no flaky tests, no race conditions, no mock/fake tests.

**Primary recommendation:** Extend the existing `RedisSMSTTestSuite` pattern to create `LifecycleTestSuite`, `SessionLifecycleTestSuite`, and `ProxyTestSuite` that share miniredis instances and use real implementations with deterministic test data.

## Standard Stack

The established libraries/tools for this domain:

### Core Testing Libraries (Already in go.mod)
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `testify/suite` | v1.11.1 | Test organization, lifecycle hooks | Used in existing miner tests |
| `testify/require` | v1.11.1 | Fast-fail assertions | Used throughout codebase |
| `miniredis/v2` | v2.35.0 | In-memory Redis for tests | Rule #1 compliant - real Redis semantics |
| `zerolog` | v1.34.0 | Logging (use Nop() in tests) | Already integrated in test helpers |

### Supporting (Already Available)
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `sync.WaitGroup` | stdlib | Concurrency synchronization | All concurrent tests |
| `sync/atomic` | stdlib | Thread-safe counters | Mock call tracking |
| `context` | stdlib | Cancellation, timeouts | Test timeout control |
| `pond/v2` | v2.6.0 | Worker pool testing | Testing pool behavior |

### Not Needed
| Instead of | Avoid | Reason |
|------------|-------|--------|
| gomock | Mock interfaces | Rule #1: no mock/fake tests |
| testcontainers | Docker-based Redis | miniredis is faster, deterministic |
| go-fuzz | Fuzzing | Outside Phase 2 scope |

**Installation:** No new dependencies needed - all libraries already in go.mod.

## Architecture Patterns

### Recommended Test Project Structure
```
miner/
  lifecycle_callback.go
  lifecycle_callback_test.go         # Existing (deduplicator tests)
  lifecycle_callback_states_test.go  # NEW: State transition tests
  session_lifecycle.go
  session_lifecycle_test.go          # NEW: Session state machine tests
  session_lifecycle_concurrent_test.go # NEW: Concurrency tests
  redis_smst_utils_test.go           # Existing (test suite pattern)
  testdata/                          # NEW: Deterministic test fixtures

relayer/
  proxy.go
  proxy_test.go                      # NEW: HTTP/WebSocket transport tests
  proxy_grpc_test.go                 # NEW: gRPC transport tests
  proxy_streaming_test.go            # NEW: SSE/NDJSON streaming tests
  relay_validator_test.go            # NEW: Validation path tests

cache/
  # Lower priority per CONTEXT.md - defer to later
```

### Pattern 1: Suite-Based Test Organization
**What:** Use testify/suite for stateful tests that need shared setup (miniredis, mocks, etc.)
**When to use:** Any test requiring Redis, shared state, or complex setup
**Example:**
```go
// Source: miner/redis_smst_utils_test.go (existing pattern)
type LifecycleCallbackTestSuite struct {
    suite.Suite
    miniRedis   *miniredis.Miniredis
    redisClient *redisutil.Client
    ctx         context.Context

    // Test dependencies
    smstManager *mockSMSTManager
    sessionStore SessionStore
}

func (s *LifecycleCallbackTestSuite) SetupSuite() {
    mr, err := miniredis.Run()
    s.Require().NoError(err)
    s.miniRedis = mr
    // ... create real dependencies
}

func (s *LifecycleCallbackTestSuite) SetupTest() {
    s.miniRedis.FlushAll() // Isolation between tests
}
```

### Pattern 2: State Transition Tables
**What:** Table-driven tests for state machine transitions
**When to use:** Testing SessionState transitions in session_lifecycle.go
**Example:**
```go
func (s *SessionLifecycleTestSuite) TestStateTransitions() {
    tests := []struct {
        name          string
        initialState  SessionState
        currentHeight int64
        expectedState SessionState
        expectAction  string
    }{
        {
            name:          "active to claiming when claim window opens",
            initialState:  SessionStateActive,
            currentHeight: 110, // Assuming claim window opens at 110
            expectedState: SessionStateClaiming,
            expectAction:  "claim_window_open",
        },
        {
            name:          "claimed to proving when proof window opens",
            initialState:  SessionStateClaimed,
            currentHeight: 140, // Assuming proof window opens at 140
            expectedState: SessionStateProving,
            expectAction:  "proof_window_open",
        },
        // ... more transitions
    }

    for _, tt := range tests {
        s.Run(tt.name, func() {
            // Test implementation
        })
    }
}
```

### Pattern 3: Controlled Concurrency Tests
**What:** Deterministic concurrency tests with explicit synchronization
**When to use:** Testing 500+ concurrent operations per CONTEXT.md
**Example:**
```go
func (s *SessionLifecycleTestSuite) TestHighConcurrencyStateUpdates() {
    const numGoroutines = 500
    const opsPerGoroutine = 10

    var wg sync.WaitGroup
    wg.Add(numGoroutines)

    errors := make(chan error, numGoroutines*opsPerGoroutine)

    for i := 0; i < numGoroutines; i++ {
        idx := i
        go func() {
            defer wg.Done()
            for j := 0; j < opsPerGoroutine; j++ {
                sessionID := fmt.Sprintf("session_%d_%d", idx, j)
                if err := s.manager.TrackSession(s.ctx, &SessionSnapshot{
                    SessionID: sessionID,
                    State:     SessionStateActive,
                }); err != nil {
                    errors <- err
                }
            }
        }()
    }

    wg.Wait()
    close(errors)

    for err := range errors {
        s.FailNow("unexpected error: %v", err)
    }
}
```

### Anti-Patterns to Avoid
- **time.Sleep() for synchronization:** Use channels, WaitGroups, or atomic counters instead. Violates Rule #1 (no timeout weird tests)
- **Mock interfaces:** Use real implementations with miniredis. Violates Rule #1 (no mock/fake tests)
- **Random test data:** Use deterministic seeds. Violates Rule #1 (no flaky tests)
- **Test order dependencies:** Each test must work in isolation (SetupTest flushes state)

## Don't Hand-Roll

Problems that look simple but have existing solutions:

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Redis for tests | Real Redis container | miniredis/v2 | In-process, deterministic, no network |
| Test assertions | if/panic | testify/require | Better error messages, fast-fail |
| Concurrent map tracking | map + mutex | xsync.Map | Already in codebase, lock-free |
| Mock HTTP server | Custom handler | httptest.Server | Stdlib, well-tested |
| Block height simulation | time-based waits | mockBlockClient | Deterministic height control |

**Key insight:** The codebase already has the right abstractions. The `mockBlockClient` pattern in claim_pipeline_test.go shows how to control block heights deterministically. Copy this pattern, don't invent new ones.

## Common Pitfalls

### Pitfall 1: Shared State Between Tests
**What goes wrong:** Test A modifies global state, Test B assumes clean state, Test B fails randomly depending on run order.
**Why it happens:** Prometheus metrics, global maps, or Redis data persisting across tests.
**How to avoid:**
- Use `SetupTest()` to call `miniRedis.FlushAll()` before each test
- Use unique supplier addresses/session IDs per test
- Don't rely on Prometheus metrics for assertions (they're global)
**Warning signs:** Tests pass individually but fail when run together.

### Pitfall 2: Race Conditions in Mock Tracking
**What goes wrong:** Multiple goroutines increment mock call counters without synchronization, race detector fails.
**Why it happens:** Simple `mockFoo.calls++` without mutex.
**How to avoid:**
- Use `sync.Mutex` for mock state (see existing `mockSMSTFlusher` pattern)
- Use `atomic.Int64` for simple counters
- Add getter methods that take locks
**Warning signs:** `-race` flag produces warnings, test results non-deterministic.

### Pitfall 3: Goroutine Leaks in Tests
**What goes wrong:** Tests spawn goroutines that never complete, eventually exhausting resources.
**Why it happens:** Context not cancelled, channels not closed, WaitGroups not decremented.
**How to avoid:**
- Always use `context.WithCancel` and defer cancel
- Use `t.Cleanup()` to ensure resources are released
- Track goroutines with WaitGroups, not time.Sleep
**Warning signs:** Tests slow down over time, memory usage increases during test runs.

### Pitfall 4: Protocol-Level Test Data Requirements
**What goes wrong:** Tests create RelayRequest without proper ring signatures, validation fails.
**Why it happens:** CONTEXT.md notes: "Protocol layer has caused test issues before"
**How to avoid:**
- Use builder pattern that generates valid cryptographic material
- Create hardcoded test keys committed to repo (per CONTEXT.md decision)
- Test validation paths separately from business logic
**Warning signs:** "signature verification failed", "invalid session header" errors in tests.

### Pitfall 5: Blocking on Mock Block Heights
**What goes wrong:** `waitForBlock()` in lifecycle_callback.go blocks indefinitely because mock doesn't advance.
**Why it happens:** Real code subscribes to block events, tests don't emit them.
**How to avoid:**
- Create `mockBlockClient` with `Subscribe()` method returning controlled channel
- Emit blocks programmatically when needed
- See existing pattern in claim_pipeline_test.go
**Warning signs:** Tests hang, timeout after 30s.

## Code Examples

Verified patterns from existing codebase:

### Suite Pattern (Production-Tested)
```go
// Source: miner/redis_smst_utils_test.go:21-70
type RedisSMSTTestSuite struct {
    suite.Suite
    miniRedis   *miniredis.Miniredis
    redisClient *redisutil.Client
    ctx         context.Context
}

func (s *RedisSMSTTestSuite) SetupSuite() {
    mr, err := miniredis.Run()
    s.Require().NoError(err)
    s.miniRedis = mr
    s.ctx = context.Background()

    redisURL := fmt.Sprintf("redis://%s", mr.Addr())
    client, err := redisutil.NewClient(s.ctx, redisutil.ClientConfig{
        URL: redisURL,
    })
    s.Require().NoError(err)
    s.redisClient = client
}

func (s *RedisSMSTTestSuite) SetupTest() {
    s.miniRedis.FlushAll()
}

func (s *RedisSMSTTestSuite) TearDownSuite() {
    if s.miniRedis != nil {
        s.miniRedis.Close()
    }
    if s.redisClient != nil {
        s.redisClient.Close()
    }
}

func TestRedisSMSTTestSuite(t *testing.T) {
    suite.Run(t, new(RedisSMSTTestSuite))
}
```

### Thread-Safe Mock Pattern (Production-Tested)
```go
// Source: miner/claim_pipeline_test.go:22-62
type mockSMSTFlusher struct {
    mu           sync.Mutex
    flushFunc    func(ctx context.Context, sessionID string) ([]byte, error)
    flushCalls   int
}

func (m *mockSMSTFlusher) FlushTree(ctx context.Context, sessionID string) ([]byte, error) {
    m.mu.Lock()
    m.flushCalls++
    m.mu.Unlock()
    if m.flushFunc != nil {
        return m.flushFunc(ctx, sessionID)
    }
    return []byte("mock-root-hash"), nil
}

func (m *mockSMSTFlusher) getFlushCalls() int {
    m.mu.Lock()
    defer m.mu.Unlock()
    return m.flushCalls
}
```

### High Concurrency Pattern (Production-Tested)
```go
// Source: miner/redis_smst_manager_test.go:721-791
func (s *RedisSMSTTestSuite) TestRedisSMSTManager_Sealing_HighConcurrency() {
    manager := s.createTestRedisSMSTManager("pokt1test_sealing_high_concurrency")
    sessionID := "session_high_concurrency"

    err := manager.UpdateTree(s.ctx, sessionID, []byte("key0"), []byte("value0"), 100)
    s.Require().NoError(err)

    numUpdates := 50
    var wg sync.WaitGroup
    wg.Add(numUpdates + 1)

    updateErrors := make([]error, numUpdates)
    flushComplete := make(chan struct{})

    go func() {
        defer wg.Done()
        time.Sleep(10 * time.Millisecond) // Brief delay to let updates queue
        _, err := manager.FlushTree(s.ctx, sessionID)
        s.Require().NoError(err)
        close(flushComplete)
    }()

    for i := 0; i < numUpdates; i++ {
        idx := i
        go func() {
            defer wg.Done()
            time.Sleep(time.Duration(idx) * time.Millisecond) // Stagger
            err := manager.UpdateTree(s.ctx, sessionID,
                []byte(fmt.Sprintf("key%d", idx+1)),
                []byte(fmt.Sprintf("value%d", idx+1)),
                uint64((idx+1)*100))
            updateErrors[idx] = err
        }()
    }

    wg.Wait()
    <-flushComplete

    // Verify mixed success/rejection
    successCount, rejectedCount := 0, 0
    for _, err := range updateErrors {
        if err == nil {
            successCount++
        } else {
            rejectedCount++
        }
    }
    s.Require().Greater(successCount, 0)
    s.Require().Greater(rejectedCount, 0)
}
```

### Deterministic Test Data Pattern (Production-Tested)
```go
// Source: miner/redis_smst_utils_test.go:113-131
type testRelay struct {
    key    []byte
    value  []byte
    weight uint64
}

func (s *RedisSMSTTestSuite) generateTestRelays(count int, seed byte) []testRelay {
    relays := make([]testRelay, count)
    for i := 0; i < count; i++ {
        relays[i] = testRelay{
            key:    []byte(fmt.Sprintf("relay_key_%d_%d", seed, i)),
            value:  []byte(fmt.Sprintf("relay_value_%d_%d", seed, i)),
            weight: uint64((i + 1) * 100), // Deterministic: 100, 200, 300, ...
        }
    }
    return relays
}
```

## State Machine Analysis

### lifecycle_callback.go State Transitions
The file implements `SessionLifecycleCallback` interface with these critical paths:

1. **Claim Path (money path - 90%+ coverage target)**
   - `OnSessionsNeedClaim()` - Lines 423-969
   - Validates: economic viability, claim ceiling, deduplication
   - Batches claims by session end height
   - Flushes SMST, builds claim messages, submits TX
   - Critical error paths: window timeout, TX errors, insufficient time

2. **Proof Path (money path - 90%+ coverage target)**
   - `OnSessionsNeedProof()` - Lines 973-1604
   - Checks proof requirement (probabilistic proofs)
   - Generates proof from SMST
   - Similar batch/submit pattern to claims

3. **Cleanup Callbacks**
   - `OnSessionProved()`, `OnProbabilisticProved()`
   - `OnClaimWindowClosed()`, `OnClaimTxError()`
   - `OnProofWindowClosed()`, `OnProofTxError()`
   - All delete SMST trees and clean up session locks

### session_lifecycle.go State Transitions
The file manages `SessionLifecycleManager` with:

1. **State Machine** (Lines 761-837 `determineTransition()`)
   - `Active -> Claiming` when claim window opens
   - `Claiming -> Claimed` after successful claim
   - `Claimed -> Proving` when proof window opens
   - `Proving -> Proved` after successful proof
   - Timeout transitions to `ClaimWindowClosed`, `ProofWindowClosed`

2. **Block Event Handling**
   - `lifecycleCheckerEventDriven()` - subscribes to block events
   - `checkSessionTransitions()` - evaluates all sessions
   - `executeBatchedClaimTransition()` / `executeBatchedProofTransition()`

3. **Concurrency Points**
   - `activeSessions *xsync.Map` - concurrent session tracking
   - `transitionSubpool pond.Pool` - bounded worker pool

### proxy.go Transport Handling
The file handles multiple transport protocols:

1. **HTTP Path**
   - `handleRelay()` - Lines 516-1029
   - Parses RelayRequest, validates supplier, forwards to backend
   - Eager vs optimistic validation modes
   - Signs RelayResponse

2. **WebSocket Path**
   - `WebSocketHandler()` - delegated to websocket handler
   - Uses `SessionMonitor` for session timeout management

3. **gRPC Path**
   - `RelayGRPCService` - proper relay protocol over gRPC
   - `GRPCWebWrapper` - HTTP/1.1 browser support

## Test Mode Strategy

Per CONTEXT.md decision on quick tests (CI) vs thorough tests (nightly):

```go
// Recommended approach: environment-based test scaling
func getTestConcurrency() int {
    if os.Getenv("TEST_MODE") == "nightly" {
        return 1000 // Production-like scale
    }
    return 100 // Quick CI tests
}

func getTestIterations() int {
    if os.Getenv("TEST_MODE") == "nightly" {
        return 100 // Many iterations for stability
    }
    return 10 // Fast feedback
}
```

## Coverage Measurement Strategy

Per CONTEXT.md decisions:

1. **Makefile Targets**
```makefile
test-coverage:
    go test -coverprofile=coverage.out -covermode=atomic ./...
    go tool cover -html=coverage.out -o coverage.html  # HTML for local dev
    go tool cover -func=coverage.out | grep -E "^(miner|relayer|cache)/"
```

2. **CI Integration (Warning-Only)**
```yaml
- name: Check coverage
  run: |
    COVERAGE=$(go tool cover -func=coverage.out | grep total | awk '{print $3}' | tr -d '%')
    if (( $(echo "$COVERAGE < 80" | bc -l) )); then
      echo "::warning::Coverage $COVERAGE% is below 80% target"
    fi
```

## Open Questions

Things that couldn't be fully resolved:

1. **Test Key Generation**
   - What we know: CONTEXT.md specifies "hardcoded test keys committed to repo"
   - What's unclear: Best format for storing keys (PEM files, hex literals, protobuf)
   - Recommendation: Use hex-encoded private keys in a `testdata/keys.go` file with `//go:build test` tag

2. **Transport Test Isolation**
   - What we know: proxy.go handles HTTP, WebSocket, gRPC
   - What's unclear: How to test each transport in isolation without full server startup
   - Recommendation: Use httptest.Server for HTTP, implement mock gRPC streams, test WebSocket via gorilla/websocket test helpers

3. **Block Event Simulation**
   - What we know: lifecycle_callback.go uses `waitForBlock()` which subscribes to events
   - What's unclear: Exact interface to mock for controlled block emission
   - Recommendation: Create `mockBlockSubscriber` implementing the Subscribe interface

## Sources

### Primary (HIGH confidence)
- `miner/redis_smst_utils_test.go` - Authoritative test suite pattern
- `miner/claim_pipeline_test.go` - Mock patterns, concurrency testing
- `miner/redis_smst_manager_test.go` - High concurrency test examples
- `miner/lifecycle_callback_test.go` - Existing lifecycle tests to extend

### Secondary (MEDIUM confidence)
- `miner/session_store.go` - SessionState enum and transitions
- `miner/session_lifecycle.go` - State machine implementation
- `miner/lifecycle_callback.go` - Callback implementations
- `relayer/proxy.go` - Transport handling code

### Project Documentation (HIGH confidence)
- `.planning/phases/02-characterization-tests/02-CONTEXT.md` - User decisions
- `CLAUDE.md` - Rule #1 requirements, testing standards
- `go.mod` - Available dependencies

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH - All libraries already in go.mod, patterns proven in codebase
- Architecture: HIGH - Patterns extracted from production-tested code
- Pitfalls: HIGH - Based on existing test issues mentioned in CONTEXT.md and code analysis

**Research date:** 2026-02-02
**Valid until:** 30 days (stable testing infrastructure, patterns unlikely to change)
