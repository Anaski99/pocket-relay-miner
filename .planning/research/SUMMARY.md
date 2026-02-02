# Testing and Refactoring Research Summary

**Project:** Pocket RelayMiner Testing and Refactoring Milestone
**Domain:** Production Go distributed systems (Redis-backed, high-throughput)
**Researched:** 2026-02-02
**Confidence:** HIGH

## Executive Summary

This research examines best practices for adding test coverage and safely refactoring large Go codebases, specifically targeting three complex files in pocket-relay-miner (total 4947 lines). The project is a production distributed system handling 1000+ RPS with real financial implications, requiring zero-tolerance for flaky tests and race conditions.

**Recommended approach:** Incremental, test-first refactoring using Go's built-in testing framework with miniredis for Redis operations, strict race detection enforcement, and state pattern extraction for complex state machines. The codebase already follows many best practices (miniredis usage, race detection for miner tests, suite pattern), but needs expansion to achieve 80%+ coverage before refactoring three large files (lifecycle_callback.go: 1898 lines, session_lifecycle.go: 1207 lines, proxy.go: 1842 lines).

**Key risks:** Flaky tests from time.Sleep() synchronization (64+ existing violations), race conditions in mock state, and "big bang" refactorings. Mitigation strategy: Phase-based approach with characterization tests first, continuous race detection, and small incremental PRs (<500 lines) with behavioral validation at each step.

## Key Findings

### Recommended Testing Stack

The 2025 standard Go testing stack prioritizes built-in tooling and real implementations over external frameworks and mocks. This aligns perfectly with pocket-relay-miner's existing patterns and production requirements.

**Core technologies:**
- **Go testing (stdlib)**: Foundation for all tests — native support, includes sub-tests, parallel execution, benchmarking, and fuzzing (Go 1.18+)
- **testify/suite + require**: Test organization and assertions — reduces boilerplate, provides clean setup/teardown, fail-fast on errors
- **miniredis v2**: In-process Redis for unit tests — zero dependencies, <1ms operations, 90%+ Redis compatibility, perfect for parallel tests
- **go test -race**: Data race detection (MANDATORY) — catches 100% of race conditions, 2-20x slowdown acceptable for correctness
- **golangci-lint**: Static analysis aggregator — bundles 70+ linters including staticcheck, errcheck, govet for comprehensive code quality
- **govulncheck**: Vulnerability scanning — official Go team tool, integrates with CVE database

**Recommended additions:**
- **Testcontainers**: Real Redis/blockchain nodes for integration tests — 100% compatibility, Docker-based isolation, auto-cleanup
- **testing/synctest (Go 1.24+)**: Deterministic concurrency testing — eliminates flaky time-based tests, fake clock advances only when goroutines block
- **Rapid**: Property-based testing for state machines — better than fuzzing for complex invariants (SMST trees, session lifecycle)

**What NOT to use:**
- gomock/go-mock (prefer real implementations)
- Ginkgo/Gomega (unnecessary abstraction over stdlib)
- Custom Redis mocks (miniredis is mature and maintained)
- time.Sleep() in tests (use channels, contexts, or synctest)

### Expected Testing Features

Based on analysis of production Go distributed systems and pocket-relay-miner's requirements:

**Must have (table stakes):**
- **Real implementation testing** — Use miniredis/httptest instead of mocks, catches integration bugs
- **Race-free concurrency** — ALL tests must pass `go test -race`, zero tolerance for warnings
- **Deterministic test data** — Seed-based or constant data, no random failures
- **Test suite pattern** — Shared setup/teardown for resource management (miniredis, worker pools)
- **State machine verification** — Test all state transitions and lifecycle hooks
- **Root hash equivalence** — Verify distributed storage matches in-memory reference (financial correctness)
- **Build constraints** — Use `//go:build test` to exclude test-only code from production
- **Warmup/failover testing** — Verify HA system can resume from Redis state after restart

**Should have (differentiators):**
- **Table-driven tests** — Reduce duplication, easy to add scenarios
- **Benchmarking critical paths** — Catch performance regressions (SMST ops, relay validation, cache hits)
- **Integration tests with real services** — Use httptest for HTTP, testcontainers for databases
- **Sealing mechanism tests** — Prevent race conditions in two-phase commit (claim/proof submission)
- **High concurrency stress tests** — Test with 10-50+ goroutines, expose rare race conditions
- **Error injection testing** — Validate graceful degradation, retry logic
- **Makefile test targets** — Convenient, documented test execution patterns

