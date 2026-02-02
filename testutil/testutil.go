//go:build test

// Package testutil provides shared test utilities for characterization tests
// across miner/, relayer/, and cache/ packages.
//
// This package enforces Rule #1 compliance from CLAUDE.md:
// - No flaky tests (deterministic data generation)
// - No race conditions (pass -race flag)
// - No mock/fake tests (use real miniredis)
// - No timeout weird tests (no time.Sleep dependencies)
package testutil

import (
	"encoding/binary"
	"math/rand"
	"os"
)

const (
	// DefaultConcurrency is the default number of concurrent goroutines for CI tests.
	DefaultConcurrency = 100

	// NightlyConcurrency is the number of concurrent goroutines for nightly tests.
	NightlyConcurrency = 1000

	// DefaultIterations is the default number of iterations for CI tests.
	DefaultIterations = 10

	// NightlyIterations is the number of iterations for nightly tests.
	NightlyIterations = 100
)

// IsNightlyMode returns true if tests are running in nightly mode.
// Set TEST_MODE=nightly to enable extended test parameters.
func IsNightlyMode() bool {
	return os.Getenv("TEST_MODE") == "nightly"
}

// GetTestConcurrency returns the appropriate concurrency level for tests.
// Returns 1000 for nightly mode, 100 for regular CI.
func GetTestConcurrency() int {
	if IsNightlyMode() {
		return NightlyConcurrency
	}
	return DefaultConcurrency
}

// GetTestIterations returns the appropriate iteration count for tests.
// Returns 100 for nightly mode, 10 for regular CI.
func GetTestIterations() int {
	if IsNightlyMode() {
		return NightlyIterations
	}
	return DefaultIterations
}

// GenerateDeterministicBytes generates deterministic bytes using a seeded PRNG.
// The same seed and length will always produce identical output.
// This is essential for Rule #1: no flaky tests.
//
// IMPORTANT: Do NOT use crypto/rand for test data generation.
// Crypto randomness breaks determinism and makes tests flaky.
func GenerateDeterministicBytes(seed int, length int) []byte {
	//nolint:gosec // Intentionally using weak PRNG for deterministic test data
	rng := rand.New(rand.NewSource(int64(seed)))
	bytes := make([]byte, length)
	//nolint:gosec // Intentionally using weak PRNG for deterministic test data
	rng.Read(bytes)
	return bytes
}

// GenerateDeterministicString generates a deterministic string of hex characters.
// Useful for generating session IDs, addresses, and other string identifiers.
func GenerateDeterministicString(seed int, length int) string {
	bytes := GenerateDeterministicBytes(seed, (length+1)/2)
	hexChars := "0123456789abcdef"
	result := make([]byte, length)
	for i := 0; i < length; i++ {
		result[i] = hexChars[bytes[i/2]>>(4*(1-i%2))&0xf]
	}
	return string(result)
}

// GenerateDeterministicSessionID generates a deterministic session ID.
// Format: "session-{seed}-{8 hex chars}"
func GenerateDeterministicSessionID(seed int) string {
	suffix := GenerateDeterministicString(seed, 8)
	return "session-" + suffix
}

// GenerateDeterministicInt64 generates a deterministic int64 from a seed.
func GenerateDeterministicInt64(seed int) int64 {
	bytes := GenerateDeterministicBytes(seed, 8)
	return int64(binary.LittleEndian.Uint64(bytes))
}

// GenerateDeterministicUint64 generates a deterministic uint64 from a seed.
func GenerateDeterministicUint64(seed int) uint64 {
	bytes := GenerateDeterministicBytes(seed, 8)
	return binary.LittleEndian.Uint64(bytes)
}
