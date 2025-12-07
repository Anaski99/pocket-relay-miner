package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/pokt-network/pocket-relay-miner/logging"
	"github.com/pokt-network/poktroll/pkg/client"
	"github.com/pokt-network/poktroll/pkg/crypto"
)

const (
	// Redis key for persisted application addresses
	discoveredAppsKey = "ha:discovered_apps"

	// Default warmup concurrency
	defaultWarmupConcurrency = 10

	// Default warmup timeout per application
	defaultWarmupTimeout = 5 * time.Second
)

// CacheWarmerConfig contains configuration for the cache warmer.
type CacheWarmerConfig struct {
	// KnownApplications is a list of application addresses to pre-warm on startup.
	// These are configured by the operator.
	KnownApplications []string

	// PersistDiscoveredApps enables saving discovered app addresses to Redis
	// for faster warmup on subsequent restarts.
	PersistDiscoveredApps bool

	// WarmupConcurrency is the number of parallel warmup operations.
	// Higher values = faster warmup but more load on the chain.
	WarmupConcurrency int

	// WarmupTimeout is the timeout for warming each application.
	WarmupTimeout time.Duration
}

// CacheWarmer handles pre-warming caches for faster request processing.
type CacheWarmer struct {
	logger logging.Logger
	config CacheWarmerConfig

	// Redis client for persistence
	redisClient redis.UniversalClient

	// Query clients for warming
	appClient     client.ApplicationQueryClient
	accountClient client.AccountQueryClient
	sharedClient  client.SharedQueryClient

	// Ring client for warming rings
	ringClient crypto.RingClient

	// Discovered apps during runtime (to persist later)
	discoveredApps   map[string]struct{}
	discoveredAppsMu sync.RWMutex

	// Metrics
	warmedApps   int64
	failedApps   int64
	warmupTimeMs int64
}

// NewCacheWarmer creates a new cache warmer.
func NewCacheWarmer(
	logger logging.Logger,
	config CacheWarmerConfig,
	redisClient redis.UniversalClient,
	appClient client.ApplicationQueryClient,
	accountClient client.AccountQueryClient,
	sharedClient client.SharedQueryClient,
	ringClient crypto.RingClient,
) *CacheWarmer {
	if config.WarmupConcurrency == 0 {
		config.WarmupConcurrency = defaultWarmupConcurrency
	}
	if config.WarmupTimeout == 0 {
		config.WarmupTimeout = defaultWarmupTimeout
	}

	return &CacheWarmer{
		logger:         logger.With().Str("component", "cache_warmer").Logger(),
		config:         config,
		redisClient:    redisClient,
		appClient:      appClient,
		accountClient:  accountClient,
		sharedClient:   sharedClient,
		ringClient:     ringClient,
		discoveredApps: make(map[string]struct{}),
	}
}

// WarmupResult contains the results of a warmup operation.
type WarmupResult struct {
	TotalApps   int
	WarmedApps  int
	FailedApps  int
	DurationMs  int64
	FailedAddrs []string
}

// Warmup pre-warms caches for known applications.
// This should be called at startup for fastest first-request performance.
func (w *CacheWarmer) Warmup(ctx context.Context) (*WarmupResult, error) {
	startTime := time.Now()

	// Collect all apps to warm: config + redis persisted
	appsToWarm := w.collectAppsToWarm(ctx)
	if len(appsToWarm) == 0 {
		w.logger.Info().Msg("no applications to warm up")
		return &WarmupResult{}, nil
	}

	w.logger.Info().
		Int("total_apps", len(appsToWarm)).
		Int("concurrency", w.config.WarmupConcurrency).
		Msg("starting cache warmup")

	// Warm in parallel
	result := w.warmAppsParallel(ctx, appsToWarm)
	result.DurationMs = time.Since(startTime).Milliseconds()

	// Update metrics
	atomic.StoreInt64(&w.warmedApps, int64(result.WarmedApps))
	atomic.StoreInt64(&w.failedApps, int64(result.FailedApps))
	atomic.StoreInt64(&w.warmupTimeMs, result.DurationMs)

	w.logger.Info().
		Int("warmed", result.WarmedApps).
		Int("failed", result.FailedApps).
		Int64("duration_ms", result.DurationMs).
		Msg("cache warmup complete")

	return result, nil
}

// collectAppsToWarm collects all application addresses to warm.
func (w *CacheWarmer) collectAppsToWarm(ctx context.Context) []string {
	appSet := make(map[string]struct{})

	// Add configured known applications
	for _, addr := range w.config.KnownApplications {
		if addr != "" {
			appSet[addr] = struct{}{}
		}
	}

	// Load persisted apps from Redis (if enabled and available)
	if w.config.PersistDiscoveredApps && w.redisClient != nil {
		persistedApps, err := w.redisClient.SMembers(ctx, discoveredAppsKey).Result()
		if err != nil {
			w.logger.Warn().Err(err).Msg("failed to load persisted apps from Redis")
		} else {
			for _, addr := range persistedApps {
				appSet[addr] = struct{}{}
			}
			w.logger.Debug().
				Int("persisted_apps", len(persistedApps)).
				Msg("loaded persisted apps from Redis")
		}
	}

	// Convert to slice
	apps := make([]string, 0, len(appSet))
	for addr := range appSet {
		apps = append(apps, addr)
	}
	return apps
}

