package cmd

import (
	"runtime"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisPoolSettings contains common Redis connection pool settings.
// Used by both miner and relayer configurations.
type RedisPoolSettings struct {
	PoolSize               int
	MinIdleConns           int
	PoolTimeoutSeconds     int
	ConnMaxIdleTimeSeconds int
}

// applyRedisPoolConfig applies connection pool settings to Redis options.
// Defaults to 2x go-redis library defaults for production workloads.
func applyRedisPoolConfig(opts *redis.Options, settings RedisPoolSettings) {
	numCPU := runtime.GOMAXPROCS(0)

	// PoolSize: 20 × GOMAXPROCS (2x go-redis default of 10 × GOMAXPROCS)
	if settings.PoolSize > 0 {
		opts.PoolSize = settings.PoolSize
	} else {
		opts.PoolSize = 20 * numCPU
	}

	// MinIdleConns: PoolSize / 4 (keeps connections warm, eliminates dial latency)
	if settings.MinIdleConns > 0 {
		opts.MinIdleConns = settings.MinIdleConns
	} else {
		opts.MinIdleConns = opts.PoolSize / 4
	}

	// PoolTimeout: 4 seconds (fail fast if pool exhausted)
	if settings.PoolTimeoutSeconds > 0 {
		opts.PoolTimeout = time.Duration(settings.PoolTimeoutSeconds) * time.Second
	} else {
		opts.PoolTimeout = 4 * time.Second
	}

	// ConnMaxIdleTime: 5 minutes (recycle idle connections)
	if settings.ConnMaxIdleTimeSeconds > 0 {
		opts.ConnMaxIdleTime = time.Duration(settings.ConnMaxIdleTimeSeconds) * time.Second
	} else {
		opts.ConnMaxIdleTime = 5 * time.Minute
	}
}