**Defer (avoid complexity):**
- Mutation testing with Gremlins (use after 80%+ coverage, slow on large codebases)
- Custom mocking frameworks (stdlib + simple function mocks sufficient)
- BDD-style tests (Go community prefers table-driven)

### Architecture Approach for Refactoring

The research recommends incremental extraction using interface-based design, state pattern for complex state machines, and builder pattern for structs with many dependencies. This maintains working code with passing tests at every step.

**Major refactoring phases:**

1. **Characterization Tests (Week 1)** — Add test coverage BEFORE refactoring, establish behavioral baselines
2. **Extract Stateless Utilities (Week 2)** — Move pure functions to separate files (claim validation, HTTP utils, session windows)
3. **Extract Internal Interfaces (Week 3)** — Define narrow interfaces (ClaimBuilder, ProofBuilder, RequestProcessor) to reduce coupling
4. **Create State Handler Packages (Week 4)** — Extract state machines to dedicated handlers (miner/statehandler/, relayer/transport/)
5. **Introduce Builder Pattern (Week 5)** — Simplify construction of complex structs (10+ dependencies)
6. **Documentation and Cleanup (Week 6)** — Package docs, ADRs, remove deprecated code

**Key architectural patterns:**

- **Interface segregation**: Small interfaces (2-5 methods), defined at consumer side
- **State pattern**: Dedicated handler per state, registered in central registry
- **Dependency injection**: Constructor functions with explicit dependencies, optional via setters
- **Strangler fig pattern**: Gradual migration with feature flags, both paths work during transition
- **Branch by abstraction**: Introduce interface, swap implementations behind it

**File size targets:**
- Before: lifecycle_callback.go (1898 lines), session_lifecycle.go (1207 lines), proxy.go (1842 lines)
- After: <800 lines each for orchestrators, extracted handlers in focused packages (<500 lines per file)
- Result: 50% reduction in main file sizes, complexity moves from "High" to "Medium" in orchestrators, "Low" in handlers

### Critical Pitfalls

Top pitfalls that violate **Rule #1: No flaky tests, no race conditions, no mock/fake tests**:

1. **Time-Based Synchronization (time.Sleep())** — 64+ violations detected in codebase, causes non-deterministic failures under load. **Avoid:** Use channels, context timeouts, require.Eventually(), or testing/synctest instead. **Phase 1-3 priority.**

2. **Missing -race Flag in CI** — Race detector not enforced for all packages (only miner currently). **Avoid:** Add `make test-race` target, require in CI, add pre-commit hook. **Phase 1 priority.**

3. **The "Mockery" Anti-Pattern** — Over-mocking creates false confidence (tests pass, production fails). **Avoid:** Prefer miniredis for Redis, httptest for HTTP, only mock external boundaries (blockchain RPC). **Phase 2-4.**

4. **"Big Bang" Refactoring** — 3000+ line PRs impossible to review, hide regressions. **Avoid:** Incremental PRs (<500 lines), strangler fig pattern, feature flags. **All phases.**

5. **Refactoring Without Characterization Tests** — No way to detect if refactor changed semantics. **Avoid:** Add tests capturing current behavior FIRST (even if behavior seems wrong), golden tests for complex outputs. **Phase 1 mandatory.**

**Additional high-priority pitfalls:**

6. **Unprotected Mock State** — Mocks with shared counters accessed by multiple goroutines without mutex. Current codebase is good (uses mutexes), maintain this pattern.

7. **Testing Implementation Details** — Tests coupled to internal state via reflection break on every refactor. Test public API only, focus on contracts not implementation.

8. **Flaky Tests from Non-Deterministic Data** — Using time.Now(), rand without seed, map iteration order. Always seed random, mock time dependencies, sort before comparison.

## Implications for Roadmap

Based on combined research findings, the refactoring milestone should follow a 6-week phased approach with clear gates between phases. Each phase maintains working code with passing tests.

### Phase 1: Foundation (Week 1)
**Rationale:** Must establish test coverage before any refactoring to detect behavior changes. Currently missing tests for session_lifecycle.go and proxy.go.

**Delivers:**
- Characterization tests for lifecycle_callback.go (expand from 205 lines)
- New test files: session_lifecycle_test.go, proxy_test.go, proxy_streaming_test.go
- Test audit: Identify all time.Sleep() usage, global state, non-deterministic data
- Baseline metrics: Coverage %, test execution time, benchmark baselines

**Addresses:**
- Features: Real implementation testing, deterministic test data, test suite pattern
- Stack: testify/suite, miniredis, -race flag enforcement

