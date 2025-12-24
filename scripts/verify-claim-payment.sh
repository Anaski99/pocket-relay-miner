#!/usr/bin/env bash
#
# Verification script to test SMST fix and claim payment
# Steps:
# 1. Send 3k relays to develop-http service
# 2. Monitor miner logs for claim submission and extract TX hash
# 3. Query block_results to find EventClaimSettled and verify payment
#

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib/common.sh"

# Configuration
RELAY_COUNT=500
SERVICE_ID="develop-http"
GATEWAY_URL="http://localhost:3069/v1"
TARGET_SUPPLIER="pokt19a3t4yunp0dlpfjrp7qwnzwlrzd5fzs2gjaaaj"  # Target specific supplier
MINER_POD=""
TIMEOUT=300  # 5 minutes max

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Find miner pod
find_miner_pod() {
    MINER_POD=$(kubectl get pods -l app=miner -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
    if [ -z "$MINER_POD" ]; then
        log_error "Miner pod not found"
        exit 1
    fi
    log_info "Found miner pod: $MINER_POD"
}

# Step 1: Send relays using hey
send_relays() {
    log_info "Step 1: Sending $RELAY_COUNT relays to $SERVICE_ID..."

    if ! command -v hey &> /dev/null; then
        log_error "hey not found. Install with: go install github.com/rakyll/hey@latest"
        exit 1
    fi

    # Build a sample relay request (you can customize this)
    local payload='{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'

    log_info "Sending relays with hey to supplier: $TARGET_SUPPLIER"
    hey -n "$RELAY_COUNT" -c 50 \
        -m POST \
        -H "Content-Type: application/json" \
        -H "Target-Service-Id: $SERVICE_ID" \
        -H "Target-Suppliers: $TARGET_SUPPLIER" \
        -d "$payload" \
        "$GATEWAY_URL" > /tmp/hey_output.txt 2>&1

    local exit_code=$?
    if [ $exit_code -ne 0 ]; then
        log_warn "hey exited with code $exit_code, but continuing..."
    fi

    log_info "Relays sent. Summary:"
    grep -E "Status code|requests/sec|Total:" /tmp/hey_output.txt || true
}

# Step 2: Monitor miner logs for claim submission
wait_for_claim() {
    log_info "Step 2: Waiting for claim submission (timeout: ${TIMEOUT}s)..."

    local start_time=$(date +%s)
    local claim_tx_hash=""

    while [ -z "$claim_tx_hash" ]; do
        local current_time=$(date +%s)
        local elapsed=$((current_time - start_time))

        if [ $elapsed -gt $TIMEOUT ]; then
            log_error "Timeout waiting for claim submission after ${TIMEOUT}s"
            return 1
        fi

        # Check logs for "claims submitted" with tx_hash for our target supplier
        claim_tx_hash=$(kubectl logs "$MINER_POD" --tail=100 2>/dev/null | \
            grep -i "claims submitted" | \
            grep "$TARGET_SUPPLIER" | \
            grep -oP 'tx_hash=\K[A-F0-9]{64}' | \
            tail -1 || true)

        if [ -z "$claim_tx_hash" ]; then
            echo -n "."
            sleep 2
        fi
    done

    echo ""
    log_info "Found claim transaction: $claim_tx_hash"
    echo "$claim_tx_hash"
}

# Step 3: Wait for settlement and verify payment
verify_payment() {
    local claim_tx_hash="$1"
    log_info "Step 3: Waiting for claim settlement and verifying payment..."

    # Get the block height where claim was included
    log_info "Querying claim transaction details..."
    local claim_block=$(curl -s "http://localhost:1317/cosmos/tx/v1beta1/txs/$claim_tx_hash" | \
        jq -r '.tx_response.height // empty')

    if [ -z "$claim_block" ]; then
        log_error "Failed to get claim block height"
        return 1
    fi

    log_info "Claim included in block $claim_block"

    # Parse session end height from claim
    local session_end=$(curl -s "http://localhost:1317/cosmos/tx/v1beta1/txs/$claim_tx_hash" | \
        jq -r '.tx.body.messages[0].session_header.session_end_block_height // empty')

    if [ -z "$session_end" ]; then
        log_error "Failed to parse session_end_block_height from claim"
        return 1
    fi

    log_info "Session end height: $session_end"

    # Calculate expected settlement block (session_end + proof_window + grace_period)
    # For localnet this is typically ~13 blocks after session end
    local min_settlement_block=$((session_end + 10))
    local max_settlement_block=$((session_end + 20))

    log_info "Expected settlement between blocks $min_settlement_block and $max_settlement_block"
    log_info "Waiting for settlement event..."

    local start_time=$(date +%s)
    local settlement_found=false

    for block in $(seq $min_settlement_block $max_settlement_block); do
        # Wait for block to be produced
        local current_height=$(curl -s http://localhost:26657/status | jq -r '.result.sync_info.latest_block_height')
        while [ "$current_height" -lt "$block" ]; do
            local elapsed=$(($(date +%s) - start_time))
            if [ $elapsed -gt $TIMEOUT ]; then
                log_error "Timeout waiting for block $block"
                return 1
            fi
            sleep 1
            current_height=$(curl -s http://localhost:26657/status | jq -r '.result.sync_info.latest_block_height')
        done

        # Check block_results for EventClaimSettled
        local settlement=$(curl -s "http://localhost:26657/block_results?height=$block" | \
            jq -r '.result.finalize_block_events[] | select(.type == "pocket.tokenomics.EventClaimSettled")')

        if [ -n "$settlement" ]; then
            settlement_found=true
            log_info "✓ Found EventClaimSettled in block $block"

            # Extract payment details
            local num_relays=$(echo "$settlement" | jq -r '.attributes[] | select(.key == "num_relays") | .value' | tr -d '"')
            local claimed_compute_units=$(echo "$settlement" | jq -r '.attributes[] | select(.key == "num_claimed_compute_units") | .value' | tr -d '"')
            local claimed_upokt=$(echo "$settlement" | jq -r '.attributes[] | select(.key == "claimed_upokt") | .value' | tr -d '"')
            local reward_distribution=$(echo "$settlement" | jq -r '.attributes[] | select(.key == "reward_distribution") | .value')

            echo ""
            echo "========================================="
            echo "CLAIM SETTLEMENT DETAILS"
            echo "========================================="
            echo "Block Height:       $block"
            echo "Num Relays:         $num_relays"
            echo "Compute Units:      $claimed_compute_units"
            echo "Claimed Amount:     $claimed_upokt"
            echo "Rewards:            $reward_distribution"
            echo "========================================="
            echo ""

            # Verify we got paid
            local expected_compute_units=$((RELAY_COUNT * 100))

            if [ "$num_relays" = "1" ] || [ "$claimed_compute_units" = "100" ]; then
                log_error "❌ BUG DETECTED: Only 1 relay in SMST tree (expected $RELAY_COUNT)!"
                log_error "This indicates the SMST bug is still present"
                return 1
            elif [ "$num_relays" = "$RELAY_COUNT" ] && [ "$claimed_compute_units" = "$expected_compute_units" ]; then
                log_info "✓ SUCCESS: All $RELAY_COUNT relays were included in the claim!"
                log_info "✓ Compute Units: $claimed_compute_units (expected: $expected_compute_units)"
                if [ "$claimed_upokt" != "0upokt" ]; then
                    log_info "✓ PAYMENT RECEIVED: $claimed_upokt"
                else
                    log_warn "⚠ No payment (likely below threshold or probabilistic proof not required)"
                fi
                return 0
            else
                log_error "❌ MISMATCH: relays=$num_relays (expected: $RELAY_COUNT), compute_units=$claimed_compute_units (expected: $expected_compute_units)"
                log_error "Some relays were lost during SMST tree building!"
                return 1
            fi
        fi
    done

    if [ "$settlement_found" = false ]; then
        log_error "EventClaimSettled not found in blocks $min_settlement_block-$max_settlement_block"
        return 1
    fi
}

# Main execution
main() {
    print_header "SMST Fix Verification"

    find_miner_pod

    # Run the test
    send_relays

    local claim_tx_hash
    claim_tx_hash=$(wait_for_claim)

    if [ -z "$claim_tx_hash" ]; then
        log_error "Failed to capture claim transaction hash"
        exit 1
    fi

    if verify_payment "$claim_tx_hash"; then
        echo ""
        log_info "========================================="
        log_info "✓✓✓ TEST PASSED ✓✓✓"
        log_info "========================================="
        exit 0
    else
        echo ""
        log_error "========================================="
        log_error "✗✗✗ TEST FAILED ✗✗✗"
        log_error "========================================="
        exit 1
    fi
}

main "$@"
