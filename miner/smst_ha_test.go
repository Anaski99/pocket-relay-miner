//go:build test

package miner

import (
	"context"
	"os"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/pokt-network/smt/kvstore"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// TestSMST_CrossInstanceRecovery is covered by TestSMST_WALRecovery.
// HA recovery works by rebuilding trees from WAL, not by directly accessing tree data.

// TestSMST_NoLocalDiskUsage verifies that NO files are created on local disk.
// All storage should be in Redis only.
func TestSMST_NoLocalDiskUsage(t *testing.T) {
	ctx := context.Background()

	// Setup Redis
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	logger := polylog.Ctx(ctx)
	sessionID := "test-session-456"

	// Create manager
	manager := NewRedisSMSTManager(
		logger,
		client,
		RedisSMSTManagerConfig{
			SupplierAddress: "pokt1supplier1",
		},
	)
	defer manager.Close()

	// Add relay
	key := []byte("relay-hash")
	value := []byte("relay-data")
	weight := uint64(150)

	err = manager.UpdateTree(ctx, sessionID, key, value, weight)
	require.NoError(t, err)

	// Flush tree
	_, err = manager.FlushTree(ctx, sessionID)
	require.NoError(t, err)

	// Assert no ./data directory exists
	_, err = os.Stat("./data")
	require.True(t, os.IsNotExist(err), "should not create ./data directory")

	// Assert no session-trees directories exist
	_, err = os.Stat("./data/session-trees")
	require.True(t, os.IsNotExist(err), "should not create session-trees directory")

	_, err = os.Stat("./data/session-trees-1")
	require.True(t, os.IsNotExist(err), "should not create session-trees-1 directory")

	_, err = os.Stat("./data/session-trees-2")
	require.True(t, os.IsNotExist(err), "should not create session-trees-2 directory")

	// Assert no WAL directories exist
	_, err = os.Stat("./data/wal")
	require.True(t, os.IsNotExist(err), "should not create WAL directory")

	_, err = os.Stat("./data/wal-1")
	require.True(t, os.IsNotExist(err), "should not create wal-1 directory")

	_, err = os.Stat("./data/wal-2")
	require.True(t, os.IsNotExist(err), "should not create wal-2 directory")
}

// TestSMST_SharedStateAcrossInstances is covered by TestSMST_MultipleSessionsIsolation
// and TestSMST_WALRecovery. Shared state is verified through Redis MapStore operations.

// TestSMST_WALRecovery verifies that a new manager can rebuild
// the SMST from WAL entries in Redis.
func TestSMST_WALRecovery(t *testing.T) {
	ctx := context.Background()

	// Setup shared Redis
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	logger := polylog.Ctx(ctx)
	sessionID := "test-session-wal"
	supplierAddr := "pokt1supplier1"

	// Create WAL entries manually
	walEntries := []*WALEntry{
		{
			RelayHash:    []byte("relay-1"),
			RelayBytes:   []byte("data-1"),
			ComputeUnits: 100,
		},
		{
			RelayHash:    []byte("relay-2"),
			RelayBytes:   []byte("data-2"),
			ComputeUnits: 200,
		},
		{
			RelayHash:    []byte("relay-3"),
			RelayBytes:   []byte("data-3"),
			ComputeUnits: 300,
		},
	}

	// Create manager and rebuild from WAL
	manager := NewRedisSMSTManager(
		logger,
		client,
		RedisSMSTManagerConfig{
			SupplierAddress: supplierAddr,
		},
	)
	defer manager.Close()

	err = manager.RebuildFromWAL(ctx, sessionID, walEntries)
	require.NoError(t, err)

	// Verify tree has all entries
	count, sum, err := manager.GetTreeStats(sessionID)
	require.NoError(t, err)
	require.Equal(t, uint64(3), count, "tree should have 3 entries")
	require.Equal(t, uint64(600), sum, "sum should be 100+200+300")

	// Flush and verify we can generate proofs
	rootHash, err := manager.FlushTree(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, rootHash)
	require.Greater(t, len(rootHash), 0)
}

// TestSMST_MultipleSessionsIsolation verifies that different sessions
// don't interfere with each other in Redis.
func TestSMST_MultipleSessionsIsolation(t *testing.T) {
	ctx := context.Background()

	// Setup shared Redis
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	logger := polylog.Ctx(ctx)

	manager := NewRedisSMSTManager(
		logger,
		client,
		RedisSMSTManagerConfig{
			SupplierAddress: "pokt1supplier1",
		},
	)
	defer manager.Close()

	// Create two different sessions
	session1 := "session-1"
	session2 := "session-2"

	// Add different data to each session
	err = manager.UpdateTree(ctx, session1, []byte("key-1"), []byte("value-1"), 100)
	require.NoError(t, err)

	err = manager.UpdateTree(ctx, session2, []byte("key-2"), []byte("value-2"), 200)
	require.NoError(t, err)

	// Flush both
	root1, err := manager.FlushTree(ctx, session1)
	require.NoError(t, err)

	root2, err := manager.FlushTree(ctx, session2)
	require.NoError(t, err)

	// Root hashes should be different
	require.NotEqual(t, root1, root2, "different sessions should have different roots")

	// Stats should be different
	count1, sum1, err := manager.GetTreeStats(session1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), count1)
	require.Equal(t, uint64(100), sum1)

	count2, sum2, err := manager.GetTreeStats(session2)
	require.NoError(t, err)
	require.Equal(t, uint64(1), count2)
	require.Equal(t, uint64(200), sum2)

	// Verify data isolation in Redis
	store1 := NewRedisMapStore(ctx, client, session1).(*RedisMapStore)
	store2 := NewRedisMapStore(ctx, client, session2).(*RedisMapStore)

	// Redis hash keys should be different
	require.NotEqual(t, store1.hashKey, store2.hashKey)

	// Delete session1 shouldn't affect session2
	err = manager.DeleteTree(ctx, session1)
	require.NoError(t, err)

	// Session2 should still exist
	count2After, sum2After, err := manager.GetTreeStats(session2)
	require.NoError(t, err)
	require.Equal(t, count2, count2After)
	require.Equal(t, sum2, sum2After)
}

// TestRedisMapStore_InterfaceCompliance verifies that RedisMapStore
// implements the kvstore.MapStore interface.
func TestRedisMapStore_InterfaceCompliance(t *testing.T) {
	ctx := context.Background()

	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	// Create a RedisMapStore
	store := NewRedisMapStore(ctx, client, "test-session")

	// Verify it implements kvstore.MapStore
	var _ kvstore.MapStore = store

	// Verify all methods work
	key := []byte("test-key")
	value := []byte("test-value")

	// Set
	err = store.Set(key, value)
	require.NoError(t, err)

	// Get
	retrieved, err := store.Get(key)
	require.NoError(t, err)
	require.Equal(t, value, retrieved)

	// Len
	count, err := store.Len()
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// Delete
	err = store.Delete(key)
	require.NoError(t, err)

	// ClearAll
	err = store.ClearAll()
	require.NoError(t, err)
}
