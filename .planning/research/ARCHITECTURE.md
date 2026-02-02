# Architecture Research: Safe Refactoring Strategies for Large Go Files

## Executive Summary

This document provides incremental refactoring strategies for three large, complex files in the pocket-relay-miner codebase:

- `miner/lifecycle_callback.go` (1898 lines, 25 methods)
- `miner/session_lifecycle.go` (1207 lines, 32 methods)
- `relayer/proxy.go` (1842 lines, 27 methods)

These files contain complex state machines and fragile business logic that require careful, test-first refactoring to maintain backwards compatibility while improving maintainability and testability.

**Key Principle**: Every refactoring step must maintain working code with passing tests. No "big bang" rewrites.

---

## Current State Analysis

### File Complexity Breakdown

#### 1. `miner/lifecycle_callback.go` (1898 lines)
**Responsibilities:**
- Implements `SessionLifecycleCallback` interface (8 lifecycle event handlers)
- Claim submission orchestration (batch building, SMST flushing, transaction submission)
- Proof submission orchestration (proof building, batch assembly, transaction submission)
- Claim ceiling validation and economic viability checks
- Retry logic for failed submissions
- Session cleanup and deduplication
- Test mode environment variable handling

**Key Dependencies:**
- `SMSTManager` (FlushTree, GetTreeRoot, ProveClosest, DeleteTree)
- `pocktclient.SupplierClient` (CreateClaim, SubmitProof)
- `SessionCoordinator` (state updates)
- `ProofRequirementChecker` (probabilistic proof logic)
- `ServiceFactorProvider` (claim ceiling calculations)
- `SubmissionTracker` (debugging/audit trail)
- `Deduplicator` (cleanup)

**State Machine Complexity:**
- Manages transitions through claim/proof windows
- Handles timing spread for submissions
- Coordinates with session coordinator for state updates
- Implements complex error recovery paths

#### 2. `miner/session_lifecycle.go` (1207 lines)
**Responsibilities:**
- Monitors block heights and triggers session state transitions
- Manages session state machine (10 states: active → claiming → claimed → proving → terminal states)
- Batches sessions for claim/proof submission
- Publishes meter cleanup signals to relayers
- Implements both event-driven (Redis pub/sub) and polling modes
- Handles shared params caching and refresh
- Coordinates with lifecycle callback for actual submissions

**Key Dependencies:**
- `SessionStore` (Redis-backed session persistence)
- `SessionLifecycleCallback` (8 callback methods for state transitions)
- `client.SharedQueryClient` (blockchain params)
- `client.BlockClient` (block height monitoring)
- `PendingRelayChecker` (late relay detection)
- `MeterCleanupPublisher` (relayer notifications)

**State Machine:**
```
active → claiming → claimed → proving → [proved | probabilistic_proved]
           ↓          ↓          ↓
    [claim_window_closed | claim_tx_error | proof_window_closed | proof_tx_error]
```

#### 3. `relayer/proxy.go` (1842 lines)
**Responsibilities:**
- HTTP/gRPC server setup and lifecycle
- Request routing to backends (multiple RPC types: JSON-RPC, WebSocket, gRPC, REST)
- Streaming response handling (SSE, NDJSON)
- Relay validation and signing
- Relay metering and stake tracking
- Backend health checking integration
- Client pool management (per-service timeout profiles)
- Worker pool coordination (validation, publishing, metrics subpools)
- gRPC-web and relay protocol handler setup
- Response compression (gzip)

**Key Dependencies:**
- `HealthChecker` (backend availability)
- `transport.MinedRelayPublisher` (Redis Streams)
- `RelayValidator` (relay request validation)
- `RelayProcessor` (session verification)
- `ResponseSigner` (supplier signature)
- `cache.SupplierCache` (supplier state)
- `RelayMeter` (stake tracking)
- `RelayPipeline` (unified processing pipeline)

**Protocol Complexity:**
- Supports 5 RPC types (gRPC, WebSocket, JSON_RPC, REST, CometBFT)
- Handles streaming and non-streaming responses differently
- Manages HTTP/2 and gRPC multiplexing
- Coordinates async publishing with bounded worker pools

---

## Refactoring Principles

### 1. Test-First Incremental Approach

**Rule #1 from CLAUDE.md**: No flaky tests, no race conditions, no mock/fake tests
- All tests must pass `go test -race` without warnings
- All tests must use real implementations (miniredis for Redis)
- All tests must be deterministic (no `time.Sleep()`, no random ordering)

**Incremental Strategy:**
1. Add characterization tests BEFORE refactoring
2. Extract small, focused units with clear interfaces
3. Run tests after each extraction
4. Never break existing tests during refactoring
5. Use feature flags or build tags if temporary dual implementations needed

### 2. Interface Extraction for Testability

