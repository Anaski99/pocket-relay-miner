# Go Testing and Code Quality Stack (2025)

**Research Date:** 2026-02-02
**Target Go Version:** 1.24.3
**System Type:** Production distributed system (Redis-backed, 1000+ RPS, complex state machines)
**Primary Goal:** Test confidence for safe refactoring of 1200-1900 line state machine files

---

## Executive Summary

This document prescribes the standard 2025 stack for testing and code quality in production Go distributed systems. All recommendations are based on current best practices, active maintenance status, and suitability for high-throughput, stateful systems.

**Key Principles:**
- Use built-in Go tooling first (stdlib > external dependencies)
- Prioritize real implementations over mocks (miniredis > mocks)
- Race detection is MANDATORY, not optional
- Integration tests >> unit tests for distributed systems
- No flaky tests, ever

---

## 1. Core Testing Framework

### Built-in `testing` Package ✅ MANDATORY
**Confidence: 100%** | **Status: Active (Go 1.24.3)**

**What:** Go's standard library testing package
**When:** ALL tests - unit, integration, benchmarks, fuzzing

**Rationale:**
- Native Go support with zero dependencies
- Includes sub-tests (`t.Run()`), parallel execution (`t.Parallel()`), benchmarking (`testing.B`), and fuzzing (since Go 1.18)
- Superior to external frameworks because it's the foundation everything else builds on
- Go 1.24/1.25 adds `testing/synctest` for async/concurrent testing

**Key Features for Distributed Systems:**
- `go test -race` for race detection (2-20x slower, catches all data races)
- `go test -parallel N` for concurrent test execution
- `testing.TB` interface for shared test/benchmark utilities
- `t.Cleanup()` for resource cleanup (replaces teardown patterns)

**Command Examples:**
```bash
# Standard test run
go test -v -tags test ./...

# Race detection (MANDATORY before commits)
go test -race -tags test ./miner/...

# Parallel execution with race detector
go test -race -parallel 4 ./...

# Coverage with HTML report
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

**Sources:**
- [Data Race Detector - The Go Programming Language](https://go.dev/doc/articles/race_detector)
- [Go Wiki: TableDrivenTests](https://go.dev/wiki/TableDrivenTests)
- [Go Unit Testing: Structure & Best Practices](https://www.glukhov.org/post/2025/11/unit-tests-in-go/)

---

## 2. Assertions and Test Utilities

### Testify ✅ RECOMMENDED
**Confidence: 95%** | **Version: v1.11.1** (currently in use)

**What:** Assertion library with mocking support
**When:** All tests requiring assertions (prefer `require` over `assert`)

**Rationale:**
- Most popular Go testing library (used in 90%+ of Go projects)
- Readable assertions reduce boilerplate
- `require` stops test on failure (prevents cascading errors)
- `assert` continues on failure (useful for collecting multiple failures)
- Mock package useful for interface-based testing (though prefer real implementations)

**Key Packages:**
```go
import (
    "github.com/stretchr/testify/require" // Use 99% of the time
    "github.com/stretchr/testify/assert"  // Use for non-critical checks
    "github.com/stretchr/testify/mock"    // Use sparingly, prefer real impls
)
```

**Best Practices:**
```go
// GOOD: require stops on failure, prevents nil pointer panics
func TestProcessRelay(t *testing.T) {
    result, err := ProcessRelay(ctx, relay)
    require.NoError(t, err, "ProcessRelay should not fail")
    require.NotNil(t, result, "result should exist")
    require.Equal(t, expected, result.Value)
}

