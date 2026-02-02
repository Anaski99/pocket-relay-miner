# Test Quality Audit

**Date:** 2026-02-02
**Phase:** 01-test-foundation
**Purpose:** Document test quality violations for Phase 3 cleanup
**Status:** Snapshot - delete when all violations fixed

## Summary

| Category | Count | Priority |
|----------|-------|----------|
| time.Sleep violations | 66 | HIGH (causes flaky tests) |
| Global state | 0 | MEDIUM (cross-test contamination) |
| Non-deterministic data | 1 | LOW (crypto.rand is acceptable) |
| Missing race tests | 0 | HIGH (all tests run with -race) |

## time.Sleep Violations

Per CLAUDE.md Rule #1: "No flaky tests, no race conditions"
time.Sleep in tests is an anti-pattern that causes flaky behavior.

**Total:** 66 violations across 11 test files

### Breakdown by File

#### client/block_subscriber_integration_test.go (19 violations)
| Line | Sleep Duration | Context | Suggested Fix |
|------|----------------|---------|---------------|
| 458 | 100ms | Waiting for block event | Use channel/Eventually |
| 623 | 200ms | Waiting for reconnection | Use channel/Eventually |
| 636 | 100ms | Waiting for publish | Use channel/Eventually |
| 664 | 100ms | Waiting for event | Use channel/Eventually |
| 670 | 500ms | Waiting for processing | Use channel/Eventually |
| 702 | 500ms | Waiting for processing | Use channel/Eventually |
| 731 | 200ms | Waiting for event | Use channel/Eventually |
| 739 | 10ms | Loop waiting | Use channel/Eventually |
| 743 | 300ms | Waiting for publish | Use channel/Eventually |
| 773 | 200ms | Waiting for event | Use channel/Eventually |
| 779 | 300ms | Waiting for publish | Use channel/Eventually |
| 809 | 200ms | Waiting for event | Use channel/Eventually |
| 823 | 5ms | Loop waiting | Use channel/Eventually |
| 926 | 200ms | Waiting for event | Use channel/Eventually |
| 932 | 500ms | Waiting for processing | Use channel/Eventually |
| 955 | 100ms | Loop waiting | Use channel/Eventually |
| 960 | 50ms | Loop waiting | Use channel/Eventually |

#### observability/runtime_metrics_test.go (9 violations)
| Line | Sleep Duration | Context | Suggested Fix |
|------|----------------|---------|---------------|
| 55 | 150ms | Waiting for metrics collection | Use channel/Eventually |
| 151 | 100ms | Waiting for metrics | Use channel/Eventually |
| 157 | 100ms | Waiting for metrics | Use channel/Eventually |
| 189 | 100ms | Waiting for metrics | Use channel/Eventually |
| 306 | 50ms | Waiting for metrics | Use channel/Eventually |
| 343 | 200ms | Waiting for metrics | Use channel/Eventually |
| 348 | 10ms | Loop waiting | Use channel/Eventually |
| 393 | 100ms | Waiting for shutdown | Use channel/Eventually |

#### observability/server_test.go (11 violations)
| Line | Sleep Duration | Context | Suggested Fix |
|------|----------------|---------|---------------|
| 59 | 100ms | Waiting for server startup | Use readiness probe |
| 129 | 200ms | Waiting for server | Use readiness probe |
| 154 | 200ms | Waiting for server | Use readiness probe |
| 176 | 200ms | Waiting for server | Use readiness probe |
| 199 | 200ms | Waiting for server | Use readiness probe |
| 238 | 100ms | Waiting for server | Use readiness probe |
| 244 | 200ms | Waiting for shutdown | Use channel/Eventually |
| 278 | 100ms | Waiting for server | Use readiness probe |
| 300 | 100ms | Waiting for server | Use readiness probe |
| 318 | 100ms | Loop waiting | Use channel/Eventually |

#### observability/helpers_test.go (8 violations)
| Line | Sleep Duration | Context | Suggested Fix |
|------|----------------|---------|---------------|
| 24 | 10ms | Waiting for async operation | Use channel/Eventually |
| 44 | 5ms | Waiting for async operation | Use channel/Eventually |
| 58 | 5ms | Waiting for async operation | Use channel/Eventually |
| 69 | 5ms | Waiting for async operation | Use channel/Eventually |
| 78 | 5ms | Waiting for async operation | Use channel/Eventually |
| 87 | 5ms | Waiting for async operation | Use channel/Eventually |
| 153 | 5ms | Waiting for async operation | Use channel/Eventually |
| 163 | 5ms | Waiting for async operation | Use channel/Eventually |

#### miner/claim_pipeline_test.go (7 violations)
| Line | Sleep Duration | Context | Suggested Fix |
|------|----------------|---------|---------------|
| 261 | 200ms | Waiting for claim submission | Use channel/Eventually |
| 309 | 200ms | Waiting for claim submission | Use channel/Eventually |
| 346 | 200ms | Waiting for claim submission | Use channel/Eventually |
| 393 | 500ms | Waiting for processing | Use channel/Eventually |
| 511 | 500ms | Waiting for processing | Use channel/Eventually |
| 653 | 100ms | Loop waiting | Use channel/Eventually |

