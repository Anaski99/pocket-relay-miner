package miner

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/rpc/client/http"
	"github.com/sourcegraph/conc/pool"

	haclient "github.com/pokt-network/pocket-relay-miner/client"
	"github.com/pokt-network/pocket-relay-miner/logging"
)

// SettlementMonitor watches for EventClaimSettled events from the blockchain
// and emits metrics for on-chain claim settlement results.
//
// This provides visibility into:
// - Proven vs Invalid vs Expired claims
// - Actual uPOKT earned from settlements
// - Settlement latency (blocks between session end and settlement)
type SettlementMonitor struct {
	logger          logging.Logger
	blockSubscriber *haclient.BlockSubscriber
	rpcClient       *http.HTTP
	suppliers       map[string]bool // Set of supplier addresses to monitor
	mu              sync.RWMutex
	workerPool      *pool.Pool // Worker pool for processing block_results (1GB+ in mainnet)
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
}

// NewSettlementMonitor creates a new settlement monitor.
func NewSettlementMonitor(
	logger logging.Logger,
	blockSubscriber *haclient.BlockSubscriber,
	rpcClient *http.HTTP,
	suppliers []string,
) *SettlementMonitor {
	// Build supplier set for fast lookup
	supplierSet := make(map[string]bool, len(suppliers))
	for _, supplier := range suppliers {
		supplierSet[supplier] = true
	}

	// Create worker pool for processing block_results asynchronously
	// Pool size: 2 workers (settlement events are rare, 1 per session)
	workerPool := pool.New().WithMaxGoroutines(2)

	return &SettlementMonitor{
		logger:          logging.ForComponent(logger, "settlement_monitor"),
		blockSubscriber: blockSubscriber,
		rpcClient:       rpcClient,
		suppliers:       supplierSet,
		workerPool:      workerPool,
	}
}

// Start begins monitoring for settlement events.
func (sm *SettlementMonitor) Start(ctx context.Context) error {
	sm.ctx, sm.cancel = context.WithCancel(ctx)

	// Subscribe to block events
	blockEvents := sm.blockSubscriber.Subscribe(sm.ctx, 100)

	sm.wg.Add(1)
	go func() {
		defer sm.wg.Done()

		sm.logger.Info().Msg("settlement monitor started")

		for {
			select {
			case <-sm.ctx.Done():
				sm.logger.Info().Msg("settlement monitor stopped")
				return

			case block, ok := <-blockEvents:
				if !ok {
					sm.logger.Warn().Msg("block events channel closed")
					return
				}

				// Submit to worker pool to avoid blocking on large block_results (1GB+ in mainnet)
				blockHeight := block.Height()
				sm.workerPool.Go(func() {
					sm.processBlock(blockHeight)
				})
			}
		}
	}()

	return nil
}

// Close stops the settlement monitor.
func (sm *SettlementMonitor) Close() {
	if sm.cancel != nil {
		sm.cancel()
	}
	sm.wg.Wait()

	// Wait for all pending block_results queries to complete
	sm.workerPool.Wait()

	sm.logger.Info().Msg("settlement monitor closed")
}

// AddSupplier adds a supplier address to monitor.
func (sm *SettlementMonitor) AddSupplier(supplier string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.suppliers[supplier] = true
}

// RemoveSupplier removes a supplier address from monitoring.
func (sm *SettlementMonitor) RemoveSupplier(supplier string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.suppliers, supplier)
}

// processBlock queries block_results for the given height and processes any EventClaimSettled events.
func (sm *SettlementMonitor) processBlock(height int64) {
	// Query block_results for this height
	result, err := sm.rpcClient.BlockResults(sm.ctx, &height)
	if err != nil {
		sm.logger.Warn().
			Err(err).
			Int64("height", height).
			Msg("failed to query block results")
		return
	}

	// Process all events in this block
	for _, event := range result.FinalizeBlockEvents {
		if event.Type == "pocket.tokenomics.EventClaimSettled" {
			sm.handleClaimSettled(event.Attributes, height)
		}
	}
}