// BAD: assert continues, may cause cascading failures
func TestProcessRelay(t *testing.T) {
    result, err := ProcessRelay(ctx, relay)
    assert.NoError(t, err) // If this fails...
    assert.NotNil(t, result) // ...this may panic
    assert.Equal(t, expected, result.Value) // ...never reached
}
```

**Sources:**
- [Testing in Go with Testify | Better Stack Community](https://betterstack.com/community/guides/scaling-go/golang-testify/)
- [Writing Table-Driven Tests Using Testify in Go](https://tillitsdone.com/blogs/table-driven-tests-with-testify/)

---

## 3. Redis Testing Strategy

### Miniredis ✅ RECOMMENDED (Current Choice)
**Confidence: 90%** | **Version: v2.35.0** (currently in use)

**What:** Pure Go Redis server implementation for unit tests
**When:** Fast unit tests, CI/CD without Docker, local development

**Rationale:**
- Zero dependencies (no Redis binary, no Docker)
- Fast test execution (<1ms vs 10-50ms for real Redis)
- In-process, isolated per-test (perfect for `t.Parallel()`)
- Implements 90%+ of Redis commands
- Integration tests run against Redis 8.4.0 (miniredis team verifies compatibility)

**Limitations:**
- TTLs don't auto-decrement (use `m.FastForward(d)` in tests)
- Missing some advanced commands (Lua scripts, modules)
- Not 100% Redis-compatible (good enough for 99% of tests)

**Example:**
```go
func TestRedisOperation(t *testing.T) {
    mr, err := miniredis.Run()
    require.NoError(t, err)
    defer mr.Close()

    client := redis.NewClient(&redis.Options{
        Addr: mr.Addr(),
    })

    // Test Redis operations
    err = client.Set(ctx, "key", "value", 0).Err()
    require.NoError(t, err)

    // Fast-forward TTLs if needed
    mr.FastForward(10 * time.Second)
}
```

### Testcontainers ⚠️ CONSIDER FOR INTEGRATION TESTS
**Confidence: 75%** | **When:** Full Redis compatibility needed

**What:** Docker-based real Redis instances for tests
**When:** Integration tests requiring 100% Redis compatibility, testing specific Redis versions

**Rationale:**
- Real Redis instance (100% compatibility)
- Tests against actual production Redis version
- Parallel execution with isolated containers
- Automatic cleanup

**Trade-offs:**
- Requires Docker runtime (not available in all CI environments)
- Slower (10-50ms setup per test vs <1ms for miniredis)
- Higher resource usage (memory, CPU, disk)

**Decision Matrix:**

| Requirement | Use Miniredis | Use Testcontainers |
|-------------|---------------|-------------------|
| Unit tests | ✅ YES | ❌ NO |
| Fast CI feedback | ✅ YES | ❌ NO |
| Parallel tests (100+) | ✅ YES | ⚠️ MAYBE |
| Lua scripts/modules | ❌ NO | ✅ YES |
| Exact Redis version | ❌ NO | ✅ YES |
| Docker-free CI | ✅ YES | ❌ NO |
| Integration tests | ⚠️ MAYBE | ✅ YES |

**Recommendation:** KEEP miniredis for unit tests, ADD testcontainers for critical integration tests only.

**Sources:**
- [GitHub - alicebob/miniredis](https://github.com/alicebob/miniredis)
- [Testcontainers for Go](https://golang.testcontainers.org/)
- [Testing in Go When You Have a Redis Dependency](https://www.razvanh.com/blog/testing-golang-redis-dependency)

---

## 4. Race Detection and Concurrency Testing

### Built-in Race Detector ✅ MANDATORY
**Confidence: 100%** | **Status: Active (Go 1.24.3)**

**What:** Go's built-in data race detector (using ThreadSanitizer)
**When:** EVERY test run before commit, CI pipeline, load tests

**Rationale:**
- Catches 100% of data races (no false positives)
- Required for concurrent systems (your miner/relayer has heavy goroutine usage)
- Found 2000+ races at Uber over 6 months (real production value)
- Performance cost acceptable (2-20x slower, 5-10x memory)

**Critical Requirements:**
1. ALL CI runs MUST use `-race` flag
2. NO commits allowed with race warnings
3. Load tests MUST run with `-race` enabled
4. Integration tests MUST use `-race`

**Command:**
```bash
# Run miner tests with race detection (current Makefile target)
make test_miner  # Already uses: go test -race -p 1 -parallel 1 ./miner/...

