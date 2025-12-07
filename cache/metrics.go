package cache

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	metricsNamespace = "ha"
	metricsSubsystem = "cache"

	// Cache levels for metrics labels
	CacheLevelL1      = "l1"       // In-memory cache
	CacheLevelL2      = "l2"       // Redis cache
	CacheLevelL2Retry = "l2_retry" // Redis cache after waiting for lock
	CacheLevelL3      = "l3"       // Chain query

	// Cache invalidation sources
	SourceManual = "manual" // Manual invalidation via API/code
	SourcePubSub = "pubsub" // Invalidation via pub/sub event

	// Result labels for metrics
	ResultSuccess = "success"
	ResultFailure = "failure"
)

var (
	// Cache hit/miss metrics
	cacheHits = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "hits_total",
			Help:      "Total number of cache hits",
		},
		[]string{"cache_type", "level"}, // level: l1, l2, l2_retry
	)

	cacheMisses = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "misses_total",
			Help:      "Total number of cache misses",
		},
		[]string{"cache_type", "level"},
	)

	cacheInvalidations = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "invalidations_total",
			Help:      "Total number of cache invalidations",
		},
		[]string{"cache_type", "source"}, // source: manual, pubsub
	)

	// Chain query metrics
	chainQueries = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "chain_queries_total",
			Help:      "Total number of chain queries (cache misses that hit chain)",
		},
		[]string{"query_type"},
	)

	chainQueryErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "chain_query_errors_total",
			Help:      "Total number of chain query errors",
		},
		[]string{"query_type"},
	)

	// chainQueryLatency tracks latency of chain queries (for future use)
	_ = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "chain_query_latency_seconds",
			Help:      "Latency of chain queries",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"query_type"},
	)

	// Session cache specific metrics
	sessionRewardableChecks = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "session_rewardable_checks_total",
			Help:      "Total number of session rewardability checks",
		},
		[]string{"result"}, // result: rewardable, non_rewardable
	)

	sessionMarkedNonRewardable = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "sessions_marked_non_rewardable_total",
			Help:      "Total number of sessions marked as non-rewardable",
		},
		[]string{"reason"},
	)

	// Block event metrics
	blockEventsPublished = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "block_events_published_total",
			Help:      "Total number of block events published",
		},
	)

	blockEventsReceived = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "block_events_received_total",
			Help:      "Total number of block events received",
		},
	)

	currentBlockHeight = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "current_block_height",
			Help:      "Current block height as seen by the cache",
		},
	)

	// lockAcquisitions tracks distributed lock acquisitions (for future use)
	_ = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "lock_acquisitions_total",
			Help:      "Total number of distributed lock acquisitions",
		},
		[]string{"lock_type", "result"}, // result: success, failed
	)

	// ========================================================================
	// Cache Orchestrator Metrics (for unified cache architecture)
	// ========================================================================

	// cacheOrchestratorRefreshes tracks the number of complete refresh cycles.
	// Each refresh cycle updates all caches in parallel.
	cacheOrchestratorRefreshes = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "orchestrator_refreshes_total",
			Help:      "Total number of cache orchestrator refresh cycles",
		},
		[]string{"result"}, // result: success, failure
	)

	// cacheOrchestratorRefreshDuration tracks how long each refresh cycle takes.
	// This includes the time to refresh all caches in parallel.
	cacheOrchestratorRefreshDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "orchestrator_refresh_duration_seconds",
			Help:      "Duration of cache orchestrator refresh cycles",
			Buckets:   []float64{0.1, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0}, // 100ms to 30s
		},
	)

	// cacheOrchestratorLeaderStatus indicates whether this instance is the global leader.
	// 1 = leader (performs cache refresh), 0 = follower (only consumes cache).
	cacheOrchestratorLeaderStatus = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "orchestrator_leader_status",
			Help:      "Whether this instance is the global cache refresh leader (1=leader, 0=follower)",
		},
	)

	// cacheRefreshDuration tracks the duration of individual cache refresh operations.
	// Each cache type (application, service, params, etc.) is tracked separately.
	cacheRefreshDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "refresh_duration_seconds",
			Help:      "Duration of individual cache refresh operations",
			Buckets:   []float64{0.01, 0.05, 0.1, 0.5, 1.0, 5.0}, // 10ms to 5s
		},
		[]string{"cache_type"}, // cache_type: application, service, shared_params, etc.
	)

	// cacheRefreshErrors tracks refresh errors for each cache type.
	cacheRefreshErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "refresh_errors_total",
			Help:      "Total number of cache refresh errors",
		},
		[]string{"cache_type"},
	)
)