// handleClaimSettled processes a single EventClaimSettled event.
func (sm *SettlementMonitor) handleClaimSettled(attributes []abcitypes.EventAttribute, settlementHeight int64) {
	// Extract attributes
	attrs := make(map[string]string)
	for _, attr := range attributes {
		attrs[attr.Key] = attr.Value
	}

	// Extract supplier address
	supplier := extractStringValue(attrs["supplier_operator_address"])

	// Check if this is one of our suppliers
	sm.mu.RLock()
	isOurs := sm.suppliers[supplier]
	sm.mu.RUnlock()

	if !isOurs {
		return // Not one of our suppliers, skip
	}

	// Extract other attributes
	serviceID := extractStringValue(attrs["service_id"])
	statusInt := extractIntValue(attrs["claim_proof_status_int"])
	numRelays := extractIntValue(attrs["num_relays"])
	computeUnits := extractIntValue(attrs["num_claimed_compute_units"])
	sessionEndHeight := extractIntValue(attrs["session_end_block_height"])

	// Map status int to status string
	// 0 = CLAIMED, 1 = PROVEN, 2 = (not used), 3 = EXPIRED
	// ClaimProofStage: CLAIMED=0, PROVEN=1, SETTLED=2, EXPIRED=3
	var status string
	switch statusInt {
	case 1:
		status = "proven"
	case 2:
		status = "invalid" // Not used in practice but handle it
	case 3:
		status = "expired"
	default:
		status = "unknown"
	}

	// Extract uPOKT earned (from reward_distribution JSON)
	upoktEarned := int64(0)
	if rewardDistJSON := attrs["reward_distribution"]; rewardDistJSON != "" {
		upoktEarned = sm.extractSupplierReward(rewardDistJSON, supplier)
	}

	sm.logger.Info().
		Str("supplier", supplier).
		Str("service_id", serviceID).
		Str("status", status).
		Int64("num_relays", numRelays).
		Int64("compute_units", computeUnits).
		Int64("upokt_earned", upoktEarned).
		Int64("session_end_height", sessionEndHeight).
		Int64("settlement_height", settlementHeight).
		Msg("claim settled on-chain")

	// Record metrics
	RecordClaimSettled(supplier, serviceID, status, numRelays, computeUnits, upoktEarned, sessionEndHeight, settlementHeight)
}

// extractSupplierReward parses the reward_distribution JSON and extracts the supplier's reward amount.
// reward_distribution format: {"address1":"10upokt","address2":"20upokt"}
func (sm *SettlementMonitor) extractSupplierReward(rewardDistJSON, supplier string) int64 {
	// Parse JSON
	var distribution map[string]string
	if err := json.Unmarshal([]byte(rewardDistJSON), &distribution); err != nil {
		sm.logger.Warn().
			Err(err).
			Str("reward_distribution", rewardDistJSON).
			Msg("failed to parse reward distribution JSON")
		return 0
	}

	// Find supplier's reward
	reward, ok := distribution[supplier]
	if !ok {
		return 0
	}

	// Parse "70upokt" -> 70
	return parseUpoktAmount(reward)
}

// Helper functions to extract values from event attributes

func extractStringValue(quoted string) string {
	// Remove quotes: "value" -> value
	if len(quoted) >= 2 && quoted[0] == '"' && quoted[len(quoted)-1] == '"' {
		return quoted[1 : len(quoted)-1]
	}
	return quoted
}

func extractIntValue(str string) int64 {
	// Remove quotes if present
	str = extractStringValue(str)

	val, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return 0
	}
	return val
}

func parseUpoktAmount(amount string) int64 {
	// Parse "70upokt" -> 70
	if len(amount) == 0 {
		return 0
	}

	// Remove "upokt" suffix
	if len(amount) > 5 && amount[len(amount)-5:] == "upokt" {
		amount = amount[:len(amount)-5]
	}

	val, err := strconv.ParseInt(amount, 10, 64)
	if err != nil {
		return 0
	}
	return val
}