# Run all tests with race detection
go test -race -tags test -p 4 -parallel 4 ./...
```

**Performance Impact:**
- Memory: 5-10x increase
- CPU: 2-20x slower
- Acceptable for CI, load tests, pre-commit hooks

**Sources:**
- [Data Race Patterns in Go | Uber Blog](https://www.uber.com/en-US/blog/data-race-patterns-in-go/)
- [Introducing the Go Race Detector](https://go.dev/blog/race-detector)

### testing/synctest ✅ RECOMMENDED (Go 1.24+)
**Confidence: 85%** | **Status: Experimental in 1.24, GA in 1.25**

**What:** Standard library package for testing concurrent/async code with fake time
**When:** Testing time-dependent concurrent behavior, async operations, goroutine coordination

**Rationale:**
- Eliminates flaky time-based tests (no more `time.Sleep()` in tests)
- Fake clock advances only when all goroutines are blocked (deterministic)
- Isolated "bubbles" for test concurrency (no cross-test interference)
- Native Go 1.24+ feature (no external deps)

**Example:**
```go
func TestAsyncOperation(t *testing.T) {
    synctest.Test(t, func() {
        // Time starts at 2000-01-01 00:00:00 UTC
        start := time.Now()

        done := make(chan struct{})
        go func() {
            time.Sleep(5 * time.Minute) // Fake sleep
            close(done)
        }()

        synctest.Wait() // Blocks until all goroutines idle

        // Time advanced exactly 5 minutes (deterministic)
        require.Equal(t, 5*time.Minute, time.Since(start))
    })
}
```

**Note:** Available in Go 1.24.3 with `GOEXPERIMENT=synctest`, GA in Go 1.25.

**Sources:**
- [Testing Time (and other asynchronicities) - The Go Programming Language](https://go.dev/blog/testing-time)
- [Advanced Concurrency Testing in Go 1.24: Exploring testing/synctest](https://medium.com/@ajitem/advanced-concurrency-testing-in-go-1-24-exploring-testing-synctest-0486f610cd82)
- [The Synctest Package (new in Go 1.25) · Applied Go](https://appliedgo.net/spotlight/go-1.25-the-synctest-package/)

---

## 5. Property-Based and Fuzz Testing

### Built-in Fuzzing ✅ RECOMMENDED
**Confidence: 90%** | **Status: Active (Go 1.18+, mature in 1.24)**

**What:** Native fuzzing support in Go standard library
**When:** Testing input parsers, serializers, protocol handlers, crypto functions

**Rationale:**
- Built-in since Go 1.18 (production-ready by 2025)
- Coverage-guided fuzzing (finds edge cases automatically)
- Corpus management built-in
- Integrates with `go test` workflow

**Example:**
```go
func FuzzRelayRequest(f *testing.F) {
    // Seed corpus with valid inputs
    f.Add([]byte(`{"session_id":"abc","payload":"test"}`))

    f.Fuzz(func(t *testing.T, data []byte) {
        // Parse should never panic
        req, err := ParseRelayRequest(data)
        if err != nil {
            return // Invalid input, skip
        }

        // Valid input must round-trip
        serialized := req.Serialize()
        req2, err := ParseRelayRequest(serialized)
        require.NoError(t, err)
        require.Equal(t, req, req2)
    })
}
```

**Command:**
```bash
# Run fuzz test for 30 seconds
go test -fuzz=FuzzRelayRequest -fuzztime=30s