#### query/ test files (5 violations)
| File | Line | Sleep Duration | Context | Suggested Fix |
|------|------|----------------|---------|---------------|
| application_query_test.go | 342 | 100ms | Waiting for cache | Use channel/Eventually |
| supplier_query_test.go | 256 | 100ms | Waiting for cache | Use channel/Eventually |
| session_query_test.go | 151 | 100ms | Waiting for cache | Use channel/Eventually |
| service_query_test.go | 358 | 100ms | Waiting for cache | Use channel/Eventually |
| proof_query_test.go | 231 | 100ms | Waiting for cache | Use channel/Eventually |

#### miner/redis_smst_manager_test.go (3 violations)
| Line | Sleep Duration | Context | Suggested Fix |
|------|----------------|---------|---------------|
| 548 | 5ms | Loop waiting for SMST update | Use channel/Eventually |
| 740 | 10ms | Waiting for updates to start | Use channel/Eventually |
| 752 | variable | Staggered goroutine start | Use proper synchronization |

#### miner/proof_pipeline_test.go (2 violations)
| Line | Sleep Duration | Context | Suggested Fix |
|------|----------------|---------|---------------|
| 138 | 200ms | Waiting for proof submission | Use channel/Eventually |
| 422 | 100ms | Loop waiting | Use channel/Eventually |

#### cmd/relay/metrics_test.go (2 violations)
| Line | Sleep Duration | Context | Suggested Fix |
|------|----------------|---------|---------------|
| 91 | 10ms | Waiting for metrics | Use channel/Eventually |
| 249 | 100ms | Waiting for metrics | Use channel/Eventually |

#### observability/instruction_timer_test.go (2 violations)
| Line | Sleep Duration | Context | Suggested Fix |
|------|----------------|---------|---------------|
| 321 | 5ms | Timing test | Use channel/Eventually |
| 323 | 5ms | Timing test | Use channel/Eventually |

#### tx/tx_client_test.go (1 violation)
| Line | Sleep Duration | Context | Suggested Fix |
|------|----------------|---------|---------------|
| 1241 | 10ms | Ensure timeout expires | Use channel/Eventually |

#### query/query_test.go (1 violation)
| Line | Sleep Duration | Context | Suggested Fix |
|------|----------------|---------|---------------|
| 213 | variable | Slow duration simulation | Use channel/Eventually |

### Impact Analysis

**HIGH RISK FILES:**
- `client/block_subscriber_integration_test.go` (19 violations) - Integration test with many async operations
- `observability/server_test.go` (11 violations) - Server lifecycle tests
- `observability/runtime_metrics_test.go` (9 violations) - Metrics collection tests
- `observability/helpers_test.go` (8 violations) - Async helper tests

**PATTERN:** Most time.Sleep violations are in integration tests waiting for async operations (events, metrics, server startup). These are prime candidates for flaky behavior under load.

**RECOMMENDED FIX STRATEGY:**
1. Replace `time.Sleep` with channel-based synchronization
2. Use `testify/require.Eventually` for polling assertions
3. Add readiness probes for server startup tests
4. Use context cancellation for timeout scenarios

## Global State Dependencies

**Count:** 0

No package-level mutable variables found in test files. All tests appear to use local state.

**Verification command:** `grep -rn "^var " --include="*_test.go" . | grep -v vendor/`

## Non-Deterministic Data Generation

**Count:** 1 (acceptable usage)

| File | Line | Pattern | Assessment |
|------|------|---------|------------|
| miner/redis_smst_utils_test.go | 138 | `rand.Read(b)` | ACCEPTABLE - uses crypto/rand for secure random bytes |

**Note:** This is `crypto/rand.Read`, not `math/rand`, which is cryptographically secure and appropriate for generating random test data. No fix needed.

## Coverage Gaps

| Package | Coverage | Status | Notes |
|---------|----------|--------|-------|
| miner/ | 14.9% | Tests exist | Comprehensive unit tests exist but low coverage indicates gaps |
| relayer/ | 0.0% | No unit tests | Covered by integration test scripts in scripts/ folder |
| cache/ | 0.0% | No unit tests | No unit test coverage at all |

**Critical Gap:** `relayer/` and `cache/` packages have 0% unit test coverage. Per CLAUDE.md, these are critical paths handling 1000+ RPS and should have 80%+ coverage.

**Analysis:**
- **relayer/**: Integration coverage via `scripts/test-simple-relay.sh` and other test scripts
- **cache/**: No test coverage at all - HIGH PRIORITY for Phase 2
- **miner/**: Good test structure but only 14.9% coverage - needs expansion

## Flaky Tests Identified

**Status:** Tests will be run with 100-iteration stability check (Task 2)

| Test | Package | Failure Rate | Status |
|------|---------|--------------|--------|
| (pending stability test) | | | |

## 100-Run Results

**Status:** Pending execution in Task 2

---

*Audit completed: 2026-02-02*
*Delete this file when all violations are fixed in Phase 3*