**Go Best Practice**: "Accept interfaces, return structs" ([Go API design](https://dev.to/shrsv/designing-go-apis-the-standard-library-way-accept-interfaces-return-structs-410k))

**Benefits:**
- Decouple components for easier testing
- Replace real implementations with test doubles
- Enable dependency injection without frameworks
- Keep interfaces small and focused (single responsibility)

**Pattern:**
```go
// Before: Hard to test (many dependencies)
type LargeStruct struct {
    dep1 *ConcreteType1
    dep2 *ConcreteType2
    dep3 *ConcreteType3
    // ... many more
}

// After: Easy to test (narrow interfaces)
type Refactored struct {
    behavior1 BehaviorInterface1  // 2-3 methods max
    behavior2 BehaviorInterface2  // 2-3 methods max
}

type BehaviorInterface1 interface {
    DoThing() error
}
```

### 3. Dependency Injection via Constructor

**Go Idiomatic Pattern**: Constructor functions with explicit dependencies ([Go DI best practices](https://www.glukhov.org/post/2025/12/dependency-injection-in-go/))

**Benefits:**
- Clear dependency graph
- Compile-time safety (missing dependencies = compiler error)
- Easy to trace initialization order
- No hidden globals or singletons

**Pattern:**
```go
// Constructor with all required dependencies explicit
func NewComponent(
    logger logging.Logger,
    behavior1 BehaviorInterface1,
    behavior2 BehaviorInterface2,
    config ComponentConfig,
) *Component {
    return &Component{
        logger:    logger,
        behavior1: behavior1,
        behavior2: behavior2,
        config:    config,
    }
}

// Optional dependencies via setter methods
func (c *Component) SetOptionalFeature(feature OptionalInterface) {
    c.optionalFeature = feature
}
```

### 4. State Machine Decomposition

**State Pattern**: Extract state-specific behavior into separate types ([State pattern in Go](https://refactoring.guru/design-patterns/state/go/example))

**Benefits:**
- Each state handler is independently testable
- State transitions are explicit and traceable
- Easier to add new states without touching existing logic
- Clear separation of concerns

**Pattern:**
```go
// State interface
type SessionStateHandler interface {
    Handle(ctx context.Context, session *Session) (nextState SessionState, err error)
}

// Concrete state implementations
type ActiveStateHandler struct { /* deps */ }
func (h *ActiveStateHandler) Handle(ctx context.Context, session *Session) (SessionState, error) {
    // State-specific logic
}

type ClaimingStateHandler struct { /* deps */ }
func (h *ClaimingStateHandler) Handle(ctx context.Context, session *Session) (SessionState, error) {
    // State-specific logic
}

// State machine coordinator
type StateMachine struct {
    handlers map[SessionState]SessionStateHandler
}

func (sm *StateMachine) Transition(ctx context.Context, session *Session) error {
    handler := sm.handlers[session.State]
    nextState, err := handler.Handle(ctx, session)
    if err != nil {
        return err
    }
    session.State = nextState
    return nil
}
```

---

## Refactoring Roadmap

### Phase 1: Add Characterization Tests (Week 1)

**Goal**: Establish test coverage BEFORE any refactoring

#### 1.1 Lifecycle Callback Tests
**Files to create/expand:**
- `miner/lifecycle_callback_test.go` (currently 205 lines - needs expansion)

**Test scenarios to add:**
```go
// Claim submission tests
TestOnSessionsNeedClaim_SingleSession_Success
TestOnSessionsNeedClaim_BatchMultipleSessions_Success
TestOnSessionsNeedClaim_ClaimWindowTimeout
TestOnSessionsNeedClaim_RetryLogic
TestOnSessionsNeedClaim_ClaimCeilingWarning
TestOnSessionsNeedClaim_EconomicViabilityCheck

// Proof submission tests
TestOnSessionsNeedProof_SingleSession_Success
TestOnSessionsNeedProof_BatchMultipleSessions_Success
TestOnSessionsNeedProof_ProbabilisticProof_Skipped
TestOnSessionsNeedProof_ProofWindowTimeout
TestOnSessionsNeedProof_RetryLogic

// Terminal state handlers
TestOnSessionProved_Cleanup_Success
TestOnProbabilisticProved_Cleanup_Success
TestOnClaimWindowClosed_Metrics_Updated
TestOnProofWindowClosed_Metrics_Updated
```

**Testing strategy:**
- Use miniredis for Redis operations (already used in codebase)
- Mock blockchain clients (already have mockTxClient pattern)
- Test retry logic with controlled failures
- Verify metrics increments

#### 1.2 Session Lifecycle Tests
**Files to create:**
- `miner/session_lifecycle_test.go` (currently missing)

**Test scenarios to add:**
```go
// State machine tests
TestSessionLifecycle_Active_To_Claiming_Transition
TestSessionLifecycle_Claiming_To_Claimed_Transition
TestSessionLifecycle_Claimed_To_Proving_Transition
TestSessionLifecycle_Proving_To_Proved_Transition
TestSessionLifecycle_ClaimWindowTimeout
TestSessionLifecycle_ProofWindowTimeout

// Batch processing tests
TestCheckSessionTransitions_BatchesSessions_Correctly
TestExecuteBatchedClaimTransition_Success
TestExecuteBatchedProofTransition_Success

// Event-driven vs polling
TestLifecycleCheckerEventDriven_BlockEvents
TestLifecycleCheckerPolling_PollInterval

// Cleanup
TestMeterCleanupPublisher_PublishesOnStateChange
```

#### 1.3 Proxy Tests
**Files to create:**
- `relayer/proxy_test.go` (currently missing)
- `relayer/proxy_streaming_test.go` (streaming-specific tests)

**Test scenarios to add:**
```go
// Request routing tests
TestHandleRelay_JSONRPC_RoutesToCorrectBackend
TestHandleRelay_gRPC_RoutesToCorrectBackend
TestHandleRelay_WebSocket_RoutesToCorrectBackend
TestHandleRelay_REST_RoutesToCorrectBackend

// Streaming tests
TestHandleRelay_SSE_StreamsCorrectly
TestHandleRelay_NDJSON_StreamsCorrectly
TestIsStreamingResponse_DetectsContentTypes

// Client pool tests
TestGetClientForService_ReturnsCorrectTimeoutProfile
TestBuildClientPool_CreatesClientsForAllProfiles

// Error handling tests
TestHandleRelay_BackendUnhealthy_RejectsRequest
TestHandleRelay_ValidationFailed_RejectsRequest
TestHandleRelay_MeterError_RejectsRequest
TestHandleRelay_StakeExhausted_RejectsRequest

// Lifecycle tests
TestProxyServer_Start_InitializesCorrectly
TestProxyServer_Close_CleansUpCorrectly
TestProxyServer_GracefulShutdown_WaitsForInflight
```

**Acceptance Criteria for Phase 1:**
- [ ] All new tests pass with `go test -race`
- [ ] Code coverage for target files increases to >70%
- [ ] Tests use real implementations (miniredis, not mocks)
- [ ] Tests are deterministic (no flaky failures)
- [ ] Benchmark tests added for critical paths

---

### Phase 2: Extract Stateless Utilities (Week 2)

**Goal**: Move pure functions and utilities to separate files without changing behavior

**Rationale**: Stateless functions are lowest risk to extract (no shared state, no lifecycle)

#### 2.1 Lifecycle Callback Utilities

**Extract from `lifecycle_callback.go`:**

**New file: `miner/claim_validation.go`**
```go
package miner

// Pure validation functions (no state, no side effects)
func isClaimEconomicallyViable(
    relayCount uint64,
    computeUnits uint64,
    serviceID string,
    minRelayMining uint64,
) bool { /* ... */ }

func checkClaimCeiling(
    ctx context.Context,
    sessionID string,
    serviceID string,
    appAddress string,
    computeUnits uint64,
    serviceFactor float64,
    appClient ApplicationQueryClient,
    logger logging.Logger,
) { /* ... */ }

func calculateClaimCeiling(
    appStake uint64,
    serviceFactor float64,
) uint64 { /* ... */ }
```

**New file: `miner/submission_delay.go`**
```go
package miner

// Test mode configuration
type TestConfig struct {
    ForceClaimTxError bool
    ForceProofTxError bool
    ClaimDelaySeconds int
    ProofDelaySeconds int
}

func getTestConfig() TestConfig { /* ... */ }

// Timing utilities
func calculateSubmissionDelay(
    testConfig TestConfig,
    isClaimSubmission bool,
) time.Duration { /* ... */ }
```

**Migration steps:**
1. Copy functions to new files
2. Update package-level references
3. Run tests (should still pass)
4. Delete from original file
5. Run tests again
6. Commit with message: `refactor(miner): extract claim validation utilities`

#### 2.2 Session Lifecycle Utilities

**Extract from `session_lifecycle.go`:**

**New file: `miner/session_window.go`**
```go
package miner

// SessionWindow represents claim and proof submission windows.
type SessionWindow struct {
    ClaimWindowStartHeight int64
    ClaimWindowEndHeight   int64
    ProofWindowStartHeight int64
    ProofWindowEndHeight   int64
}

func CalculateSessionWindow(
    params *sharedtypes.Params,
    sessionEndHeight int64,
) SessionWindow { /* ... */ }

func (w SessionWindow) IsInClaimWindow(currentHeight int64) bool { /* ... */ }
func (w SessionWindow) IsInProofWindow(currentHeight int64) bool { /* ... */ }
```

**New file: `miner/session_filtering.go`**
```go
package miner

// Pre-filtering optimization for transition checks
func sessionMightNeedTransition(
    snapshot *SessionSnapshot,
    currentHeight int64,
    params *sharedtypes.Params,
) bool { /* ... */ }

func filterSessionsByState(
    sessions []*SessionSnapshot,
    targetState SessionState,
) []*SessionSnapshot { /* ... */ }
```

**Migration steps:**
1. Extract window calculation logic (already somewhat isolated)
2. Extract filtering optimization
3. Update tests to cover extracted utilities
4. Commit: `refactor(miner): extract session window utilities`

#### 2.3 Proxy Server Utilities

**Extract from `proxy.go`:**

**New file: `relayer/http_utils.go`**
```go
package relayer

// HTTP utility functions
func isStreamingResponse(resp *http.Response) bool { /* ... */ }

func clientAcceptsGzip(r *http.Request) bool { /* ... */ }

func compressGzip(data []byte) ([]byte, error) { /* ... */ }

func extractServiceID(r *http.Request) string { /* ... */ }

func copyHeaders(dst, src *http.Request) { /* ... */ }
```

**New file: `relayer/client_pool.go`**
```go
package relayer

// HTTP client pool management
type ClientPool struct {
    clients   map[string]*http.Client
    clientsMu sync.RWMutex
}

func NewClientPool(services map[string]*ServiceConfig) *ClientPool { /* ... */ }

func (cp *ClientPool) GetClientForService(serviceID string) *http.Client { /* ... */ }

func buildHTTPClient(cfg *HTTPTransportConfig) *http.Client { /* ... */ }

func buildClientPool(
    services map[string]*ServiceConfig,
    httpTransportConfig *HTTPTransportConfig,
) map[string]*http.Client { /* ... */ }
```

**Migration steps:**
1. Extract HTTP utilities (pure functions)
2. Extract client pool into separate struct
3. Update ProxyServer to use ClientPool wrapper
4. Tests should still pass
5. Commit: `refactor(relayer): extract HTTP utilities and client pool`

**Acceptance Criteria for Phase 2:**
- [ ] All tests still pass with `go test -race`
- [ ] Original files reduced by 200-300 lines each
- [ ] Extracted utilities have dedicated unit tests
- [ ] No behavior changes (characterization tests confirm)
- [ ] Commits are atomic and reversible

---

### Phase 3: Extract Internal Interfaces (Week 3)

**Goal**: Define narrow interfaces for internal responsibilities, prepare for state handler extraction

**Rationale**: Interfaces enable testing and reduce coupling before extracting complex logic

#### 3.1 Lifecycle Callback Interfaces

**New file: `miner/claim_builder.go`**
```go
package miner

// ClaimBuilder handles claim construction and submission.
type ClaimBuilder interface {
    // BuildClaims builds claim messages for a batch of sessions.
    BuildClaims(ctx context.Context, snapshots []*SessionSnapshot) ([]ClaimRequest, error)

    // SubmitClaims submits a batch of claims in a single transaction.
    SubmitClaims(ctx context.Context, requests []ClaimRequest) (txHash string, err error)
}

type ClaimRequest struct {
    SessionID   string
    RootHash    []byte
    ServiceID   string
    AppAddress  string
    // ... other fields
}

// Default implementation (initially just wraps existing logic)
type DefaultClaimBuilder struct {
    logger         logging.Logger
    supplierClient pocktclient.SupplierClient
    smstManager    SMSTManager
    // ... other deps
}

func NewDefaultClaimBuilder( /* deps */ ) *DefaultClaimBuilder { /* ... */ }

func (b *DefaultClaimBuilder) BuildClaims(
    ctx context.Context,
    snapshots []*SessionSnapshot,
) ([]ClaimRequest, error) {
    // Extract claim building logic from OnSessionsNeedClaim
}

func (b *DefaultClaimBuilder) SubmitClaims(
    ctx context.Context,
    requests []ClaimRequest,
) (string, error) {
    // Extract claim submission logic from OnSessionsNeedClaim
}
```

**New file: `miner/proof_builder.go`**
```go
package miner

// ProofBuilder handles proof construction and submission.
type ProofBuilder interface {
    // BuildProofs builds proof messages for a batch of sessions.
    BuildProofs(ctx context.Context, snapshots []*SessionSnapshot) ([]ProofRequest, error)

    // SubmitProofs submits a batch of proofs in a single transaction.
    SubmitProofs(ctx context.Context, requests []ProofRequest) (txHash string, err error)
}

type ProofRequest struct {
    SessionID   string
    ProofBytes  []byte
    ServiceID   string
    AppAddress  string
    // ... other fields
}

type DefaultProofBuilder struct {
    logger         logging.Logger
    supplierClient pocktclient.SupplierClient
    smstManager    SMSTManager
    proofChecker   *ProofRequirementChecker
    // ... other deps
}

func NewDefaultProofBuilder( /* deps */ ) *DefaultProofBuilder { /* ... */ }

func (b *DefaultProofBuilder) BuildProofs( /* ... */ ) ([]ProofRequest, error) {
    // Extract proof building logic from OnSessionsNeedProof
}

func (b *DefaultProofBuilder) SubmitProofs( /* ... */ ) (string, error) {
    // Extract proof submission logic from OnSessionsNeedProof
}
```

**Refactor `LifecycleCallback`:**
```go
type LifecycleCallback struct {
    logger       logging.Logger
    config       LifecycleCallbackConfig
    claimBuilder ClaimBuilder  // NEW: Use interface instead of inline logic
    proofBuilder ProofBuilder  // NEW: Use interface instead of inline logic
    coordinator  *SessionCoordinator
    // ... other deps
}

func (lc *LifecycleCallback) OnSessionsNeedClaim(
    ctx context.Context,
    snapshots []*SessionSnapshot,
) ([][]byte, error) {
    // Simplified: Delegate to claimBuilder
    requests, err := lc.claimBuilder.BuildClaims(ctx, snapshots)
    if err != nil {
        return nil, err
    }

    txHash, err := lc.claimBuilder.SubmitClaims(ctx, requests)
    if err != nil {
        return nil, err
    }

    // Extract root hashes from requests
    rootHashes := make([][]byte, len(requests))
    for i, req := range requests {
        rootHashes[i] = req.RootHash
    }

    return rootHashes, nil
}
```

**Migration steps:**
1. Define ClaimBuilder interface
2. Create DefaultClaimBuilder with extracted logic
3. Update LifecycleCallback constructor to accept ClaimBuilder
4. Update tests to use new structure
5. Repeat for ProofBuilder
6. Commit: `refactor(miner): extract claim/proof builder interfaces`

#### 3.2 Session Lifecycle State Handlers

**New file: `miner/state_handlers.go`**
```go
package miner

// StateTransitionDeterminer decides if a session needs to transition.
type StateTransitionDeterminer interface {
    // DetermineTransition checks if a session should transition at current height.
    // Returns (shouldTransition, nextState, error).
    DetermineTransition(
        ctx context.Context,
        snapshot *SessionSnapshot,
        currentHeight int64,
        params *sharedtypes.Params,
    ) (bool, SessionState, error)
}

// DefaultStateTransitionDeterminer implements the existing transition logic.
type DefaultStateTransitionDeterminer struct {
    logger         logging.Logger
    sessionStore   SessionStore
    pendingChecker PendingRelayChecker
}

func NewDefaultStateTransitionDeterminer( /* deps */ ) *DefaultStateTransitionDeterminer { /* ... */ }

func (d *DefaultStateTransitionDeterminer) DetermineTransition(
    ctx context.Context,
    snapshot *SessionSnapshot,
    currentHeight int64,
    params *sharedtypes.Params,
) (bool, SessionState, error) {
    // Extract determineTransition logic from session_lifecycle.go:756
}
```

**Refactor `SessionLifecycleManager`:**
```go
type SessionLifecycleManager struct {
    logger              logging.Logger
    config              SessionLifecycleConfig
    sessionStore        SessionStore
    callback            SessionLifecycleCallback
    transitionDeterminer StateTransitionDeterminer  // NEW
    // ... other deps
}

func (m *SessionLifecycleManager) checkSessionTransitions(
    ctx context.Context,
    currentHeight int64,
) {
    // Simplified: Use transitionDeterminer interface
    for _, session := range m.GetActiveSessions() {
        shouldTransition, nextState, err := m.transitionDeterminer.DetermineTransition(
            ctx, session, currentHeight, m.getSharedParams(),
        )
        if err != nil {
            m.logger.Error().Err(err).Str("session_id", session.SessionID).Msg("transition check failed")
            continue
        }
        if shouldTransition {
            m.executeTransition(ctx, session, nextState)
        }
    }
}
```

**Migration steps:**
1. Define StateTransitionDeterminer interface
2. Extract determineTransition logic into DefaultStateTransitionDeterminer
3. Update SessionLifecycleManager to use interface
4. Update tests to verify transition logic independently
5. Commit: `refactor(miner): extract state transition determiner`

#### 3.3 Proxy Request Handlers

**New file: `relayer/request_processor.go`**
```go
package relayer

// RequestProcessor orchestrates relay request processing.
type RequestProcessor interface {
    // ProcessRequest validates, meters, signs, and publishes a relay.
    ProcessRequest(ctx context.Context, req *ProcessingRequest) (*ProcessingResponse, error)
}

type ProcessingRequest struct {
    HTTPRequest     *http.Request
    RequestBody     []byte
    ServiceID       string
    SupplierAddress string
    ArrivalHeight   int64
}

type ProcessingResponse struct {
    BackendResponse *http.Response
    SignedRelayResponse []byte
    ShouldPublish   bool
    PublishTask     *publishTask
}

// DefaultRequestProcessor uses the existing RelayPipeline.
type DefaultRequestProcessor struct {
    logger      logging.Logger
    pipeline    *RelayPipeline
    publisher   transport.MinedRelayPublisher
    workerPool  pond.Pool
}

func NewDefaultRequestProcessor( /* deps */ ) *DefaultRequestProcessor { /* ... */ }

func (p *DefaultRequestProcessor) ProcessRequest(
    ctx context.Context,
    req *ProcessingRequest,
) (*ProcessingResponse, error) {
    // Extract processing logic from handleRelay
}
```

**Refactor `ProxyServer`:**
```go
type ProxyServer struct {
    logger           logging.Logger
    config           *Config
    requestProcessor RequestProcessor  // NEW: Use interface
    clientPool       *ClientPool       // NEW: From Phase 2
    server           *http.Server
    // ... other deps
}

func (p *ProxyServer) handleRelay(w http.ResponseWriter, r *http.Request) {
    // Simplified: Read request, delegate to processor, write response
    body, err := io.ReadAll(r.Body)
    if err != nil {
        p.sendError(w, http.StatusBadRequest, "failed to read request body")
        return
    }

    req := &ProcessingRequest{
        HTTPRequest:     r,
        RequestBody:     body,
        ServiceID:       p.extractServiceID(r),
        SupplierAddress: p.config.SupplierAddress,
        ArrivalHeight:   p.currentBlockHeight.Load(),
    }

    resp, err := p.requestProcessor.ProcessRequest(r.Context(), req)
    if err != nil {
        p.sendError(w, http.StatusInternalServerError, err.Error())
        return
    }

    // Write response
    w.Write(resp.SignedRelayResponse)
}
```

**Migration steps:**
1. Define RequestProcessor interface
2. Extract processing logic from handleRelay into DefaultRequestProcessor
3. Update ProxyServer to use RequestProcessor
4. Update tests to verify processing independently
5. Commit: `refactor(relayer): extract request processor interface`

**Acceptance Criteria for Phase 3:**
- [ ] All tests still pass with `go test -race`
- [ ] New interfaces are narrow (2-5 methods max)
- [ ] Each interface has default implementation
- [ ] Interfaces enable easier testing (mock implementations possible)
- [ ] No behavior changes (characterization tests confirm)

---

### Phase 4: Create State Handler Packages (Week 4)

**Goal**: Extract state machine handlers into dedicated packages for isolation and testability

**Rationale**: State handlers are complex and benefit from package-level isolation

#### 4.1 Session State Handlers Package

**New package: `miner/statehandler/`**

**Structure:**
```
miner/
  statehandler/
    handler.go          # Common interface and utilities
    active.go           # Active state handler
    claiming.go         # Claiming state handler
    claimed.go          # Claimed state handler
    proving.go          # Proving state handler
    terminal.go         # Terminal state handlers
    handler_test.go     # Handler tests
```

**File: `miner/statehandler/handler.go`**
```go
package statehandler

import (
    "context"
    "github.com/pokt-network/pocket-relay-miner/miner"
)

// Handler processes a session in a specific state and returns next state.
type Handler interface {
    // Handle processes the session and returns the next state.
    // Returns nil error and same state if no transition is needed.
    Handle(ctx context.Context, session *miner.SessionSnapshot, currentHeight int64) (miner.SessionState, error)

    // CanHandle returns true if this handler can process the given state.
    CanHandle(state miner.SessionState) bool
}

// Registry maps session states to their handlers.
type Registry struct {
    handlers map[miner.SessionState]Handler
}

func NewRegistry() *Registry {
    return &Registry{
        handlers: make(map[miner.SessionState]Handler),
    }
}

func (r *Registry) Register(state miner.SessionState, handler Handler) {
    r.handlers[state] = handler
}

func (r *Registry) GetHandler(state miner.SessionState) (Handler, bool) {
    handler, ok := r.handlers[state]
    return handler, ok
}
```

**File: `miner/statehandler/active.go`**
```go
package statehandler

import (
    "context"
    "github.com/pokt-network/pocket-relay-miner/miner"
    sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
)

// ActiveStateHandler handles sessions in active state.
type ActiveStateHandler struct {
    pendingChecker miner.PendingRelayChecker
    params         *sharedtypes.Params
}

func NewActiveStateHandler(
    pendingChecker miner.PendingRelayChecker,
    params *sharedtypes.Params,
) *ActiveStateHandler {
    return &ActiveStateHandler{
        pendingChecker: pendingChecker,
        params:         params,
    }
}

func (h *ActiveStateHandler) Handle(
    ctx context.Context,
    session *miner.SessionSnapshot,
    currentHeight int64,
) (miner.SessionState, error) {
    // Check if session should transition to claiming
    window := miner.CalculateSessionWindow(h.params, session.SessionEndHeight)

    if currentHeight >= window.ClaimWindowStartHeight {
        // Check for pending relays
        pending, err := h.pendingChecker.GetPendingRelayCount(ctx, session.SessionID)
        if err != nil {
            return session.State, err
        }

        if pending > 0 {
            // Wait for pending relays
            return session.State, nil
        }

        // Transition to claiming
        return miner.SessionStateClaiming, nil
    }

    // Stay in active state
    return session.State, nil
}

func (h *ActiveStateHandler) CanHandle(state miner.SessionState) bool {
    return state == miner.SessionStateActive
}
```

**File: `miner/statehandler/claiming.go`**
```go
package statehandler

// Similar structure for claiming state:
// - Check if claim window is still open
// - Transition to claimed (handled by callback) or claim_window_closed
```

**File: `miner/statehandler/claimed.go`**
```go
package statehandler

// Similar structure for claimed state:
// - Check if proof window has started
// - Transition to proving
```

**File: `miner/statehandler/proving.go`**
```go
package statehandler

// Similar structure for proving state:
// - Check if proof window is still open
// - Transition to proved (handled by callback) or proof_window_closed
```

**Update `SessionLifecycleManager`:**
```go
import "github.com/pokt-network/pocket-relay-miner/miner/statehandler"

type SessionLifecycleManager struct {
    // ... existing fields
    stateHandlers *statehandler.Registry  // NEW
}

func NewSessionLifecycleManager( /* deps */ ) *SessionLifecycleManager {
    // ... existing init

    // Setup state handler registry
    registry := statehandler.NewRegistry()
    registry.Register(
        miner.SessionStateActive,
        statehandler.NewActiveStateHandler(pendingChecker, sharedParams),
    )
    registry.Register(
        miner.SessionStateClaiming,
        statehandler.NewClaimingStateHandler(sharedParams),
    )
    // ... register other handlers

    return &SessionLifecycleManager{
        // ... existing fields
        stateHandlers: registry,
    }
}

func (m *SessionLifecycleManager) checkSessionTransitions(
    ctx context.Context,
    currentHeight int64,
) {
    for _, session := range m.GetActiveSessions() {
        handler, ok := m.stateHandlers.GetHandler(session.State)
        if !ok {
            m.logger.Warn().Str("state", string(session.State)).Msg("no handler for state")
            continue
        }

        nextState, err := handler.Handle(ctx, session, currentHeight)
        if err != nil {
            m.logger.Error().Err(err).Str("session_id", session.SessionID).Msg("state handler failed")
            continue
        }

        if nextState != session.State {
            m.executeTransition(ctx, session, nextState)
        }
    }
}
```

**Migration steps:**
1. Create `miner/statehandler/` package
2. Implement Handler interface and Registry
3. Extract ActiveStateHandler (simplest, good starting point)
4. Write tests for ActiveStateHandler
5. Extract ClaimingStateHandler
6. Write tests for ClaimingStateHandler
7. Repeat for other state handlers
8. Update SessionLifecycleManager to use Registry
9. Verify all tests pass
10. Commit: `refactor(miner): extract state handlers to dedicated package`

**Benefits:**
- Each state handler is independently testable
- Clear separation of state transition logic
- Easy to add new states (just register a new handler)
- Reduced cyclomatic complexity in SessionLifecycleManager

#### 4.2 Proxy Transport Handlers Package

**New package: `relayer/transport/`**

**Structure:**
```
relayer/
  transport/
    handler.go          # Common interface
    jsonrpc.go          # JSON-RPC handler
    grpc.go             # gRPC handler
    websocket.go        # WebSocket handler (wrapper around existing websocket.go)
    rest.go             # REST handler
    streaming.go        # Streaming response handler (SSE, NDJSON)
    handler_test.go     # Handler tests
```

**File: `relayer/transport/handler.go`**
```go
package transport

import (
    "context"
    "net/http"
)

// TransportHandler handles relay forwarding for a specific RPC type.
type TransportHandler interface {
    // Forward forwards the relay to the backend and returns the response.
    Forward(ctx context.Context, req *ForwardRequest) (*ForwardResponse, error)

    // Supports returns true if this handler supports the given RPC type.
    Supports(rpcType string) bool
}

type ForwardRequest struct {
    HTTPRequest  *http.Request
    RequestBody  []byte
    BackendURL   string
    ServiceID    string
    Timeout      time.Duration
}

type ForwardResponse struct {
    ResponseBody []byte
    StatusCode   int
    Headers      http.Header
    IsStreaming  bool
    StreamReader io.ReadCloser  // For streaming responses
}

// Registry maps RPC types to their handlers.
type Registry struct {
    handlers map[string]TransportHandler
}

func NewRegistry() *Registry {
    return &Registry{
        handlers: make(map[string]TransportHandler),
    }
}

func (r *Registry) Register(rpcType string, handler TransportHandler) {
    r.handlers[rpcType] = handler
}

func (r *Registry) GetHandler(rpcType string) (TransportHandler, bool) {
    handler, ok := r.handlers[rpcType]
    return handler, ok
}
```

**File: `relayer/transport/jsonrpc.go`**
```go
package transport

// JSONRPCHandler handles JSON-RPC relays.
type JSONRPCHandler struct {
    client      *http.Client
    bufferPool  *BufferPool
}

func NewJSONRPCHandler(client *http.Client, bufferPool *BufferPool) *JSONRPCHandler {
    return &JSONRPCHandler{
        client:     client,
        bufferPool: bufferPool,
    }
}

func (h *JSONRPCHandler) Forward(
    ctx context.Context,
    req *ForwardRequest,
) (*ForwardResponse, error) {
    // Extract JSON-RPC forwarding logic from proxy.go
    // This is the non-streaming HTTP request path
}

func (h *JSONRPCHandler) Supports(rpcType string) bool {
    return rpcType == "jsonrpc" || rpcType == "json_rpc"
}
```

**Update `ProxyServer`:**
```go
import "github.com/pokt-network/pocket-relay-miner/relayer/transport"

type ProxyServer struct {
    // ... existing fields
    transportHandlers *transport.Registry  // NEW
}

func NewProxyServer( /* deps */ ) (*ProxyServer, error) {
    // ... existing init

    // Setup transport handler registry
    registry := transport.NewRegistry()
    registry.Register("jsonrpc", transport.NewJSONRPCHandler(httpClient, bufferPool))
    registry.Register("grpc", transport.NewGRPCHandler(grpcClient, bufferPool))
    registry.Register("rest", transport.NewRESTHandler(httpClient, bufferPool))
    // ... register other handlers

    return &ProxyServer{
        // ... existing fields
        transportHandlers: registry,
    }
}

func (p *ProxyServer) forwardToBackend(
    ctx context.Context,
    rpcType string,
    req *transport.ForwardRequest,
) (*transport.ForwardResponse, error) {
    handler, ok := p.transportHandlers.GetHandler(rpcType)
    if !ok {
        return nil, fmt.Errorf("unsupported RPC type: %s", rpcType)
    }

    return handler.Forward(ctx, req)
}
```

**Migration steps:**
1. Create `relayer/transport/` package
2. Define TransportHandler interface and Registry
3. Extract JSONRPCHandler (simplest, non-streaming)
4. Write tests for JSONRPCHandler
5. Extract RESTHandler (similar to JSON-RPC)
6. Write tests for RESTHandler
7. Extract StreamingHandler (SSE, NDJSON)
8. Write tests for StreamingHandler
9. Wrap existing WebSocketBridge in new interface
10. Update ProxyServer to use Registry
11. Verify all tests pass
12. Commit: `refactor(relayer): extract transport handlers to dedicated package`

**Acceptance Criteria for Phase 4:**
- [ ] All tests still pass with `go test -race`
- [ ] New packages have <500 lines per file
- [ ] Each handler is independently testable
- [ ] Clear separation of concerns (state logic vs transport logic)
- [ ] No behavior changes (characterization tests confirm)
- [ ] Original large files reduced by 50%+

---

### Phase 5: Introduce Builder Pattern (Week 5)

**Goal**: Simplify construction of complex structs with many optional dependencies

**Rationale**: Some structs have 10+ dependencies, making constructors unwieldy

#### 5.1 LifecycleCallback Builder

**New file: `miner/lifecycle_callback_builder.go`**
```go
package miner

// LifecycleCallbackBuilder uses the builder pattern for complex construction.
type LifecycleCallbackBuilder struct {
    logger             logging.Logger
    config             LifecycleCallbackConfig
    supplierClient     pocktclient.SupplierClient
    sharedClient       pocktclient.SharedQueryClient
    blockClient        pocktclient.BlockClient
    sessionClient      SessionQueryClient
    smstManager        SMSTManager
    sessionCoordinator *SessionCoordinator

    // Optional dependencies
    proofChecker          *ProofRequirementChecker
    serviceFactorProvider ServiceFactorProvider
    appClient             ApplicationQueryClient
    streamDeleter         StreamDeleter
    submissionTracker     *SubmissionTracker
    deduplicator          Deduplicator
    buildPool             pond.Pool
}

// NewLifecycleCallbackBuilder creates a builder with required dependencies.
func NewLifecycleCallbackBuilder(
    logger logging.Logger,
    config LifecycleCallbackConfig,
    supplierClient pocktclient.SupplierClient,
    // ... other required deps
) *LifecycleCallbackBuilder {
    return &LifecycleCallbackBuilder{
        logger:             logger,
        config:             config,
        supplierClient:     supplierClient,
        // ... other required deps
    }
}

// Optional dependency setters (fluent API)
func (b *LifecycleCallbackBuilder) WithProofChecker(checker *ProofRequirementChecker) *LifecycleCallbackBuilder {
    b.proofChecker = checker
    return b
}

func (b *LifecycleCallbackBuilder) WithServiceFactorProvider(provider ServiceFactorProvider) *LifecycleCallbackBuilder {
    b.serviceFactorProvider = provider
    return b
}

func (b *LifecycleCallbackBuilder) WithAppClient(client ApplicationQueryClient) *LifecycleCallbackBuilder {
    b.appClient = client
    return b
}

// ... other optional setters

// Build constructs the LifecycleCallback with all dependencies.
func (b *LifecycleCallbackBuilder) Build() (*LifecycleCallback, error) {
    // Validation
    if b.logger == nil {
        return nil, fmt.Errorf("logger is required")
    }
    if b.supplierClient == nil {
        return nil, fmt.Errorf("supplierClient is required")
    }
    // ... validate other required deps

    return &LifecycleCallback{
        logger:                b.logger,
        config:                b.config,
        supplierClient:        b.supplierClient,
        // ... all deps
        proofChecker:          b.proofChecker,  // May be nil
        serviceFactorProvider: b.serviceFactorProvider,  // May be nil
        appClient:             b.appClient,  // May be nil
        // ... other optional deps
        sessionLocks:          make(map[string]*sync.Mutex),
    }, nil
}
```

**Usage in `cmd/cmd_miner.go`:**
```go
// Before: Unwieldy constructor with many optional dependencies
callback := miner.NewLifecycleCallback(
    logger, config, supplierClient, sharedClient, blockClient,
    sessionClient, smstManager, sessionCoordinator,
)
callback.SetProofChecker(proofChecker)
callback.SetServiceFactorProvider(serviceFactorProvider)
callback.SetAppClient(appClient)
callback.SetStreamDeleter(streamDeleter)
callback.SetSubmissionTracker(submissionTracker)
callback.SetBuildPool(buildPool)
callback.SetDeduplicator(deduplicator)

// After: Fluent builder pattern
callback, err := miner.NewLifecycleCallbackBuilder(
    logger, config, supplierClient, sharedClient, blockClient,
    sessionClient, smstManager, sessionCoordinator,
).
    WithProofChecker(proofChecker).
    WithServiceFactorProvider(serviceFactorProvider).
    WithAppClient(appClient).
    WithStreamDeleter(streamDeleter).
    WithSubmissionTracker(submissionTracker).
    WithBuildPool(buildPool).
    WithDeduplicator(deduplicator).
    Build()
if err != nil {
    return err
}
```

**Benefits:**
- Clearer distinction between required and optional dependencies
- Compile-time safety for required dependencies
- Self-documenting construction code
- Easier to add new optional dependencies without breaking existing code

#### 5.2 SessionLifecycleManager Builder

**Similar pattern for `SessionLifecycleManager`:**

```go
// miner/session_lifecycle_builder.go

type SessionLifecycleManagerBuilder struct {
    // Required
    logger       logging.Logger
    sessionStore SessionStore
    sharedClient client.SharedQueryClient
    blockClient  client.BlockClient
    callback     SessionLifecycleCallback
    config       SessionLifecycleConfig
    workerPool   pond.Pool

    // Optional
    pendingChecker        PendingRelayChecker
    meterCleanupPublisher MeterCleanupPublisher
}

func NewSessionLifecycleManagerBuilder(
    logger logging.Logger,
    sessionStore SessionStore,
    sharedClient client.SharedQueryClient,
    blockClient client.BlockClient,
    callback SessionLifecycleCallback,
    config SessionLifecycleConfig,
    workerPool pond.Pool,
) *SessionLifecycleManagerBuilder {
    return &SessionLifecycleManagerBuilder{
        logger:       logger,
        sessionStore: sessionStore,
        sharedClient: sharedClient,
        blockClient:  blockClient,
        callback:     callback,
        config:       config,
        workerPool:   workerPool,
    }
}

func (b *SessionLifecycleManagerBuilder) WithPendingChecker(checker PendingRelayChecker) *SessionLifecycleManagerBuilder {
    b.pendingChecker = checker
    return b
}

func (b *SessionLifecycleManagerBuilder) WithMeterCleanupPublisher(publisher MeterCleanupPublisher) *SessionLifecycleManagerBuilder {
    b.meterCleanupPublisher = publisher
    return b
}

func (b *SessionLifecycleManagerBuilder) Build() (*SessionLifecycleManager, error) {
    // Validation and construction
}
```

#### 5.3 ProxyServer Builder

**Similar pattern for `ProxyServer`:**

```go
// relayer/proxy_builder.go

type ProxyServerBuilder struct {
    // Required
    logger        logging.Logger
    config        *Config
    healthChecker *HealthChecker
    publisher     transport.MinedRelayPublisher
    workerPool    pond.Pool

    // Optional (set via config or dependency injection)
    validator      RelayValidator
    relayProcessor RelayProcessor
    responseSigner *ResponseSigner
    supplierCache  *cache.SupplierCache
    relayMeter     *RelayMeter
}

func NewProxyServerBuilder(
    logger logging.Logger,
    config *Config,
    healthChecker *HealthChecker,
    publisher transport.MinedRelayPublisher,
    workerPool pond.Pool,
) *ProxyServerBuilder {
    return &ProxyServerBuilder{
        logger:        logger,
        config:        config,
        healthChecker: healthChecker,
        publisher:     publisher,
        workerPool:    workerPool,
    }
}

func (b *ProxyServerBuilder) WithValidator(validator RelayValidator) *ProxyServerBuilder {
    b.validator = validator
    return b
}

func (b *ProxyServerBuilder) WithRelayProcessor(processor RelayProcessor) *ProxyServerBuilder {
    b.relayProcessor = processor
    return b
}

func (b *ProxyServerBuilder) WithResponseSigner(signer *ResponseSigner) *ProxyServerBuilder {
    b.responseSigner = signer
    return b
}

func (b *ProxyServerBuilder) WithSupplierCache(cache *cache.SupplierCache) *ProxyServerBuilder {
    b.supplierCache = cache
    return b
}

func (b *ProxyServerBuilder) WithRelayMeter(meter *RelayMeter) *ProxyServerBuilder {
    b.relayMeter = meter
    return b
}

func (b *ProxyServerBuilder) Build() (*ProxyServer, error) {
    // Validation and construction
}
```

**Migration steps:**
1. Create builder for LifecycleCallback
2. Update cmd/cmd_miner.go to use builder
3. Deprecate old SetXxx methods (keep for backwards compat)
4. Test migration
5. Repeat for SessionLifecycleManager
6. Repeat for ProxyServer
7. Update all call sites in cmd/
8. Commit: `refactor: introduce builder pattern for complex constructors`

**Acceptance Criteria for Phase 5:**
- [ ] All tests still pass with `go test -race`
- [ ] Builders validate required dependencies at Build() time
- [ ] Fluent API is self-documenting
- [ ] Old constructors marked deprecated (not removed)
- [ ] Call sites updated to use builders

---

### Phase 6: Documentation and Cleanup (Week 6)

**Goal**: Document refactored architecture and clean up deprecated code

#### 6.1 Package Documentation

**Add package docs to new packages:**

**File: `miner/statehandler/doc.go`**
```go
// Package statehandler provides state-specific handlers for session lifecycle management.
//
// Each state (active, claiming, claimed, proving) has a dedicated handler that determines
// when a session should transition to the next state. Handlers are registered in a Registry
// and invoked by the SessionLifecycleManager.
//
// State Transition Flow:
//
//   active → claiming → claimed → proving → [proved | probabilistic_proved]
//      ↓          ↓          ↓
//   [claim_window_closed | claim_tx_error | proof_window_closed | proof_tx_error]
//
// Adding a New State Handler:
//
//   1. Implement the Handler interface
//   2. Register the handler in SessionLifecycleManager initialization
//   3. Add unit tests for the new handler
//
// Example:
//
//   handler := statehandler.NewActiveStateHandler(pendingChecker, params)
//   registry.Register(miner.SessionStateActive, handler)
//
package statehandler
```

**File: `relayer/transport/doc.go`**
```go
// Package transport provides transport-specific handlers for relay forwarding.
//
// Each RPC type (JSON-RPC, gRPC, WebSocket, REST) has a dedicated handler that
// manages the protocol-specific forwarding logic. Handlers are registered in a
// Registry and invoked by the ProxyServer.
//
// Supported RPC Types:
//
//   - JSON-RPC (jsonrpc): Standard HTTP POST with JSON-RPC payload
//   - gRPC (grpc): gRPC protocol with relay service
//   - WebSocket (websocket): Persistent WebSocket connections
//   - REST (rest): RESTful HTTP with streaming support (SSE, NDJSON)
//   - CometBFT (cometbft): Cosmos CometBFT RPC
//
// Adding a New Transport Handler:
//
//   1. Implement the TransportHandler interface
//   2. Register the handler in ProxyServer initialization
//   3. Add unit tests for the new handler
//   4. Update config.relayer.schema.yaml with new RPC type
//
// Example:
//
//   handler := transport.NewJSONRPCHandler(httpClient, bufferPool)
//   registry.Register("jsonrpc", handler)
//
package transport
```

#### 6.2 Architecture Decision Records (ADRs)

**Create ADRs for major refactoring decisions:**

**File: `.planning/research/ADR-001-state-handler-extraction.md`**
```markdown
# ADR-001: State Handler Extraction

## Status
Accepted

## Context
The SessionLifecycleManager contained 1207 lines of complex state machine logic
mixed with lifecycle management, making it difficult to test individual state
transitions and add new states.

## Decision
Extract state-specific logic into dedicated handlers implementing a Handler
interface, registered in a Registry pattern.

## Consequences

### Positive
- Each state handler is independently testable
- Clear separation of state transition logic from lifecycle management
- Easy to add new states without modifying existing handlers
- Reduced cyclomatic complexity in SessionLifecycleManager

### Negative
- More files to navigate (8 files vs 1)
- Additional abstraction layer (Handler interface + Registry)
- Slight performance overhead from interface dispatch (negligible)

## Alternatives Considered
1. Keep all logic in SessionLifecycleManager (rejected: too complex)
2. Use switch statement with extracted functions (rejected: still coupled)
3. State pattern with embedded structs (rejected: Go interfaces simpler)
```

**File: `.planning/research/ADR-002-transport-handler-extraction.md`**
```markdown
# ADR-002: Transport Handler Extraction

## Status
Accepted

## Context
The ProxyServer contained 1842 lines handling 5 different RPC types with
different forwarding semantics (streaming vs non-streaming, HTTP vs gRPC),
making it difficult to test protocol-specific logic.

## Decision
Extract transport-specific logic into dedicated handlers implementing a
TransportHandler interface, registered in a Registry pattern.

## Consequences

### Positive
- Each transport handler is independently testable
- Clear separation of protocol logic from server lifecycle
- Easy to add new RPC types without modifying existing handlers
- Streaming logic isolated from non-streaming logic

### Negative
- More files to navigate (7 files vs 1)
- Additional abstraction layer (TransportHandler interface + Registry)
- Must coordinate between multiple handlers for shared resources

## Alternatives Considered
1. Keep all logic in ProxyServer (rejected: too complex)
2. Separate files with no common interface (rejected: no polymorphism)
3. Use type switch on RPC type (rejected: violates open/closed principle)
```

**File: `.planning/research/ADR-003-builder-pattern-adoption.md`**
```markdown
# ADR-003: Builder Pattern Adoption

## Status
Accepted

## Context
Structs like LifecycleCallback and ProxyServer have 10+ dependencies, with
some required and some optional. The constructor + SetXxx() approach was
error-prone (forgot to set optional dependency) and verbose.

## Decision
Introduce builder pattern for complex constructors, using fluent API for
optional dependencies and validation in Build() method.

## Consequences

### Positive
- Clear distinction between required and optional dependencies
- Compile-time safety for required dependencies (constructor params)
- Self-documenting construction code (fluent API)
- Centralized validation at Build() time

### Negative
- More boilerplate (builder struct + methods)
- Slightly more verbose for simple cases
- Need to maintain both old and new APIs during migration

## Alternatives Considered
1. Keep constructor + SetXxx() (rejected: error-prone)
2. Functional options pattern (rejected: no compile-time safety for required deps)
3. Dependency injection framework (rejected: Go community prefers explicit)
```

#### 6.3 Update CLAUDE.md

**Add refactoring guidance to CLAUDE.md:**

```markdown
## Code Structure After Refactoring

### Miner Package

**Core Orchestrators:**
- `lifecycle_callback.go` (~800 lines): Coordinates claim/proof submission
- `session_lifecycle.go` (~600 lines): Monitors sessions and triggers transitions
- `supplier_manager.go` (1233 lines): Manages supplier workers

**State Handlers** (`miner/statehandler/`):
- `active.go`: Handles active → claiming transition
- `claiming.go`: Handles claiming → claimed/failed transition
- `claimed.go`: Handles claimed → proving transition
- `proving.go`: Handles proving → terminal transition

**Builders** (`miner/`):
- `lifecycle_callback_builder.go`: Builder for LifecycleCallback
- `session_lifecycle_builder.go`: Builder for SessionLifecycleManager

**Utilities:**
- `claim_validation.go`: Pure validation functions
- `submission_delay.go`: Test mode and timing utilities
- `session_window.go`: Window calculation utilities

### Relayer Package

**Core Server:**
- `proxy.go` (~800 lines): HTTP/gRPC server lifecycle and routing
- `relay_pipeline.go` (475 lines): Unified processing pipeline

**Transport Handlers** (`relayer/transport/`):
- `jsonrpc.go`: JSON-RPC forwarding
- `grpc.go`: gRPC forwarding
- `websocket.go`: WebSocket bridge
- `rest.go`: REST/streaming forwarding
- `streaming.go`: SSE/NDJSON handling

**Utilities:**
- `client_pool.go`: HTTP client pool management
- `http_utils.go`: HTTP utilities (compression, headers, etc.)

### Adding New Features

**To add a new session state:**
1. Define state constant in `session_store.go`
2. Create handler in `miner/statehandler/new_state.go`
3. Register handler in `SessionLifecycleManager.NewSessionLifecycleManager()`
4. Add unit tests in `miner/statehandler/new_state_test.go`

**To add a new RPC type:**
1. Define RPC type constant in config schema
2. Create handler in `relayer/transport/new_rpc.go`
3. Register handler in `ProxyServer.NewProxyServer()`
4. Add unit tests in `relayer/transport/new_rpc_test.go`
```

#### 6.4 Cleanup Deprecated Code

**After migration is complete and stable:**

1. Remove deprecated SetXxx methods from structs
2. Remove old constructor functions
3. Update all tests to use new APIs
4. Run full test suite
5. Commit: `chore: remove deprecated APIs after refactoring`

**Acceptance Criteria for Phase 6:**
- [ ] All packages have package-level documentation
- [ ] ADRs document major decisions
- [ ] CLAUDE.md updated with new structure
- [ ] Deprecated code removed (or marked for removal)
- [ ] All tests pass with `go test -race`

---

## Testing Strategy

### Unit Testing

**For each extracted component:**

1. **Interface-based mocks**: Use interfaces to provide test doubles
2. **Table-driven tests**: Use Go's table-driven test pattern for multiple scenarios
3. **Deterministic tests**: No `time.Sleep()`, use controlled time or mocks
4. **Race detection**: All tests must pass `go test -race`

**Example unit test pattern:**

```go
func TestActiveStateHandler_Handle(t *testing.T) {
    tests := []struct {
        name           string
        session        *SessionSnapshot
        currentHeight  int64
        pendingRelays  int64
        expectedState  SessionState
        expectError    bool
    }{
        {
            name: "before claim window - stays active",
            session: &SessionSnapshot{
                SessionID:        "session1",
                SessionEndHeight: 1000,
                State:            SessionStateActive,
            },
            currentHeight:  900,  // Before claim window
            pendingRelays:  0,
            expectedState:  SessionStateActive,
            expectError:    false,
        },
        {
            name: "in claim window, no pending - transitions to claiming",
            session: &SessionSnapshot{
                SessionID:        "session1",
                SessionEndHeight: 1000,
                State:            SessionStateActive,
            },
            currentHeight:  1005,  // In claim window
            pendingRelays:  0,
            expectedState:  SessionStateClaiming,
            expectError:    false,
        },
        {
            name: "in claim window, has pending - stays active",
            session: &SessionSnapshot{
                SessionID:        "session1",
                SessionEndHeight: 1000,
                State:            SessionStateActive,
            },
            currentHeight:  1005,  // In claim window
            pendingRelays:  5,     // Has pending relays
            expectedState:  SessionStateActive,
            expectError:    false,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Setup
            mockChecker := &mockPendingChecker{
                pendingCount: tt.pendingRelays,
            }
            params := &sharedtypes.Params{
                ClaimWindowOpenOffsetBlocks:  4,
                ClaimWindowCloseOffsetBlocks: 8,
                // ... other params
            }

            handler := statehandler.NewActiveStateHandler(mockChecker, params)

            // Execute
            nextState, err := handler.Handle(context.Background(), tt.session, tt.currentHeight)

            // Assert
            if tt.expectError {
                require.Error(t, err)
            } else {
                require.NoError(t, err)
                assert.Equal(t, tt.expectedState, nextState)
            }
        })
    }
}
```

### Integration Testing

**For complex interactions between components:**

1. **Use miniredis**: Already established pattern in codebase
2. **Test full flows**: Active → Claiming → Claimed → Proving → Proved
3. **Test error paths**: Window timeouts, transaction failures
4. **Test concurrency**: Multiple sessions transitioning simultaneously

**Example integration test:**

```go
func TestSessionLifecycle_FullFlow_Success(t *testing.T) {
    // Setup miniredis
    mr := miniredis.RunT(t)
    redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

    // Setup dependencies
    sessionStore := NewRedisSessionStore(redisClient, logger, config)
    mockCallback := &mockLifecycleCallback{}
    mockBlockClient := &mockBlockClient{currentHeight: 1000}

    // Create manager
    manager := NewSessionLifecycleManager(
        logger,
        sessionStore,
        sharedClient,
        mockBlockClient,
        mockCallback,
        config,
        nil,  // pendingChecker
        workerPool,
    )

    // Start manager
    ctx := context.Background()
    err := manager.Start(ctx)
    require.NoError(t, err)
    defer manager.Close()

    // Create active session
    session := &SessionSnapshot{
        SessionID:        "session1",
        SessionEndHeight: 1000,
        State:            SessionStateActive,
        // ... other fields
    }
    err = manager.TrackSession(ctx, session)
    require.NoError(t, err)

    // Advance to claim window
    mockBlockClient.SetHeight(1005)

    // Wait for transition
    time.Sleep(100 * time.Millisecond)  // Allow time for transition check

    // Verify callback was called
    assert.Equal(t, 1, mockCallback.claimCallCount)

    // Verify state updated
    updated := manager.GetSession("session1")
    assert.Equal(t, SessionStateClaiming, updated.State)
}
```

### Benchmark Testing

**For performance-critical paths:**

1. **Benchmark state transitions**: Ensure no regression in transition speed
2. **Benchmark transport handlers**: Verify protocol overhead is acceptable
3. **Benchmark builder construction**: Ensure builder pattern doesn't add significant overhead

**Example benchmark:**

```go
func BenchmarkActiveStateHandler_Handle(b *testing.B) {
    mockChecker := &mockPendingChecker{pendingCount: 0}
    params := &sharedtypes.Params{/* ... */}
    handler := statehandler.NewActiveStateHandler(mockChecker, params)

    session := &SessionSnapshot{
        SessionID:        "session1",
        SessionEndHeight: 1000,
        State:            SessionStateActive,
    }
    currentHeight := int64(1005)
    ctx := context.Background()

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, _ = handler.Handle(ctx, session, currentHeight)
    }
}
```

---

## Backwards Compatibility

### API Compatibility

**No breaking changes to public APIs:**

1. Old constructor functions remain available (deprecated)
2. Old SetXxx methods remain available (deprecated)
3. Deprecation warnings added via comments
4. Migration guide provided in ADRs

**Deprecation timeline:**

- **Phase 5 (Week 5)**: Introduce builders, mark old APIs as deprecated
- **Phase 6 (Week 6)**: Update call sites, keep deprecated APIs
- **v2.0.0 (Future)**: Remove deprecated APIs in major version bump

### Configuration Compatibility

**No changes to YAML configuration:**

- All config structs remain unchanged
- Config parsing logic unchanged
- Validation logic unchanged

### Redis Key Compatibility

**No changes to Redis key patterns:**

- All Redis keys remain unchanged
- No migration needed for existing deployments
- Full HA compatibility maintained

---

## Risk Mitigation

### Risk 1: Test Flakiness

**Mitigation:**
- Follow Rule #1: No flaky tests
- Use deterministic time (controlled mocks)
- Use miniredis for Redis (no network timing)
- Run tests 1000x before accepting (`go test -count=1000`)

### Risk 2: Behavior Changes

**Mitigation:**
- Characterization tests before refactoring
- Run full test suite after each extraction
- Manual testing with Tilt environment
- Compare metrics before/after (Prometheus)

### Risk 3: Performance Regression

**Mitigation:**
- Benchmark critical paths before refactoring
- Re-benchmark after each phase
- Monitor production metrics during rollout
- Rollback plan if performance degrades

### Risk 4: Breaking HA Compatibility

**Mitigation:**
- No changes to Redis keys or data structures
- No changes to pub/sub channels
- No changes to state machine logic (only extraction)
- Test with multiple instances in Tilt

---

## Success Metrics

### Code Quality Metrics

- **Lines per file**: Target <800 lines for main files
- **Cyclomatic complexity**: Target <15 per function
- **Test coverage**: Target >80% for new packages
- **Test execution time**: No increase >10%

### Development Velocity Metrics

- **Time to add new state**: <2 hours (was ~1 day)
- **Time to add new RPC type**: <4 hours (was ~2 days)
- **Time to fix state transition bug**: <1 hour (was ~4 hours)

### Production Metrics

- **No change in**:
  - Relay latency (p50, p95, p99)
  - Relay throughput (RPS)
  - Claim/proof submission success rate
  - Memory usage
  - CPU usage

---

## References

### Go Best Practices

- [Designing Go APIs the Standard Library Way: Accept Interfaces, Return Structs](https://dev.to/shrsv/designing-go-apis-the-standard-library-way-accept-interfaces-return-structs-410k)
- [How Go Interfaces Help Build Clean, Testable Systems](https://dev.to/shrsv/how-go-interfaces-help-build-clean-testable-systems-3163)
- [Dependency Injection in Go: Patterns & Best Practices](https://www.glukhov.org/post/2025/12/dependency-injection-in-go/)
- [Forget go dependency injection — master Functional Options Pattern](https://blog.stackademic.com/forget-go-dependency-injection-master-functional-options-pattern-c2fd32924078)

### Design Patterns

- [State Pattern in Go](https://refactoring.guru/design-patterns/state/go/example)
- [State Pattern Design](https://refactoring.guru/design-patterns/state)
- [Go State Machine Patterns](https://medium.com/@johnsiilver/go-state-machine-patterns-3b667f345b5e)

### Refactoring Tools

- [gopatch: Refactoring and code transformation tool for Go](https://github.com/uber-go/gopatch)
- [golang.org/x/tools/refactor package](https://pkg.go.dev/golang.org/x/tools/refactor)
- [Codebase Refactoring (with help from Go)](https://go.dev/talks/2016/refactor.article)

### Project-Specific

- [CLAUDE.md](../../CLAUDE.md) - Development guidelines
- [CONTRIBUTING.md](../../CONTRIBUTING.md) - Contribution requirements
- Session lifecycle state machine: `miner/session_store.go:16-86`
- Relay processing pipeline: `relayer/relay_pipeline.go:17-76`

---

## Appendix: File Size Tracking

### Before Refactoring

| File | Lines | Functions | Complexity |
|------|-------|-----------|------------|
| miner/lifecycle_callback.go | 1898 | 25 | High |
| miner/session_lifecycle.go | 1207 | 32 | High |
| relayer/proxy.go | 1842 | 27 | High |
| **Total** | **4947** | **84** | - |

### After Phase 2 (Utilities Extracted)

| File | Lines | Functions | Complexity |
|------|-------|-----------|------------|
| miner/lifecycle_callback.go | ~1650 | ~20 | High |
| miner/session_lifecycle.go | ~1050 | ~28 | High |
| relayer/proxy.go | ~1600 | ~22 | High |
| **Extracted utilities** | ~450 | ~15 | Low |
| **Total** | **4750** | **85** | - |

### After Phase 4 (State Handlers Extracted)

| File | Lines | Functions | Complexity |
|------|-------|-----------|------------|
| miner/lifecycle_callback.go | ~800 | ~12 | Medium |
| miner/session_lifecycle.go | ~600 | ~18 | Medium |
| relayer/proxy.go | ~800 | ~15 | Medium |
| **Extracted handlers** | ~1800 | ~25 | Low |
| **Extracted utilities** | ~450 | ~15 | Low |
| **Total** | **4450** | **85** | - |

### Target (All Phases Complete)

| File | Lines | Functions | Complexity |
|------|-------|-----------|------------|
| miner/lifecycle_callback.go | <800 | <12 | Medium |
| miner/session_lifecycle.go | <600 | <18 | Medium |
| relayer/proxy.go | <800 | <15 | Medium |
| **Extracted packages** | ~2500 | ~50 | Low |
| **Total** | **<4700** | **<95** | - |

**Key Improvements:**
- 50% reduction in main file sizes
- Clear separation of concerns
- Complexity moved from "High" to "Medium" in orchestrators
- Complexity is "Low" in extracted handlers (single responsibility)
- Total line count may increase slightly due to interfaces and builders, but maintainability greatly improves

---

## Conclusion

This refactoring roadmap provides a safe, incremental approach to decomposing large Go files while maintaining backwards compatibility, test coverage, and production stability.

**Key Takeaways:**

1. **Test-first**: Add characterization tests before any refactoring
2. **Incremental**: Small, reversible commits at each step
3. **Interface-driven**: Extract interfaces to enable testing
4. **State pattern**: Use dedicated handlers for state machine logic
5. **Builder pattern**: Simplify construction of complex structs
6. **No breaking changes**: Maintain backwards compatibility throughout

Each phase builds on the previous, with clear acceptance criteria and rollback points. The refactoring can be paused at any phase if priorities shift, as each phase leaves the codebase in a working state.

**Estimated Timeline:** 6 weeks (1 phase per week)
**Risk Level:** Low (incremental approach with tests at each step)
**ROI:** High (significantly improved maintainability and testability)