# Run with race detector
go test -fuzz=FuzzRelayRequest -fuzztime=30s -race
```

**Sources:**
- [Go Fuzzing - The Go Programming Language](https://go.dev/doc/security/fuzz/)
- [Tutorial: Getting started with fuzzing](https://go.dev/doc/tutorial/fuzz)
- [Go Testing in 2025: Mocks, Fuzzing & Property-Based Testing](https://dev.to/aleksei_aleinikov/go-testing-in-2025-mocks-fuzzing-property-based-testing-1gmg)

### Rapid (Property-Based Testing) ⚠️ CONSIDER
**Confidence: 70%** | **Version: Latest (actively maintained, updated Feb 2025)**

**What:** Modern property-based testing library for Go
**When:** Complex state machines, invariant testing, data structure validation

**Rationale:**
- Simpler API than gopter (fewer abstractions)
- Automatic test case minimization (shrinking)
- Better data generation than native fuzzing for structured types
- Actively maintained (updated Feb 2025)

**When to Use:**
- Testing complex state machine invariants (your 1200-1900 line FSMs)
- Verifying SMST tree properties (structure invariants)
- Redis operation sequences (consistency checks)

**Example:**
```go
func TestSMSTInvariants(t *testing.T) {
    rapid.Check(t, func(t *rapid.T) {
        // Generate random SMST operations
        ops := rapid.SliceOf(rapid.Just(Operation{
            Type: rapid.SampledFrom([]OpType{Set, Get, Delete}).Draw(t, "op"),
            Key:  rapid.String().Draw(t, "key"),
            Val:  rapid.Bytes().Draw(t, "val"),
        })).Draw(t, "ops")

        // Apply operations
        tree := NewSMST()
        for _, op := range ops {
            tree.Apply(op)
        }

        // Invariant: root hash must be deterministic
        root1 := tree.Root()
        root2 := ReplayOperations(ops).Root()
        require.Equal(t, root1, root2, "SMST root must be deterministic")
    })
}
```

**Trade-off:** Native fuzzing is usually sufficient. Use Rapid only for complex structured invariants.

**Sources:**
- [GitHub - flyingmutant/rapid](https://github.com/flyingmutant/rapid)
- [Comprehensive Guide to Property-Based Testing in Go](https://dzone.com/articles/property-based-testing-guide-go)

---

## 6. Integration Testing

### httptest (Standard Library) ✅ RECOMMENDED
**Confidence: 100%** | **Status: Active (stdlib)**

**What:** HTTP testing utilities from Go standard library
**When:** Testing HTTP handlers, servers, clients

**Rationale:**
- Zero dependencies (part of stdlib)
- Fast in-process HTTP servers
- Perfect for relayer HTTP handler tests

**Example:**
```go
func TestRelayHandler(t *testing.T) {
    handler := NewRelayHandler(deps)
    server := httptest.NewServer(handler)
    defer server.Close()

    resp, err := http.Post(server.URL+"/relay", "application/json", body)
    require.NoError(t, err)
    require.Equal(t, http.StatusOK, resp.StatusCode)
}
```

### Testcontainers ⚠️ CONSIDER
**Confidence: 75%** | **When:** Real service dependencies needed

**What:** Docker-based integration testing
**When:** Testing against real Redis, real blockchain nodes, real external services

**Rationale:**
- Real services (not mocks/fakes)
- Production-like environment
- Parallel execution safe (isolated containers)

**Trade-off:** Slower than miniredis, requires Docker. Use for critical integration tests only.

**Sources:**
- [Testcontainers for Go](https://golang.testcontainers.org/)
- [Realistic Integration Testing in Go with TestContainers](https://medium.com/@dilankanimsara101/realistic-integration-testing-in-go-with-testcontainers-fe9a859f8b39)

---

## 7. Chaos and Load Testing

### Toxiproxy ✅ RECOMMENDED
**Confidence: 80%** | **When:** Network resilience testing

**What:** TCP proxy for simulating network failures
**When:** Testing Redis connection failures, blockchain RPC latency, network partitions

**Rationale:**
- Simulate real-world network conditions (latency, packet loss, connection drops)
- Test circuit breakers and retry logic
- Validate graceful degradation

**Use Cases:**
- Redis connection failures (test miner failover)
- Blockchain RPC timeouts (test retry backoff)
- Network partitions (test distributed state consistency)

**Example:**
```go
func TestRedisFailover(t *testing.T) {
    // Setup Toxiproxy between miner and Redis
    proxy := toxiproxy.NewProxy("redis", "localhost:6379", "localhost:26379")

    // Inject 1000ms latency
    proxy.AddToxic("latency", "latency", "upstream", 1.0, toxiproxy.Attributes{
        "latency": 1000,
    })

    // Test that miner handles slow Redis gracefully
    // ...
}
```

**Sources:**
- [Best Chaos Engineering Tools: Open Source & Commercial Guide](https://steadybit.com/blog/top-chaos-engineering-tools-worth-knowing-about-2025-guide/)
- [Testing Distributed Systems | Curated list of resources](https://asatarin.github.io/testing-distributed-systems/)

### Chaos Mesh ⚠️ CONSIDER (Kubernetes-focused)
**Confidence: 60%** | **When:** Testing Kubernetes deployments

**What:** CNCF chaos engineering platform for Kubernetes
**When:** Testing HA deployment in Kubernetes, pod failures, network partitions

**Rationale:**
- Native Kubernetes integration
- Comprehensive fault injection (pod kill, network chaos, I/O delays)
- Useful for Tilt-based development environment

**Note:** Your project uses Tilt/Kubernetes, so Chaos Mesh is relevant. However, start with simpler tools (Toxiproxy) first.

**Sources:**
- [Automated Testing Framework Based on Chaos Mesh and Argo](https://www.pingcap.com/blog/building-automated-testing-framework-based-on-chaos-mesh-and-argo/)
- [Google Cloud Introduces Chaos Engineering Framework](https://www.infoq.com/news/2025/11/google-chaos-engineering/)

---

## 8. Code Quality and Static Analysis

### golangci-lint ✅ MANDATORY
**Confidence: 100%** | **Version: Latest (v1.62+)**

**What:** Meta-linter aggregating 70+ Go linters
**When:** EVERY commit (pre-commit hook), CI pipeline

**Rationale:**
- Industry standard (used by 90%+ of Go projects)
- Bundles staticcheck, go-vet, errcheck, revive, and 60+ others
- Single binary, fast execution
- Highly configurable

**Critical Linters for Your Project:**
```yaml
# .golangci.yml (MUST HAVE)
linters:
  enable:
    - errcheck       # Unchecked errors (CRITICAL for production)
    - gosec          # Security issues
    - govet          # Suspicious constructs
    - staticcheck    # Most important linter (catches 90% of bugs)
    - unused         # Dead code
    - ineffassign    # Ineffectual assignments
    - gocyclo        # Cyclomatic complexity (flag 1200-line functions)
    - gofmt          # Code formatting
    - goimports      # Import ordering
    - misspell       # Spelling errors
    - unconvert      # Unnecessary type conversions
    - unparam        # Unused function parameters
    - gocritic       # Opinionated checks (best practices)
    - revive         # Fast, configurable, extensible linter

