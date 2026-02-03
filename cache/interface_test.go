//go:build test

package cache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCacheConfig_DefaultValues verifies DefaultCacheConfig returns expected defaults.
func TestCacheConfig_DefaultValues(t *testing.T) {
	cfg := DefaultCacheConfig()

	require.Equal(t, "ha:cache", cfg.CachePrefix)
	require.Equal(t, "ha:events", cfg.PubSubPrefix)
	require.Equal(t, int64(1), cfg.TTLBlocks)
	require.Equal(t, int64(30), cfg.BlockTimeSeconds)
	require.Equal(t, int64(2), cfg.ExtraGracePeriodBlocks)
	require.Equal(t, 5*time.Second, cfg.LockTimeout)
}

// TestCacheConfig_BlocksToTTL verifies BlocksToTTL converts blocks to correct duration.
func TestCacheConfig_BlocksToTTL(t *testing.T) {
	tests := []struct {
		name             string
		blockTimeSeconds int64
		blocks           int64
		expectedTTL      time.Duration
	}{
		{
			name:             "mainnet block time (30s)",
			blockTimeSeconds: 30,
			blocks:           10,
			expectedTTL:      300 * time.Second, // 10 blocks × 30s = 300s
		},
		{
			name:             "localnet block time (1s)",
			blockTimeSeconds: 1,
			blocks:           100,
			expectedTTL:      100 * time.Second, // 100 blocks × 1s = 100s
		},
		{
			name:             "single block",
			blockTimeSeconds: 30,
			blocks:           1,
			expectedTTL:      30 * time.Second, // 1 block × 30s = 30s
		},
		{
			name:             "zero blocks",
			blockTimeSeconds: 30,
			blocks:           0,
			expectedTTL:      0, // 0 blocks = 0 duration
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CacheConfig{
				BlockTimeSeconds: tt.blockTimeSeconds,
			}

			ttl := cfg.BlocksToTTL(tt.blocks)
			require.Equal(t, tt.expectedTTL, ttl)
		})
	}
}

// TestCacheKeys_SharedParams verifies SharedParams key format matches expected pattern.
func TestCacheKeys_SharedParams(t *testing.T) {
	keys := CacheKeys{Prefix: "ha:cache"}

	key := keys.SharedParams(100)
	require.Equal(t, "ha:cache:params:shared:100", key)

	key = keys.SharedParams(12345)
	require.Equal(t, "ha:cache:params:shared:12345", key)
}

// TestCacheKeys_SharedParamsLock verifies SharedParamsLock key format matches expected pattern.
func TestCacheKeys_SharedParamsLock(t *testing.T) {
	keys := CacheKeys{Prefix: "ha:cache"}

	key := keys.SharedParamsLock(100)
	require.Equal(t, "ha:cache:lock:params:shared:100", key)

	key = keys.SharedParamsLock(12345)
	require.Equal(t, "ha:cache:lock:params:shared:12345", key)
}

// TestCacheKeys_Session verifies Session key includes app, service, and height.
func TestCacheKeys_Session(t *testing.T) {
	keys := CacheKeys{Prefix: "ha:cache"}

	key := keys.Session("pokt1abc123", "anvil", 100)
	require.Equal(t, "ha:cache:session:pokt1abc123:anvil:100", key)

	key = keys.Session("pokt1xyz789", "ethereum", 12345)
	require.Equal(t, "ha:cache:session:pokt1xyz789:ethereum:12345", key)
}

// TestCacheKeys_SessionByID verifies SessionByID key format is correct.
func TestCacheKeys_SessionByID(t *testing.T) {
	keys := CacheKeys{Prefix: "ha:cache"}

	key := keys.SessionByID("session_abc_123")
	require.Equal(t, "ha:cache:session:id:session_abc_123", key)

	key = keys.SessionByID("session_xyz_789")
	require.Equal(t, "ha:cache:session:id:session_xyz_789", key)
}

// TestCacheKeys_SessionValidation verifies SessionValidation key format is correct.
func TestCacheKeys_SessionValidation(t *testing.T) {
	keys := CacheKeys{Prefix: "ha:cache"}

	key := keys.SessionValidation("pokt1abc123", "anvil", 100)
	require.Equal(t, "ha:cache:session:validation:pokt1abc123:anvil:100", key)

	key = keys.SessionValidation("pokt1xyz789", "ethereum", 12345)
	require.Equal(t, "ha:cache:session:validation:pokt1xyz789:ethereum:12345", key)
}

// TestCacheKeys_SupplierParams verifies SupplierParams key format is correct.
func TestCacheKeys_SupplierParams(t *testing.T) {
	keys := CacheKeys{Prefix: "ha:cache"}

	key := keys.SupplierParams()
	require.Equal(t, "ha:cache:params:supplier", key)
}

// TestCacheKeys_SupplierParamsLock verifies SupplierParamsLock key format is correct.
func TestCacheKeys_SupplierParamsLock(t *testing.T) {
	keys := CacheKeys{Prefix: "ha:cache"}

	key := keys.SupplierParamsLock()
	require.Equal(t, "ha:cache:lock:params:supplier", key)
}
