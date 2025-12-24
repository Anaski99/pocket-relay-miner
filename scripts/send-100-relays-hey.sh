#!/bin/bash
#
# Send 100 relays to PATH using hey
#

set -euo pipefail

# Configuration
PATH_URL="http://localhost:3069/v1"
SERVICE_ID="develop-http"
SUPPLIER="pokt19a3t4yunp0dlpfjrp7qwnzwlrzd5fzs2gjaaaj"
COUNT=100

# Relay request payload (simple JSON-RPC request)
PAYLOAD='{
  "jsonrpc": "2.0",
  "method": "eth_blockNumber",
  "params": [],
  "id": 1
}'

echo "Sending $COUNT relays to PATH..."
echo "URL: $PATH_URL"
echo "Service: $SERVICE_ID"
echo "Supplier: $SUPPLIER"
echo ""

hey -n $COUNT \
    -m POST \
    -H "Content-Type: application/json" \
    -H "Target-Service-Id: $SERVICE_ID" \
    -H "Target-Suppliers: $SUPPLIER" \
    -d "$PAYLOAD" \
    "$PATH_URL"

echo ""
echo "Done! Check current block:"
curl -s http://localhost:26657/status | jq -r '.result.sync_info.latest_block_height'