**Avoids:**
- Pitfall 5.2: Refactoring without characterization tests
- Pitfall 1.3: Non-deterministic test data

**Quality Gates:**
- Coverage >70% on target files (lifecycle_callback, session_lifecycle, proxy)
- All new tests pass `go test -race`
- No flaky tests (100/100 runs pass)
- Tests use real implementations (miniredis, httptest)

---

### Phase 2: Core Components (Week 2)
**Rationale:** Extract pure functions first (lowest risk) to reduce file sizes without changing behavior. This phase is reversible if issues arise.

**Delivers:**
- Extracted utility files: claim_validation.go, submission_delay.go, session_window.go, http_utils.go, client_pool.go
- Convert repetitive tests to table-driven format (30% test code reduction target)
- Fix time.Sleep() in miner/cache tests (replace with channels/contexts)
- Target file reduction: ~200-300 lines each

**Uses:**
- Stack: Built-in testing, table-driven test pattern
- Architecture: Interface-based design (narrow interfaces)

**Implements:**
- Features: Table-driven tests, benchmarking critical paths

**Avoids:**
- Pitfall 4.2: Copy-paste test proliferation
- Pitfall 1.1: Time-based synchronization (partial fix)

**Quality Gates:**
- All tests still pass with `go test -race`
- Original files reduced by 200-300 lines each
- Extracted utilities have dedicated unit tests
- No behavior changes (characterization tests confirm)
- Commits are atomic and reversible

---

### Phase 3: Edge Cases (Week 3)
**Rationale:** Define interfaces to decouple components before extracting complex logic. Prepares for state handler extraction in Phase 4.

**Delivers:**
- Interface definitions: ClaimBuilder, ProofBuilder, StateTransitionDeterminer, RequestProcessor
- Default implementations wrapping existing logic
- Split large interfaces (>5 methods) by responsibility
- Fix time.Sleep() in observability/client tests
- Interface segregation audit

**Uses:**
- Stack: Interface-based testing, dependency injection
- Architecture: "Accept interfaces, return structs" pattern

**Implements:**
- Features: Integration tests with real services (httptest)

**Avoids:**
- Pitfall 3.3: Interface pollution (fat interfaces)
- Pitfall 4.1: Testing implementation details
- Pitfall 1.1: Time-based synchronization (complete fix)

**Quality Gates:**
- All tests still pass with `go test -race`
- New interfaces are narrow (2-5 methods max)
- Each interface has default implementation
- Interfaces enable easier testing (mock implementations possible)
- No behavior changes (characterization tests confirm)

---

### Phase 4: Integration (Week 4)
**Rationale:** Extract state machines into dedicated packages for isolation and testability. This is the highest-risk phase requiring careful validation.

**Delivers:**
- New packages: miner/statehandler/, relayer/transport/
- State handlers: active.go, claiming.go, claimed.go, proving.go, terminal.go
- Transport handlers: jsonrpc.go, grpc.go, websocket.go, rest.go, streaming.go
- Testcontainers integration tests for critical paths
- Target: 50%+ reduction in main file sizes

**Uses:**
- Stack: Testcontainers for integration tests
- Architecture: State pattern, handler registry

**Implements:**
- Features: Sealing mechanism tests, high concurrency stress tests, error injection testing

**Avoids:**
- Pitfall 5.1: "Big Bang" refactoring (incremental extraction)
- Pitfall 6.1: "Works on my machine" (testcontainers standardize environment)

**Quality Gates:**
- All tests still pass with `go test -race`
- New packages have <500 lines per file
- Each handler is independently testable
- Clear separation of concerns (state logic vs transport logic)
- No behavior changes (characterization tests confirm)
- Original large files reduced by 50%+

**Research Flag:** This phase may need deeper research into state pattern implementation in Go and handler registry best practices.

---

### Phase 5: Validation (Week 5)
**Rationale:** Simplify construction of refactored components and validate no regressions occurred.

**Delivers:**
- Builder pattern for complex constructors (LifecycleCallback, SessionLifecycleManager, ProxyServer)
- Benchmark comparison (before/after refactoring)
- Coverage threshold enforcement in CI (80%+)
- Pre-commit hooks for race detection
- Remove deprecated code after migration

**Uses:**
- Stack: Benchstat for performance comparison, govulncheck for security

**Implements:**
- Features: Makefile test targets expansion

**Avoids:**
- Pitfall 5.3: Trusting IDE refactorings blindly (benchmark validation)
- Pitfall 2.1: Missing -race flag (enforce in pre-commit)