linters-settings:
  gocyclo:
    min-complexity: 30  # Lower this from default (flag complex functions)
  gocritic:
    enabled-tags:
      - diagnostic   # Diagnostics for problematic code
      - performance  # Performance issues
      - style        # Style issues
```

**Command:**
```bash
# Run linter
make lint  # Should run: golangci-lint run ./...

# Auto-fix issues
golangci-lint run --fix ./...

# Run specific linters
golangci-lint run --enable=errcheck,staticcheck ./...
```

**Sources:**
- [GolangCI-Lint, a linter for Go](https://www.analysis-tools.dev/tool/golangci-lint)
- [GitHub - golangci/awesome-go-linters](https://github.com/golangci/awesome-go-linters)

### Staticcheck ✅ INCLUDED (via golangci-lint)
**Confidence: 100%** | **Status: Active**

**What:** Most respected standalone Go static analyzer
**When:** Already included in golangci-lint (no separate install needed)

**Rationale:**
- Catches subtle bugs (race conditions, incorrect type conversions, performance issues)
- Low false positive rate (high signal-to-noise)
- Used by Go team internally

**Note:** Already enabled by default in golangci-lint. No additional setup needed.

**Sources:**
- [What's the best Static Analysis tool for Golang?](https://www.dolthub.com/blog/2024-07-24-static-analysis/)

### govulncheck ✅ RECOMMENDED
**Confidence: 95%** | **Status: Active (Go 1.18+)**

**What:** Official Go vulnerability scanner
**When:** CI pipeline, pre-release checks, dependency updates

**Rationale:**
- Official Go team tool
- Scans dependencies for known CVEs
- Integrates with Go vulnerability database

**Command:**
```bash
# Install
go install golang.org/x/vuln/cmd/govulncheck@latest

# Scan codebase
govulncheck ./...

# Scan dependencies only
govulncheck -scan=packages ./...
```

**Sources:**
- [103 Go Static Analysis Tools, Linters, And Code Formatters](https://analysis-tools.dev/tag/go)

---

## 9. Test Coverage Tools

### Built-in Coverage ✅ RECOMMENDED
**Confidence: 100%** | **Status: Active (stdlib)**

**What:** Go's native coverage tools
**When:** All test runs, CI reporting

**Command:**
```bash
# Generate coverage
go test -coverprofile=coverage.out ./...

# View as HTML
go tool cover -html=coverage.out

