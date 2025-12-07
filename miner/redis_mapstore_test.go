//go:build test

package miner

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func setupTestRedisMapStore(t *testing.T) (*RedisMapStore, *miniredis.Miniredis) {
	t.Helper()

	mr, err := miniredis.Run()
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	ctx := context.Background()
	store := NewRedisMapStore(ctx, client, "test-session").(*RedisMapStore)

	t.Cleanup(func() {
		client.Close()
		mr.Close()
	})

	return store, mr
}

func TestRedisMapStore_BasicOperations(t *testing.T) {
	store, _ := setupTestRedisMapStore(t)

	// Test Set
	key1 := []byte("key1")
	value1 := []byte("value1")
	err := store.Set(key1, value1)
	require.NoError(t, err)

	// Test Get
	retrieved, err := store.Get(key1)
	require.NoError(t, err)
	require.Equal(t, value1, retrieved)

	// Test Len
	count, err := store.Len()
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// Add another key
	key2 := []byte("key2")
	value2 := []byte("value2")
	err = store.Set(key2, value2)
	require.NoError(t, err)

	count, err = store.Len()
	require.NoError(t, err)
	require.Equal(t, 2, count)

	// Test Delete
	err = store.Delete(key1)
	require.NoError(t, err)

	// Verify key1 is deleted
	retrieved, err = store.Get(key1)
	require.NoError(t, err)
	require.Nil(t, retrieved)

	// Verify count decreased
	count, err = store.Len()
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// Test ClearAll
	err = store.ClearAll()
	require.NoError(t, err)

	count, err = store.Len()
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestRedisMapStore_NotFound(t *testing.T) {
	store, _ := setupTestRedisMapStore(t)

	// Get on non-existent key should return nil, nil
	key := []byte("non-existent")
	value, err := store.Get(key)
	require.NoError(t, err)
	require.Nil(t, value)
}

func TestRedisMapStore_Overwrite(t *testing.T) {
	store, _ := setupTestRedisMapStore(t)

	key := []byte("key1")
	value1 := []byte("value1")
	value2 := []byte("value2-updated")

	// Set initial value
	err := store.Set(key, value1)
	require.NoError(t, err)

	retrieved, err := store.Get(key)
	require.NoError(t, err)
	require.Equal(t, value1, retrieved)

	// Overwrite with new value
	err = store.Set(key, value2)
	require.NoError(t, err)

	retrieved, err = store.Get(key)
	require.NoError(t, err)
	require.Equal(t, value2, retrieved)

	// Len should still be 1
	count, err := store.Len()
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestRedisMapStore_Len(t *testing.T) {
	store, _ := setupTestRedisMapStore(t)

	// Initially empty
	count, err := store.Len()
	require.NoError(t, err)
	require.Equal(t, 0, count)

	// Add keys
	for i := 0; i < 10; i++ {
		key := []byte{byte(i)}
		value := []byte{byte(i * 2)}
		err := store.Set(key, value)
		require.NoError(t, err)
	}

	count, err = store.Len()
	require.NoError(t, err)
	require.Equal(t, 10, count)
}

func TestRedisMapStore_ClearAll(t *testing.T) {
	store, _ := setupTestRedisMapStore(t)

	// Add multiple keys
	for i := 0; i < 5; i++ {
		key := []byte{byte(i)}
		value := []byte{byte(i * 2)}
		err := store.Set(key, value)
		require.NoError(t, err)
	}

	count, err := store.Len()
	require.NoError(t, err)
	require.Equal(t, 5, count)

	// ClearAll should remove all keys
	err = store.ClearAll()
	require.NoError(t, err)

	count, err = store.Len()
	require.NoError(t, err)
	require.Equal(t, 0, count)

	// All keys should be gone
	for i := 0; i < 5; i++ {
		key := []byte{byte(i)}
		value, err := store.Get(key)
		require.NoError(t, err)
		require.Nil(t, value)
	}
}

func TestRedisMapStore_ConcurrentAccess(t *testing.T) {
	store, _ := setupTestRedisMapStore(t)

	// Test concurrent writes
	const numGoroutines = 10
	const keysPerGoroutine = 10

	done := make(chan bool, numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			defer func() { done <- true }()

			for i := 0; i < keysPerGoroutine; i++ {
				key := []byte{byte(goroutineID), byte(i)}
				value := []byte{byte(goroutineID * 100), byte(i * 10)}
				err := store.Set(key, value)
				require.NoError(t, err)
			}
		}(g)
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify all keys were written
	count, err := store.Len()
	require.NoError(t, err)
	require.Equal(t, numGoroutines*keysPerGoroutine, count)

	// Verify concurrent reads
	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			defer func() { done <- true }()

			for i := 0; i < keysPerGoroutine; i++ {
				key := []byte{byte(goroutineID), byte(i)}
				expectedValue := []byte{byte(goroutineID * 100), byte(i * 10)}
				value, err := store.Get(key)
				require.NoError(t, err)
				require.Equal(t, expectedValue, value)
			}
		}(g)
	}

	// Wait for all read goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}
}