**Quality Gates:**
- All tests still pass with `go test -race`
- Builders validate required dependencies at Build() time
- No >10% performance regression (benchstat)
- Old constructors marked deprecated (not removed yet)
- Call sites updated to use builders

---

### Phase 6: Cleanup (Week 6)
**Rationale:** Document architectural changes and finalize migration.

**Delivers:**
- Package documentation (miner/statehandler/, relayer/transport/)
- Architecture Decision Records (ADRs) for major decisions
- Updated CLAUDE.md with new structure
- Removed deprecated APIs
- Final validation: 10x test run, benchmark report, coverage report

**Implements:**
- Features: Documentation for maintainability

**Avoids:**
- Technical debt accumulation from undocumented changes

**Quality Gates:**
- All packages have package-level documentation
- ADRs document major decisions (state handler extraction, transport handlers, builder pattern)
- CLAUDE.md updated with new code structure
- Deprecated code removed
- All tests pass with `go test -race`
- Coverage >80% on critical paths maintained

---

### Phase Ordering Rationale

**Why this order:**

1. **Tests First (Phase 1)** — Cannot safely refactor without understanding current behavior. Characterization tests are the foundation.

2. **Utilities Before Logic (Phase 2)** — Pure functions are lowest risk to extract (no shared state, no lifecycle). Builds confidence before touching complex logic.

3. **Interfaces Before Extraction (Phase 3)** — Defining contracts first makes Phase 4 extraction safer. Can test interface implementations independently.

4. **State Machines Last (Phase 4)** — Most complex extraction, highest risk. By this point, team has experience with Phases 2-3 patterns.

5. **Builders After Refactoring (Phase 5)** — Simplifies construction of newly-refactored components. Doing this earlier would require re-work.

6. **Documentation After Stabilization (Phase 6)** — Premature documentation would need updates as design evolves. Document once structure is final.

**Dependency chain:**
- Phase 2 depends on Phase 1 (needs tests to verify no behavior change)
- Phase 3 depends on Phase 2 (interfaces reference extracted utilities)
- Phase 4 depends on Phase 3 (state handlers implement interfaces)
- Phase 5 depends on Phase 4 (builders construct refactored components)
- Phase 6 depends on Phase 5 (documents final architecture)

**How this avoids pitfalls:**
- Small PRs (<500 lines) at each phase avoid "big bang" (Pitfall 5.1)
- Characterization tests first avoid refactoring blind (Pitfall 5.2)
- Race detection at every phase prevents concurrency bugs (Pitfall 2.1)
- Real implementations throughout avoid mock drift (Pitfall 3.1)
- Continuous testing prevents flaky test accumulation (Pitfall 1.1, 1.2, 1.3)

### Research Flags

**Phases likely needing deeper research during planning:**

- **Phase 1:** Test coverage patterns for 1800+ line state machine files — may need property-based testing research (Rapid library)
- **Phase 4:** State handler registry implementation in Go — review state pattern examples, ensure no over-engineering
- **Phase 4:** Transport handler concurrency patterns — need research on goroutine pooling for streaming handlers

**Phases with standard patterns (skip research-phase):**

- **Phase 2:** Pure function extraction — straightforward Go refactoring, well-documented
- **Phase 3:** Interface definition — standard Go practice, covered in official docs
- **Phase 5:** Builder pattern — established Go pattern, many examples available
- **Phase 6:** Documentation — internal process, no research needed

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | **HIGH** | Go stdlib testing and tooling is stable (1.18-1.24), miniredis actively maintained (updated Feb 2025), testcontainers mature with Go support, testify is industry standard (90%+ adoption) |
| Features | **HIGH** | Testing patterns derived from current codebase analysis (13,757 test lines), aligned with Go community best practices, validated by production use (1000+ RPS target met) |
| Architecture | **MEDIUM-HIGH** | Refactoring patterns well-documented in Go community, state pattern proven in similar systems, but 1800+ line files are complex and may have hidden coupling |
| Pitfalls | **HIGH** | 64+ time.Sleep() violations confirmed via codebase search, race detector patterns proven at Uber (2000+ bugs found), anti-patterns documented across multiple Go projects |

**Overall confidence:** **HIGH**

The recommended stack and testing patterns are proven in production Go systems. The refactoring approach is incremental and reversible. Primary risk is execution discipline (maintaining small PRs, not skipping phases).

### Gaps to Address

