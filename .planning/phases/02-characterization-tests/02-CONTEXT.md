# Phase 2: Characterization Tests - Context

**Gathered:** 2026-02-02
**Status:** Ready for planning

<domain>
## Phase Boundary

Add comprehensive test coverage for large state machines (lifecycle_callback.go, session_lifecycle.go, proxy.go) to enable safe refactoring. Tests document expected behavior and verify the system under concurrent load. This phase brings miner/, relayer/, and cache/ packages to 80%+ coverage.

</domain>

<decisions>
## Implementation Decisions

### Test Granularity
- Use both approaches: individual transition tests (TestActiveToClaimable, etc.) PLUS happy-path scenario tests (TestReachSettled, TestReachFailed)
- Tests encode expected behavior, not just current behavior — if test fails, code is wrong
- Focus on observable behavior for transport tests (response format, errors), not wire protocol details (WebSocket frames, gRPC metadata)

### Concurrency Scenarios
- High concurrency tests (500+ concurrent operations) to match 1000 RPS production target
- Cover both session state races (multiple relays hitting same session, state transitions during processing) AND cache invalidation races (L1/L2 updates while reads in progress)
- Worker pool exhaustion tested implicitly via high-load tests — no explicit exhaustion tests needed
- Include Redis connection failure simulation during concurrent operations

### Coverage Strategy
- Uniform 80% coverage across all packages (miner/, relayer/, cache/) individually
- cache/ is lower priority — focus on complex state machines first (lifecycle_callback.go, session_lifecycle.go, proxy.go)
- Warning-only CI gate — report coverage, warn on drops, but don't block merges in Phase 2
- "Money paths" are critical (claim/proof submission, relay validation, signature verification) — aim for 90%+ on these
- Error handling paths included in 80% target — error handling bugs lose funds
- Tests assume fresh state — no backward-compatibility or migration testing
- Test proxy.go and relay_processor.go in parallel for relayer/ coverage
- Coverage reports: HTML for local development, percentage summary in CI logs

### Test Data Patterns
- Strict determinism: same seed produces same data, every test run is reproducible
- Hardcoded test keys committed to repo — same cryptographic material every test
- Shared testutil/ package for helpers reused across miner/, relayer/, cache/
- Both quick tests (CI) and thorough tests (nightly) via test modes — minimal data for fast CI, production-like scale for nightly

### Claude's Discretion
- Error test organization (separate functions vs table-driven subtests) — choose based on readability
- Test data generation approach (builder pattern, fixtures, or table-driven) — choose based on protocol layer requirements and existing codebase patterns
- Exact concurrency levels within "high" range (500-1000)
- Redis failure injection implementation details

</decisions>

<specifics>
## Specific Ideas

- Protocol layer has caused test issues before — test data generation must account for validation requirements (ring signatures, session headers, etc.)
- Rule #1 from CLAUDE.md is absolute: no flaky tests, no race conditions, no mock/fake tests
- All tests must use miniredis for Redis (not mocks), pass race detector, run deterministically

</specifics>

<deferred>
## Deferred Ideas

None — discussion stayed within phase scope

</deferred>

---

*Phase: 02-characterization-tests*
*Context gathered: 2026-02-02*