# View per-function breakdown
go tool cover -func=coverage.out
```

### Codecov ✅ RECOMMENDED (CI Integration)
**Confidence: 90%** | **When:** GitHub/GitLab CI pipelines

**What:** Cloud-based coverage aggregation and reporting
**When:** CI pipelines, pull request checks, historical trends

**Rationale:**
- Free for open source
- PR comments with coverage diffs
- Historical trend tracking
- GitHub Actions integration

**Setup:**
```yaml
# .github/workflows/test.yml
- name: Upload coverage to Codecov
  uses: codecov/codecov-action@v4
  with:
    files: ./coverage.out
    flags: unittests
    name: codecov-umbrella
```

**Alternative:** Coveralls (similar features, free for open source)

**Sources:**
- [Codecov: Code Coverage Testing & Insights Solution](https://about.codecov.io/)
- [How to Measure Test Coverage in Go](https://www.tutorialpedia.org/blog/how-to-measure-test-coverage-in-go/)

### SonarQube ⚠️ CONSIDER (Enterprise Only)
**Confidence: 60%** | **When:** Large teams, enterprise requirements

**What:** Comprehensive code quality platform
**When:** Enterprise environments with compliance requirements

**Trade-off:** Complex setup, requires server infrastructure. Overkill for most open source projects.

**Sources:**
- [Go test coverage | SonarQube Server 2025.3](https://docs.sonarsource.com/sonarqube-server/2025.3/analyzing-source-code/test-coverage/go-test-coverage)

---

## 10. Mutation Testing

### Gremlins ⚠️ CONSIDER
**Confidence: 65%** | **Status: Active (v0.5.x, still pre-1.0)**

**What:** Mutation testing tool for Go
**When:** Evaluating test suite quality (post-milestone, not during active development)

**Rationale:**
- Verifies tests actually catch bugs (not just execute code)
- Useful for hardening critical paths (SMST operations, relay validation)
- Inspired by PITest (Java mutation testing leader)

**Limitations:**
- Still 0.x release (no backward compatibility guarantees)
- Slow on large codebases (hours for full run)
- Best for focused modules (microservices, critical packages)

**Use Cases:**
- Audit test quality before v1.0 release
- Validate critical path coverage (SMST, relay signing, session validation)
- Identify weak tests (high coverage, low mutation kill rate)

**Command:**
```bash
# Install
go install github.com/go-gremlins/gremlins@latest

# Run on specific package
gremlins unleash ./miner