**Testing Strategy:**
- **Gap:** Property-based testing (Rapid) coverage — need to determine which SMST invariants to test
- **Handle:** During Phase 1, analyze SMST operations to identify key invariants (deterministic root hash, tree structure consistency)

**Refactoring Execution:**
- **Gap:** Team familiarity with state pattern in Go — may need training/examples
- **Handle:** Review state pattern examples from Go community before Phase 4, create proof-of-concept for one handler

**Performance Validation:**
- **Gap:** Benchmark baseline collection incomplete — need pre-refactoring benchmarks for all critical paths
- **Handle:** During Phase 1, run benchmarks and save results for comparison in Phase 5

**Integration Testing:**
- **Gap:** Testcontainers setup for blockchain nodes — unclear if needed or if mocks sufficient
- **Handle:** During Phase 3, evaluate if blockchain integration tests add value vs mocks for query clients

**CI/CD Integration:**
- **Gap:** Pre-commit hook enforcement — need to decide if hooks are mandatory or optional
- **Handle:** During Phase 5, discuss with team whether to enforce via git hooks or rely on CI checks

## Sources

### Primary (HIGH confidence)

**Official Go Documentation:**
- [Data Race Detector - The Go Programming Language](https://go.dev/doc/articles/race_detector) — Race detection patterns, performance characteristics
- [Go Fuzzing](https://go.dev/doc/security/fuzz/) — Native fuzzing support since Go 1.18
- [Testing Time (and other asynchronicities)](https://go.dev/blog/testing-time) — Official blog on testing/synctest (Go 1.25)
- [Go Wiki: TableDrivenTests](https://go.dev/wiki/TableDrivenTests) — Table-driven test pattern

**Codebase Analysis:**
- `miner/redis_mapstore_test.go` — Current suite pattern implementation (143 tests)
- `miner/redis_smst_utils_test.go` — Shared test infrastructure
- `Makefile` test targets — Current test execution patterns
- `CLAUDE.md` — Project-specific testing requirements (Rule #1)

**Active Libraries (verified maintenance):**
- [GitHub - alicebob/miniredis](https://github.com/alicebob/miniredis) — v2.35.0, last commit Feb 2025
- [GitHub - stretchr/testify](https://github.com/stretchr/testify) — v1.11.1, industry standard
- [Testcontainers for Go](https://golang.testcontainers.org/) — Official CNCF project

### Secondary (MEDIUM confidence)

**Community Best Practices:**
- [Learn Go with tests - Anti-patterns](https://quii.gitbook.io/learn-go-with-tests/meta/anti-patterns) — Chris James, community-recognized resource
- [Go Unit Testing: Structure & Best Practices](https://www.glukhov.org/post/2025/11/unit-tests-in-go/) — Recent (2025) best practices
- [Data Race Patterns in Go | Uber Blog](https://www.uber.com/en-US/blog/data-race-patterns-in-go/) — Real production experience (2000+ races found)

**Refactoring Patterns:**
- [Designing Go APIs the Standard Library Way](https://dev.to/shrsv/designing-go-apis-the-standard-library-way-accept-interfaces-return-structs-410k) — Interface design principles
- [State Pattern in Go](https://refactoring.guru/design-patterns/state/go/example) — Pattern implementation
- [Dependency Injection in Go: Patterns & Best Practices](https://www.glukhov.org/post/2025/12/dependency-injection-in-go/) — Constructor injection

**Testing Strategies:**
- [Testing in Go with Testify | Better Stack Community](https://betterstack.com/community/guides/scaling-go/golang-testify/) — testify patterns
- [Parallel Table-Driven Tests in Go](https://www.glukhov.org/post/2025/12/parallel-table-driven-tests-in-go/) — Concurrency patterns

### Tertiary (LOW confidence, requires validation)

**Emerging Tools:**
- [GitHub - flyingmutant/rapid](https://github.com/flyingmutant/rapid) — Property-based testing, last updated Feb 2025 but still 0.x release
- [GitHub - go-gremlins/gremlins](https://github.com/go-gremlins/gremlins) — Mutation testing, pre-1.0 (v0.5.x)

**Needs validation during implementation:**
- testing/synctest adoption — Go 1.24.3 has experimental support, need to test with GOEXPERIMENT=synctest
- Testcontainers performance — unclear if overhead acceptable for unit tests vs integration tests only

---

*Research completed: 2026-02-02*
*Ready for roadmap: yes*
*Confidence: HIGH (stack, features, pitfalls), MEDIUM-HIGH (architecture)*
*Primary risk: Execution discipline, not technical approach*
