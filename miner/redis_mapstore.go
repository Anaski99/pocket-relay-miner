package miner

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/pokt-network/smt/kvstore"
	"github.com/redis/go-redis/v9"

	"github.com/pokt-network/pocket-relay-miner/observability"
)

// RedisMapStore implements kvstore.MapStore using Redis hashes.
// This enables shared storage across HA instances, avoiding local disk IOPS issues.
//
// The RedisMapStore uses a single Redis hash to store all key-value pairs for a session's SMST.
// This provides O(1) access for Get/Set/Delete operations and enables instant failover since
// all instances can access the same Redis data.
//
// Redis Hash Structure:
//
//	Key: "ha:smst:{sessionID}:nodes"
//	Fields: hex-encoded SMST node keys
//	Values: raw SMST node data
type RedisMapStore struct {
	redisClient redis.UniversalClient
	hashKey     string // Redis hash key: "ha:smst:{sessionID}:nodes"
	ctx         context.Context
}

// NewRedisMapStore creates a new Redis-backed MapStore for a session.
// The store uses a Redis hash to persist SMST nodes, enabling shared access across HA instances.
//
// Parameters:
//   - ctx: Context for Redis operations
//   - redisClient: Redis client (supports standalone, sentinel, and cluster)
//   - sessionID: Unique session identifier used to namespace the Redis hash
//
// Returns:
//
//	A MapStore implementation backed by Redis
func NewRedisMapStore(
	ctx context.Context,
	redisClient redis.UniversalClient,
	sessionID string,
) kvstore.MapStore {
	return &RedisMapStore{
		redisClient: redisClient,
		hashKey:     fmt.Sprintf("ha:smst:%s:nodes", sessionID),
		ctx:         ctx,
	}
}

// Get retrieves a value from the Redis hash.
//
// The key is hex-encoded before being used as a Redis hash field name,
// since Redis requires string field names but SMST keys are byte arrays.
//
// Returns nil, nil if the key doesn't exist (per MapStore interface contract).
func (s *RedisMapStore) Get(key []byte) ([]byte, error) {
	start := time.Now()
	defer func() {
		observability.SMSTRedisOperationDuration.WithLabelValues("get").Observe(time.Since(start).Seconds())
	}()

	// Convert key to hex string for Redis field name
	field := hex.EncodeToString(key)

	val, err := s.redisClient.HGet(s.ctx, s.hashKey, field).Bytes()
	if err == redis.Nil {
		observability.SMSTRedisOperations.WithLabelValues("get", "not_found").Inc()
		return nil, nil // Key not found - return nil, nil per interface contract
	}
	if err != nil {
		observability.SMSTRedisOperations.WithLabelValues("get", "error").Inc()
		observability.SMSTRedisErrors.WithLabelValues("get", "redis_error").Inc()
		return nil, err
	}
	observability.SMSTRedisOperations.WithLabelValues("get", "success").Inc()
	return val, nil
}

// Set stores a value in the Redis hash.
//
// The key is hex-encoded before being used as a Redis hash field name.
// If the key already exists, its value is overwritten.
func (s *RedisMapStore) Set(key, value []byte) error {
	start := time.Now()
	defer func() {
		observability.SMSTRedisOperationDuration.WithLabelValues("set").Observe(time.Since(start).Seconds())
	}()

	field := hex.EncodeToString(key)
	err := s.redisClient.HSet(s.ctx, s.hashKey, field, value).Err()
	if err != nil {
		observability.SMSTRedisOperations.WithLabelValues("set", "error").Inc()
		observability.SMSTRedisErrors.WithLabelValues("set", "redis_error").Inc()
		return err
	}
	observability.SMSTRedisOperations.WithLabelValues("set", "success").Inc()
	return nil
}

// Delete removes a key from the Redis hash.
//
// If the key doesn't exist, this operation is a no-op and returns nil.
func (s *RedisMapStore) Delete(key []byte) error {
	start := time.Now()
	defer func() {
		observability.SMSTRedisOperationDuration.WithLabelValues("delete").Observe(time.Since(start).Seconds())
	}()

	field := hex.EncodeToString(key)
	err := s.redisClient.HDel(s.ctx, s.hashKey, field).Err()
	if err != nil {
		observability.SMSTRedisOperations.WithLabelValues("delete", "error").Inc()
		observability.SMSTRedisErrors.WithLabelValues("delete", "redis_error").Inc()
		return err
	}
	observability.SMSTRedisOperations.WithLabelValues("delete", "success").Inc()
	return nil
}

// Len returns the number of keys in the Redis hash.
//
// This operation is O(1) as it uses Redis's HLEN command.
func (s *RedisMapStore) Len() (int, error) {
	start := time.Now()
	defer func() {
		observability.SMSTRedisOperationDuration.WithLabelValues("len").Observe(time.Since(start).Seconds())
	}()

	count, err := s.redisClient.HLen(s.ctx, s.hashKey).Result()
	if err != nil {
		observability.SMSTRedisOperations.WithLabelValues("len", "error").Inc()
		observability.SMSTRedisErrors.WithLabelValues("len", "redis_error").Inc()
		return 0, err
	}
	observability.SMSTRedisOperations.WithLabelValues("len", "success").Inc()
	return int(count), nil
}

// ClearAll deletes the entire Redis hash.
//
// This is an atomic operation that removes all SMST nodes for the session.
// After calling ClearAll, Len() will return 0.
func (s *RedisMapStore) ClearAll() error {
	start := time.Now()
	defer func() {
		observability.SMSTRedisOperationDuration.WithLabelValues("clear_all").Observe(time.Since(start).Seconds())
	}()

	err := s.redisClient.Del(s.ctx, s.hashKey).Err()
	if err != nil {
		observability.SMSTRedisOperations.WithLabelValues("clear_all", "error").Inc()
		observability.SMSTRedisErrors.WithLabelValues("clear_all", "redis_error").Inc()
		return err
	}
	observability.SMSTRedisOperations.WithLabelValues("clear_all", "success").Inc()
	return nil
}

// Verify interface compliance at compile time.
var _ kvstore.MapStore = (*RedisMapStore)(nil)