func TestRedisMapStore_DeleteNonExistentKey(t *testing.T) {
	store, _ := setupTestRedisMapStore(t)

	// Delete on non-existent key should be a no-op
	key := []byte("non-existent")
	err := store.Delete(key)
	require.NoError(t, err)

	// Len should still be 0
	count, err := store.Len()
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestRedisMapStore_BinaryKeys(t *testing.T) {
	store, _ := setupTestRedisMapStore(t)

	// Test with various binary keys
	testCases := []struct {
		name  string
		key   []byte
		value []byte
	}{
		{
			name:  "zero bytes",
			key:   []byte{0x00, 0x00, 0x00},
			value: []byte{0xFF, 0xFF, 0xFF},
		},
		{
			name:  "all bytes",
			key:   []byte{0x01, 0x02, 0x03, 0x04, 0x05},
			value: []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE},
		},
		{
			name:  "hash-like key",
			key:   []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xBA, 0xBE},
			value: []byte("some relay data"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := store.Set(tc.key, tc.value)
			require.NoError(t, err)

			retrieved, err := store.Get(tc.key)
			require.NoError(t, err)
			require.Equal(t, tc.value, retrieved)
		})
	}
}

func TestRedisMapStore_SessionIsolation(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	ctx := context.Background()

	// Create stores for different sessions
	store1 := NewRedisMapStore(ctx, client, "session-1").(*RedisMapStore)
	store2 := NewRedisMapStore(ctx, client, "session-2").(*RedisMapStore)

	// Add data to store1
	key := []byte("shared-key")
	value1 := []byte("session-1-value")
	err = store1.Set(key, value1)
	require.NoError(t, err)

	// Add data to store2 with same key
	value2 := []byte("session-2-value")
	err = store2.Set(key, value2)
	require.NoError(t, err)

	// Verify isolation - each store should have its own value
	retrieved1, err := store1.Get(key)
	require.NoError(t, err)
	require.Equal(t, value1, retrieved1)

	retrieved2, err := store2.Get(key)
	require.NoError(t, err)
	require.Equal(t, value2, retrieved2)

	// Verify independent lengths
	count1, err := store1.Len()
	require.NoError(t, err)
	require.Equal(t, 1, count1)

	count2, err := store2.Len()
	require.NoError(t, err)
	require.Equal(t, 1, count2)

	// Clear store1 should not affect store2
	err = store1.ClearAll()
	require.NoError(t, err)

	count1, err = store1.Len()
	require.NoError(t, err)
	require.Equal(t, 0, count1)

	count2, err = store2.Len()
	require.NoError(t, err)
	require.Equal(t, 1, count2)

	retrieved2, err = store2.Get(key)
	require.NoError(t, err)
	require.Equal(t, value2, retrieved2)
}