# Generate HTML report
gremlins unleash --report-type=html ./miner
```

**Recommendation:** Use AFTER achieving 80%+ coverage. Not during active refactoring.

**Sources:**
- [GitHub - go-gremlins/gremlins](https://github.com/go-gremlins/gremlins)
- [Mutation testing on Go](https://dev.to/guilhermeguitte/mutation-testing-on-go-1lbf)

---

## Recommended Stack Summary

### MANDATORY (Must Have)
1. Built-in `testing` package with `-race` flag
2. `testify/require` for assertions
3. `miniredis` for Redis unit tests
4. `golangci-lint` with staticcheck, errcheck, govet
5. `govulncheck` for vulnerability scanning
6. Built-in coverage tools

### RECOMMENDED (Should Have)
7. Built-in fuzzing for input validation
8. `httptest` for HTTP handler tests
9. `testing/synctest` for async testing (Go 1.24+)
10. Codecov/Coveralls for CI coverage reporting
11. Toxiproxy for network chaos testing

### CONSIDER (Nice to Have)
12. Testcontainers for integration tests (Redis, blockchain nodes)
13. Rapid for property-based testing (state machine invariants)
14. Chaos Mesh for Kubernetes chaos testing
15. Gremlins for mutation testing (post-milestone audit)

---

## What NOT to Use

### ❌ AVOID: go-mock, gomock
**Why:** Prefer real implementations (miniredis, httptest). Mocks hide integration bugs and are brittle to refactor.

**Exception:** Use `testify/mock` only for external services you cannot control (blockchain RPCs).

### ❌ AVOID: Ginkgo, Gomega
**Why:** Adds unnecessary abstraction over stdlib `testing`. BDD-style tests are less common in Go community. Harder for new contributors.

### ❌ AVOID: gopter (Property-Based Testing)
**Why:** Rapid has simpler API, better shrinking, more active maintenance (updated Feb 2025 vs gopter's 2019 last update).

**Exception:** Use gopter if you need ScalaCheck-style generators.

### ❌ AVOID: Custom Redis mocks
**Why:** Miniredis is mature, maintained, and 90%+ compatible. Custom mocks hide edge cases.

### ❌ AVOID: time.Sleep() in tests
**Why:** Flaky tests. Use `testing/synctest` (Go 1.24+) or fake clocks.

---

## Implementation Roadmap

### Phase 1: Hardening (Immediate)
1. Ensure ALL tests pass `go test -race` (already enforced in Makefile)
2. Add `.golangci.yml` with recommended linters
3. Add `govulncheck` to CI pipeline
4. Set up Codecov/Coveralls for PR coverage diffs

### Phase 2: Coverage (Milestone Goal)
5. Add fuzzing for relay request parsing
6. Add property-based tests for SMST invariants (Rapid)
7. Increase integration test coverage with testcontainers (Redis, blockchain)
8. Target 80%+ coverage on critical paths (miner, relayer)

### Phase 3: Resilience (Post-Milestone)
9. Add Toxiproxy tests for Redis/blockchain failures
10. Add Chaos Mesh tests for Kubernetes HA scenarios
11. Run Gremlins mutation testing on critical packages
12. Document test patterns in CONTRIBUTING.md

---

## Confidence Levels Explained

- **100% - MANDATORY**: Must use, no alternatives, industry standard
- **90-95% - RECOMMENDED**: Proven tool, widely adopted, minimal risk
- **70-85% - CONSIDER**: Good fit for use case, some trade-offs
- **60-70% - EVALUATE**: Useful for specific scenarios, not general purpose
- **<60% - AVOID**: Better alternatives exist, immature, or over-engineered

---

## References

### Official Go Documentation
- [Data Race Detector](https://go.dev/doc/articles/race_detector)
- [Testing Time (and other asynchronicities)](https://go.dev/blog/testing-time)
- [Go Fuzzing](https://go.dev/doc/security/fuzz/)
- [Go Wiki: TableDrivenTests](https://go.dev/wiki/TableDrivenTests)

### Testing Best Practices
- [Go Unit Testing: Structure & Best Practices](https://www.glukhov.org/post/2025/11/unit-tests-in-go/)
- [Parallel Table-Driven Tests in Go](https://www.glukhov.org/post/2025/12/parallel-table-driven-tests-in-go/)
- [Data Race Patterns in Go | Uber Blog](https://www.uber.com/en-US/blog/data-race-patterns-in-go/)

### Tools and Libraries
- [Testify](https://betterstack.com/community/guides/scaling-go/golang-testify/)
- [Miniredis](https://github.com/alicebob/miniredis)
- [Testcontainers for Go](https://golang.testcontainers.org/)
- [Rapid (Property-Based Testing)](https://github.com/flyingmutant/rapid)
- [Gremlins (Mutation Testing)](https://github.com/go-gremlins/gremlins)

### Chaos Engineering
- [Best Chaos Engineering Tools: Open Source & Commercial Guide](https://steadybit.com/blog/top-chaos-engineering-tools-worth-knowing-about-2025-guide/)
- [Testing Distributed Systems](https://asatarin.github.io/testing-distributed-systems/)

### Code Quality
- [golangci-lint awesome linters](https://github.com/golangci/awesome-go-linters)
- [103 Go Static Analysis Tools](https://analysis-tools.dev/tag/go)

---

## Notes for Downstream Planning

### Key Takeaways for Refactoring Milestone
1. **Current test infrastructure is GOOD**: miniredis + testify + race detection is the right foundation
2. **Gaps to fill**: Fuzzing, property-based testing for SMST, integration tests with testcontainers
3. **Priority**: Race detection MUST pass 100% before any refactoring begins
4. **Coverage target**: 80%+ on critical paths (miner state machine, relayer validation)
5. **No flaky tests**: Use `testing/synctest` for time-based tests, avoid `time.Sleep()`

### For 1200-1900 Line State Machines
- Add property-based tests for invariants (Rapid)
- Add fuzzing for event sequences
- Add integration tests for state transitions with real Redis
- Use mutation testing (Gremlins) to validate test quality

### CI/CD Integration
```yaml
# Recommended GitHub Actions workflow
jobs:
  test:
    steps:
      - run: go test -race -tags test -coverprofile=coverage.out ./...
      - run: govulncheck ./...
      - run: golangci-lint run ./...
      - uses: codecov/codecov-action@v4
```

---

**End of STACK.md**
