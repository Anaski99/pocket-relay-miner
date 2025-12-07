//go:build test

package miner

import (
	"context"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/redis/go-redis/v9"
)

func setupBenchRedis(b *testing.B) (*redis.Client, func()) {
	b.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		b.Fatalf("failed to start miniredis: %v", err)
	}

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	cleanup := func() {
		client.Close()
		mr.Close()
	}

	return client, cleanup
}

// BenchmarkRedisSMST_Update measures the performance of adding relays to a Redis-backed SMST.
func BenchmarkRedisSMST_Update(b *testing.B) {
	ctx := context.Background()
	client, cleanup := setupBenchRedis(b)
	defer cleanup()

	logger := polylog.Ctx(ctx)
	manager := NewRedisSMSTManager(
		logger,
		client,
		RedisSMSTManagerConfig{
			SupplierAddress: "pokt1supplier1",
		},
	)
	defer manager.Close()

	sessionID := "bench-session"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("relay-%d", i))
		value := []byte(fmt.Sprintf("data-%d", i))
		weight := uint64(100 + i)

		err := manager.UpdateTree(ctx, sessionID, key, value, weight)
		if err != nil {
			b.Fatalf("failed to update tree: %v", err)
		}
	}
}

// BenchmarkRedisSMST_Update_Parallel measures parallel update performance.
func BenchmarkRedisSMST_Update_Parallel(b *testing.B) {
	ctx := context.Background()
	client, cleanup := setupBenchRedis(b)
	defer cleanup()

	logger := polylog.Ctx(ctx)
	manager := NewRedisSMSTManager(
		logger,
		client,
		RedisSMSTManagerConfig{
			SupplierAddress: "pokt1supplier1",
		},
	)
	defer manager.Close()

	sessionID := "bench-session-parallel"

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := []byte(fmt.Sprintf("relay-%d", i))
			value := []byte(fmt.Sprintf("data-%d", i))
			weight := uint64(100 + i)

			err := manager.UpdateTree(ctx, sessionID, key, value, weight)
			if err != nil {
				b.Fatalf("failed to update tree: %v", err)
			}
			i++
		}
	})
}

// BenchmarkRedisSMST_Flush measures the performance of flushing a tree.
func BenchmarkRedisSMST_Flush(b *testing.B) {
	ctx := context.Background()
	client, cleanup := setupBenchRedis(b)
	defer cleanup()

	logger := polylog.Ctx(ctx)

	// Run benchmarks for different tree sizes
	sizes := []int{10, 100, 1000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			manager := NewRedisSMSTManager(
				logger,
				client,
				RedisSMSTManagerConfig{
					SupplierAddress: "pokt1supplier1",
				},
			)
			defer manager.Close()

			for i := 0; i < b.N; i++ {
				sessionID := fmt.Sprintf("bench-flush-%d", i)

				// Populate tree
				for j := 0; j < size; j++ {
					key := []byte(fmt.Sprintf("relay-%d", j))
					value := []byte(fmt.Sprintf("data-%d", j))
					weight := uint64(100 + j)
					_ = manager.UpdateTree(ctx, sessionID, key, value, weight)
				}

				b.StartTimer()
				_, err := manager.FlushTree(ctx, sessionID)
				b.StopTimer()

				if err != nil {
					b.Fatalf("failed to flush tree: %v", err)
				}
			}
		})
	}
}

// BenchmarkRedisSMST_ProveClosest measures the performance of proof generation.
func BenchmarkRedisSMST_ProveClosest(b *testing.B) {
	ctx := context.Background()
	client, cleanup := setupBenchRedis(b)
	defer cleanup()

	logger := polylog.Ctx(ctx)

	// Run benchmarks for different tree sizes
	sizes := []int{10, 100, 1000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			manager := NewRedisSMSTManager(
				logger,
				client,
				RedisSMSTManagerConfig{
					SupplierAddress: "pokt1supplier1",
				},
			)
			defer manager.Close()

			sessionID := fmt.Sprintf("bench-prove-%d", size)

			// Populate and flush tree
			for j := 0; j < size; j++ {
				key := []byte(fmt.Sprintf("relay-%d", j))
				value := []byte(fmt.Sprintf("data-%d", j))
				weight := uint64(100 + j)
				_ = manager.UpdateTree(ctx, sessionID, key, value, weight)
			}
			_, _ = manager.FlushTree(ctx, sessionID)

			// Path to prove
			path := []byte("relay-42")

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := manager.ProveClosest(ctx, sessionID, path)
				if err != nil {
					b.Fatalf("failed to prove closest: %v", err)
				}
			}
		})
	}
}

