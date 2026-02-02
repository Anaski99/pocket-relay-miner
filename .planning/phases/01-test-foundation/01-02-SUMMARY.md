---
phase: 01-test-foundation
plan: 02
subsystem: testing
tags: [go, race-detection, test-stability, makefile, bash]

# Dependency graph
requires:
  - phase: 01-test-foundation
    provides: Test infrastructure foundation
provides:
  - Race detection enabled for all test targets (mandatory)
  - Flaky test detection script (100-run stability validation)
  - test-no-race target for quick local iteration
affects: [01-03-coverage-baseline, 01-04-audit-flakes, 01-05-ci-pipeline]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Race detection mandatory via make test (2-20x overhead acceptable)"
    - "Stability validation via 100-run script with -shuffle"
    - "Fail-fast on first flaky test detection"

key-files:
  created:
    - scripts/test-stability.sh
  modified:
    - Makefile

key-decisions:
  - "Race detection now mandatory for all packages (not just miner)"
  - "test-no-race target provides escape hatch for quick local dev"
  - "Stability script uses -shuffle to maximize test variance"

patterns-established:
  - "All test targets use -race flag by default"
  - "Cache package requires sequential execution (-p 1 -parallel 1)"
  - "Stability script fails fast with verbose output on first failure"

# Metrics
duration: 1min
completed: 2026-02-02
---

# Phase [01] Plan [02]: Enable Race Detection and Stability Testing Summary

**Race detection mandatory for all packages via Makefile, with 100-run flaky test detection script**

## Performance

- **Duration:** 1 min 11 sec
- **Started:** 2026-02-02T20:26:12Z
- **Completed:** 2026-02-02T20:27:23Z
- **Tasks:** 2
- **Files modified:** 2

## Accomplishments
- Race detection enabled for all test targets (was only miner package before)
- Created scripts/test-stability.sh for 100-run stability validation
- Added test-no-race target for quick local iteration (skips 2-20x race detection overhead)

## Task Commits

Each task was committed atomically:

1. **Task 1: Update Makefile for race detection** - `0b7e566` (chore)
2. **Task 2: Create stability test script** - `9624968` (feat)

## Files Created/Modified
- `Makefile` - Updated test target to include -race flag, added test-no-race target
- `scripts/test-stability.sh` - 100-run flaky test detection with fail-fast behavior

## Decisions Made

**Race detection overhead acceptable (2-20x):**
- Per 01-CONTEXT.md: "Race detection runs on every PR"
- User decision: Correctness > speed
- test-no-race provides escape hatch for quick local dev

**Stability script uses -shuffle:**
- Maximizes test variance to expose order dependencies
- Combined with -race to catch both timing and data races
- Fail-fast on first failure with verbose output

**Cache package special handling preserved:**
- 143 tests with shared miniredis require sequential execution
- Uses -p 1 -parallel 1 for both test and test-no-race targets

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

**Ready for coverage baseline (01-03):**
- Race detection now universal
- Stability script ready for flaky test audit (01-04)
- CI pipeline can use `make test` for race-enabled runs (01-05)

**Blockers:** None

**Concerns:** None

---
*Phase: 01-test-foundation*
*Completed: 2026-02-02*