// warmAppsParallel warms applications in parallel for speed.
func (w *CacheWarmer) warmAppsParallel(ctx context.Context, apps []string) *WarmupResult {
	result := &WarmupResult{
		TotalApps: len(apps),
	}

	// Create work channel
	workCh := make(chan string, len(apps))
	for _, app := range apps {
		workCh <- app
	}
	close(workCh)

	// Results collection
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Launch workers
	for i := 0; i < w.config.WarmupConcurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for appAddr := range workCh {
				warmCtx, cancel := context.WithTimeout(ctx, w.config.WarmupTimeout)
				err := w.warmApp(warmCtx, appAddr)
				cancel()

				mu.Lock()
				if err != nil {
					result.FailedApps++
					result.FailedAddrs = append(result.FailedAddrs, appAddr)
					w.logger.Debug().
						Err(err).
						Str("app_address", appAddr).
						Int("worker", workerID).
						Msg("failed to warm app")
				} else {
					result.WarmedApps++
					w.logger.Debug().
						Str("app_address", appAddr).
						Int("worker", workerID).
						Msg("warmed app successfully")
				}
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()
	return result
}

// warmApp warms caches for a single application.
func (w *CacheWarmer) warmApp(ctx context.Context, appAddr string) error {
	// 1. Warm application cache
	app, err := w.appClient.GetApplication(ctx, appAddr)
	if err != nil {
		return err
	}

	// 2. Warm account cache for app address
	_, err = w.accountClient.GetPubKeyFromAddress(ctx, appAddr)
	if err != nil {
		return err
	}

	// 3. Warm account cache for delegated gateways
	for _, gatewayAddr := range app.DelegateeGatewayAddresses {
		_, err = w.accountClient.GetPubKeyFromAddress(ctx, gatewayAddr)
		if err != nil {
			w.logger.Debug().
				Err(err).
				Str("gateway_address", gatewayAddr).
				Msg("failed to warm gateway account (may be okay if not on chain)")
			// Don't fail - gateway might not have a registered account yet
		}
	}

	// 4. Warm shared params (only once, but safe to call multiple times)
	_, _ = w.sharedClient.GetParams(ctx)

	return nil
}

// RecordDiscoveredApp records a newly discovered application address.
// This should be called when processing relays from new applications.
func (w *CacheWarmer) RecordDiscoveredApp(ctx context.Context, appAddr string) {
	if appAddr == "" || !w.config.PersistDiscoveredApps {
		return
	}

	// Check if already known
	w.discoveredAppsMu.RLock()
	_, known := w.discoveredApps[appAddr]
	w.discoveredAppsMu.RUnlock()

	if known {
		return
	}

	// Add to local set
	w.discoveredAppsMu.Lock()
	w.discoveredApps[appAddr] = struct{}{}
	w.discoveredAppsMu.Unlock()

	// Persist to Redis asynchronously
	if w.redisClient != nil {
		go func() {
			persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err := w.redisClient.SAdd(persistCtx, discoveredAppsKey, appAddr).Err()
			if err != nil {
				w.logger.Warn().
					Err(err).
					Str("app_address", appAddr).
					Msg("failed to persist discovered app to Redis")
			} else {
				w.logger.Debug().
					Str("app_address", appAddr).
					Msg("persisted discovered app to Redis")
			}
		}()
	}
}

// RemoveDiscoveredApp removes an application from the discovered list.
// This should be called when an app is found to be invalid/unstaked.
func (w *CacheWarmer) RemoveDiscoveredApp(ctx context.Context, appAddr string) {
	if appAddr == "" {
		return
	}

	// Remove from local set
	w.discoveredAppsMu.Lock()
	delete(w.discoveredApps, appAddr)
	w.discoveredAppsMu.Unlock()

	// Remove from Redis
	if w.redisClient != nil && w.config.PersistDiscoveredApps {
		go func() {
			persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err := w.redisClient.SRem(persistCtx, discoveredAppsKey, appAddr).Err()
			if err != nil {
				w.logger.Warn().
					Err(err).
					Str("app_address", appAddr).
					Msg("failed to remove app from Redis")
			}
		}()
	}
}

// GetStats returns warmup statistics.
func (w *CacheWarmer) GetStats() (warmed, failed int64, warmupMs int64) {
	return atomic.LoadInt64(&w.warmedApps),
		atomic.LoadInt64(&w.failedApps),
		atomic.LoadInt64(&w.warmupTimeMs)
}

// GetDiscoveredAppsCount returns the number of discovered apps.
func (w *CacheWarmer) GetDiscoveredAppsCount() int {
	w.discoveredAppsMu.RLock()
	defer w.discoveredAppsMu.RUnlock()
	return len(w.discoveredApps)
}