// BenchmarkRedisSMST_GetTreeStats measures the performance of getting tree statistics.
func BenchmarkRedisSMST_GetTreeStats(b *testing.B) {
	ctx := context.Background()
	client, cleanup := setupBenchRedis(b)
	defer cleanup()

	logger := polylog.Ctx(ctx)
	manager := NewRedisSMSTManager(
		logger,
		client,
		RedisSMSTManagerConfig{
			SupplierAddress: "pokt1supplier1",
		},
	)
	defer manager.Close()

	sessionID := "bench-stats"

	// Populate tree
	for j := 0; j < 100; j++ {
		key := []byte(fmt.Sprintf("relay-%d", j))
		value := []byte(fmt.Sprintf("data-%d", j))
		weight := uint64(100 + j)
		_ = manager.UpdateTree(ctx, sessionID, key, value, weight)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := manager.GetTreeStats(sessionID)
		if err != nil {
			b.Fatalf("failed to get tree stats: %v", err)
		}
	}
}

// BenchmarkRedisMapStore_Operations measures the performance of individual MapStore operations.
func BenchmarkRedisMapStore_Operations(b *testing.B) {
	ctx := context.Background()
	client, cleanup := setupBenchRedis(b)
	defer cleanup()

	sessionID := "bench-mapstore"
	store := NewRedisMapStore(ctx, client, sessionID)

	b.Run("Set", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			key := []byte(fmt.Sprintf("key-%d", i))
			value := []byte(fmt.Sprintf("value-%d", i))
			err := store.Set(key, value)
			if err != nil {
				b.Fatalf("failed to set: %v", err)
			}
		}
	})

	b.Run("Get", func(b *testing.B) {
		// Pre-populate
		key := []byte("test-key")
		value := []byte("test-value")
		_ = store.Set(key, value)

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := store.Get(key)
			if err != nil {
				b.Fatalf("failed to get: %v", err)
			}
		}
	})

	b.Run("Delete", func(b *testing.B) {
		// Pre-populate
		for i := 0; i < b.N; i++ {
			key := []byte(fmt.Sprintf("del-key-%d", i))
			value := []byte(fmt.Sprintf("del-value-%d", i))
			_ = store.Set(key, value)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := []byte(fmt.Sprintf("del-key-%d", i))
			err := store.Delete(key)
			if err != nil {
				b.Fatalf("failed to delete: %v", err)
			}
		}
	})

	b.Run("Len", func(b *testing.B) {
		// Pre-populate
		for i := 0; i < 100; i++ {
			key := []byte(fmt.Sprintf("len-key-%d", i))
			value := []byte(fmt.Sprintf("len-value-%d", i))
			_ = store.Set(key, value)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := store.Len()
			if err != nil {
				b.Fatalf("failed to get len: %v", err)
			}
		}
	})
}

// BenchmarkRedisSMST_RebuildFromWAL measures WAL recovery performance.
func BenchmarkRedisSMST_RebuildFromWAL(b *testing.B) {
	ctx := context.Background()
	client, cleanup := setupBenchRedis(b)
	defer cleanup()

	logger := polylog.Ctx(ctx)

	// Run benchmarks for different WAL sizes
	sizes := []int{10, 100, 1000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("entries=%d", size), func(b *testing.B) {
			// Create WAL entries
			walEntries := make([]*WALEntry, size)
			for i := 0; i < size; i++ {
				walEntries[i] = &WALEntry{
					RelayHash:    []byte(fmt.Sprintf("relay-%d", i)),
					RelayBytes:   []byte(fmt.Sprintf("data-%d", i)),
					ComputeUnits: uint64(100 + i),
				}
			}

			manager := NewRedisSMSTManager(
				logger,
				client,
				RedisSMSTManagerConfig{
					SupplierAddress: "pokt1supplier1",
				},
			)
			defer manager.Close()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sessionID := fmt.Sprintf("bench-wal-%d", i)
				err := manager.RebuildFromWAL(ctx, sessionID, walEntries)
				if err != nil {
					b.Fatalf("failed to rebuild from WAL: %v", err)
				}
			}
		})
	}
}