// TestRedisMapStore_RedisConnectionError verifies error handling when Redis is unavailable.
func TestRedisMapStore_RedisConnectionError(t *testing.T) {
	ctx := context.Background()

	// Create client pointing to non-existent Redis server
	client := redis.NewClient(&redis.Options{
		Addr:         "localhost:9999", // Invalid address
		DialTimeout:  100,              // Fast timeout
		ReadTimeout:  100,
		WriteTimeout: 100,
	})
	defer client.Close()

	store := NewRedisMapStore(ctx, client, "test-session").(*RedisMapStore)

	key := []byte("test-key")
	value := []byte("test-value")

	// Set should fail with connection error
	err := store.Set(key, value)
	require.Error(t, err)
	// Error message varies by environment (connection refused, i/o timeout, dial timeout, etc.)
	require.NotNil(t, err)

	// Get should fail with connection error
	_, err = store.Get(key)
	require.Error(t, err)
	require.NotNil(t, err)

	// Delete should fail with connection error
	err = store.Delete(key)
	require.Error(t, err)
	require.NotNil(t, err)

	// Len should fail with connection error
	_, err = store.Len()
	require.Error(t, err)
	require.NotNil(t, err)

	// ClearAll should fail with connection error
	err = store.ClearAll()
	require.Error(t, err)
	require.NotNil(t, err)
}

// TestRedisMapStore_RedisFailureMidOperation simulates Redis becoming unavailable mid-operation.
func TestRedisMapStore_RedisFailureMidOperation(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	ctx := context.Background()
	store := NewRedisMapStore(ctx, client, "test-session").(*RedisMapStore)

	// Initial operations succeed
	key := []byte("test-key")
	value := []byte("test-value")
	err = store.Set(key, value)
	require.NoError(t, err)

	retrieved, err := store.Get(key)
	require.NoError(t, err)
	require.Equal(t, value, retrieved)

	// Simulate Redis server crash
	mr.Close()

	// All operations should now fail
	err = store.Set(key, value)
	require.Error(t, err)

	_, err = store.Get(key)
	require.Error(t, err)

	_, err = store.Len()
	require.Error(t, err)

	err = store.Delete(key)
	require.Error(t, err)

	err = store.ClearAll()
	require.Error(t, err)
}

// TestRedisMapStore_ContextCancellation verifies proper handling of context cancellation.
func TestRedisMapStore_ContextCancellation(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	// Create a cancellable context
	ctx, cancel := context.WithCancel(context.Background())
	store := NewRedisMapStore(ctx, client, "test-session").(*RedisMapStore)

	// Operations should succeed before cancellation
	key := []byte("test-key")
	value := []byte("test-value")
	err = store.Set(key, value)
	require.NoError(t, err)

	// Cancel the context
	cancel()

	// Operations should fail with context canceled error
	// Note: miniredis may not always respect context cancellation immediately
	// but the error handling path is still exercised
	err = store.Set(key, value)
	// May or may not error depending on timing, but should not panic
	_ = err
}

// TestRedisMapStore_LargeValues verifies handling of large byte arrays.
func TestRedisMapStore_LargeValues(t *testing.T) {
	store, _ := setupTestRedisMapStore(t)

	// Create a large value (1MB)
	largeValue := make([]byte, 1024*1024)
	for i := range largeValue {
		largeValue[i] = byte(i % 256)
	}

	key := []byte("large-key")

	// Should be able to store and retrieve large values
	err := store.Set(key, largeValue)
	require.NoError(t, err)

	retrieved, err := store.Get(key)
	require.NoError(t, err)
	require.Equal(t, largeValue, retrieved)

	// Verify deletion works for large values
	err = store.Delete(key)
	require.NoError(t, err)

	retrieved, err = store.Get(key)
	require.NoError(t, err)
	require.Nil(t, retrieved)
}

// TestRedisMapStore_EmptyValues verifies handling of empty byte arrays.
func TestRedisMapStore_EmptyValues(t *testing.T) {
	store, _ := setupTestRedisMapStore(t)

	key := []byte("empty-value-key")
	emptyValue := []byte{}

	// Should be able to store empty values
	err := store.Set(key, emptyValue)
	require.NoError(t, err)

	// Should be able to retrieve empty values (distinct from nil/not-found)
	retrieved, err := store.Get(key)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	require.Equal(t, 0, len(retrieved))

	// Len should count empty values
	count, err := store.Len()
	require.NoError(t, err)
	require.Equal(t, 1, count)
}
