package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/pokt-network/pocket-relay-miner/client/relay_client"
	"github.com/pokt-network/pocket-relay-miner/logging"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
)

// runWebSocketMode sends WebSocket relay requests to the relayer.
func runWebSocketMode(ctx context.Context, logger logging.Logger, relayClient *relay_client.RelayClient) error {
	// Build payload (eth_blockNumber by default)
	payloadBz, err := buildJSONRPCPayload()
	if err != nil {
		return fmt.Errorf("failed to build payload: %w", err)
	}

	// Diagnostic mode: single request with detailed output
	if !relayLoadTest {
		return runWebSocketDiagnostic(ctx, logger, relayClient, payloadBz)
	}

	// Load test mode: concurrent requests with metrics
	return runWebSocketLoadTest(ctx, logger, relayClient, payloadBz)
}

// runWebSocketDiagnostic sends a single WebSocket relay request with detailed output.
func runWebSocketDiagnostic(ctx context.Context, logger logging.Logger, relayClient *relay_client.RelayClient, payloadBz []byte) error {
	// Build and sign relay request
	buildStart := time.Now()
	relayRequest, relayRequestBz, err := relayClient.BuildRelayRequest(ctx, relayServiceID, relaySupplierAddr, payloadBz)
	if err != nil {
		return fmt.Errorf("failed to build relay request: %w", err)
	}
	buildDuration := time.Since(buildStart)

	logger.Info().
		Dur("build_time", buildDuration).
		Int("request_size", len(relayRequestBz)).
		Msg("relay request built and signed")

	// Connect to WebSocket
	connectStart := time.Now()
	conn, err := connectWebSocket(relayRelayerURL, relayServiceID, relaySupplierAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
	}
	defer conn.Close()
	connectDuration := time.Since(connectStart)

	// Send relay request
	sendStart := time.Now()
	if err := conn.WriteMessage(websocket.BinaryMessage, relayRequestBz); err != nil {
		return fmt.Errorf("failed to send relay request: %w", err)
	}
	sendDuration := time.Since(sendStart)

	// Receive relay response
	receiveStart := time.Now()
	messageType, responseData, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("failed to read relay response: %w", err)
	}
	receiveDuration := time.Since(receiveStart)

	if messageType != websocket.BinaryMessage {
		return fmt.Errorf("unexpected message type: %d (expected binary)", messageType)
	}

	// Verify supplier signature
	verifyStart := time.Now()
	relayResponse, err := relayClient.VerifyRelayResponse(ctx, relaySupplierAddr, responseData)
	if err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}
	verifyDuration := time.Since(verifyStart)

	// Display results
	fmt.Printf("\n=== WebSocket Relay Diagnostic ===\n")
	fmt.Printf("App Address: %s\n", relayClient.GetAppAddress())
	fmt.Printf("Service ID: %s\n", relayServiceID)
	fmt.Printf("Session ID: %s\n", relayRequest.Meta.SessionHeader.SessionId)
	fmt.Printf("Supplier: %s\n", relaySupplierAddr)
	fmt.Printf("\n=== Timings ===\n")
	fmt.Printf("Build Time: %v\n", buildDuration)
	fmt.Printf("Connect Time: %v\n", connectDuration)
	fmt.Printf("Send Time: %v\n", sendDuration)
	fmt.Printf("Receive Time: %v\n", receiveDuration)
	fmt.Printf("Verify Time: %v\n", verifyDuration)
	fmt.Printf("Total Time: %v\n", buildDuration+connectDuration+sendDuration+receiveDuration+verifyDuration)
	fmt.Printf("\n=== Response ===\n")
	fmt.Printf("Signature: ✅ VALID\n")
	fmt.Printf("Size: %d bytes\n", len(relayResponse.Payload))

	// Parse and display response payload
	if relayOutputJSON {
		fmt.Printf("Payload (raw): %s\n", string(relayResponse.Payload))
	} else {
		// Try to pretty-print JSON
		var payloadData interface{}
		if err := json.Unmarshal(relayResponse.Payload, &payloadData); err == nil {
			prettyJSON, _ := json.MarshalIndent(payloadData, "", "  ")
			fmt.Printf("Payload:\n%s\n", string(prettyJSON))
		} else {
			fmt.Printf("Payload: %s\n", string(relayResponse.Payload))
		}
	}

	// Send close message for graceful shutdown (avoids "abnormal closure" errors)
	closeMessage := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	conn.WriteMessage(websocket.CloseMessage, closeMessage)

	return nil
}

// runWebSocketLoadTest sends concurrent WebSocket relay requests with performance metrics.
// Uses a connection pool to avoid overhead of creating new connections for each request.
func runWebSocketLoadTest(ctx context.Context, logger logging.Logger, relayClient *relay_client.RelayClient, payloadBz []byte) error {
	// Build relay request once (reuse across requests)
	logger.Info().Msg("building relay request template for load test")
	_, relayRequestBz, err := relayClient.BuildRelayRequest(ctx, relayServiceID, relaySupplierAddr, payloadBz)
	if err != nil {
		return fmt.Errorf("failed to build relay request template: %w", err)
	}

	// Create connection pool (one connection per worker for efficiency)
	connPool := make([]*websocket.Conn, relayConcurrency)
	for i := 0; i < relayConcurrency; i++ {
		conn, err := connectWebSocket(relayRelayerURL, relayServiceID, relaySupplierAddr)
		if err != nil {
			// Close any connections we already opened
			for j := 0; j < i; j++ {
				connPool[j].Close()
			}
			return fmt.Errorf("failed to create connection pool: %w", err)
		}
		connPool[i] = conn
		// Set ping/pong handlers to keep connections alive
		conn.SetPongHandler(func(string) error { return nil })
	}
	defer func() {
		for _, conn := range connPool {
			conn.Close()
		}
	}()

	// Create metrics collector
	metrics := NewRelayMetrics()

	// Worker pool pattern with semaphore
	semaphore := make(chan struct{}, relayConcurrency)
	var wg sync.WaitGroup

	logger.Info().
		Int("count", relayCount).
		Int("concurrency", relayConcurrency).
		Int("connection_pool_size", len(connPool)).
		Msg("starting WebSocket load test with connection pool")

	metrics.Start()

	// Track which worker gets which connection (round-robin)
	var connIdx atomic.Int32

	// Spawn workers
	for i := 0; i < relayCount; i++ {
		wg.Add(1)
		semaphore <- struct{}{} // Acquire slot

		go func(reqNum int) {
			defer wg.Done()
			defer func() { <-semaphore }() // Release slot

			// Get a connection from the pool (round-robin)
			idx := int(connIdx.Add(1)-1) % len(connPool)
			conn := connPool[idx]

			// Send relay with timeout
			requestCtx, cancel := context.WithTimeout(ctx, time.Duration(relayTimeout)*time.Second)
			defer cancel()

			start := time.Now()
			_, err := sendWebSocketRelayOnConnection(requestCtx, conn, relayRequestBz)
			latencyMs := float64(time.Since(start).Microseconds()) / 1000.0

			if err != nil {
				metrics.RecordError(err)
				logger.Debug().
					Err(err).
					Int("request_num", reqNum).
					Int("conn_idx", idx).
					Msg("WebSocket relay request failed")
			} else {
				metrics.RecordSuccess(latencyMs)
				logger.Debug().
					Int("request_num", reqNum).
					Int("conn_idx", idx).
					Float64("latency_ms", latencyMs).
					Msg("WebSocket relay request succeeded")
			}
		}(i)
	}

	// Wait for all workers to finish
	wg.Wait()
	metrics.End()

	// Display results
	fmt.Println(metrics.GetSummary())

	return nil
}

// connectWebSocket establishes a WebSocket connection to the relayer.
func connectWebSocket(relayerURL, serviceID, supplierAddr string) (*websocket.Conn, error) {
	// Parse URL and convert to WebSocket scheme
	parsedURL, err := url.Parse(relayerURL)
	if err != nil {
		return nil, fmt.Errorf("invalid relayer URL: %w", err)
	}

	// Convert http:// to ws:// and https:// to wss://
	switch parsedURL.Scheme {
	case "http":
		parsedURL.Scheme = "ws"
	case "https":
		parsedURL.Scheme = "wss"
	case "ws", "wss":
		// Already WebSocket scheme
	default:
		return nil, fmt.Errorf("unsupported URL scheme: %s", parsedURL.Scheme)
	}

	// Create request headers with service and supplier metadata
	headers := http.Header{}
	headers.Set("Pocket-Service-Id", serviceID)
	if supplierAddr != "" {
		headers.Set("Pocket-Supplier-Address", supplierAddr)
	}
	// Set Rpc-Type header to WEBSOCKET (2) for proper backend selection
	// Reference: poktroll/x/shared/types/service.pb.go RPCType enum
	headers.Set("Rpc-Type", "2") // WEBSOCKET = 2

	// Dial WebSocket connection
	conn, _, err := websocket.DefaultDialer.Dial(parsedURL.String(), headers)
	if err != nil {
		return nil, fmt.Errorf("WebSocket dial failed: %w", err)
	}

	return conn, nil
}

// sendWebSocketRelay sends a relay request via WebSocket and returns the response.
func sendWebSocketRelay(ctx context.Context, relayRequestBz []byte) (*servicetypes.RelayResponse, error) {
	// Connect to WebSocket
	conn, err := connectWebSocket(relayRelayerURL, relayServiceID, relaySupplierAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer conn.Close()

	// Set deadline based on context
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetWriteDeadline(deadline)
		conn.SetReadDeadline(deadline)
	}

	// Send relay request
	if err := conn.WriteMessage(websocket.BinaryMessage, relayRequestBz); err != nil {
		return nil, fmt.Errorf("failed to send: %w", err)
	}

	// Receive relay response
	messageType, responseData, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("failed to receive: %w", err)
	}

	if messageType != websocket.BinaryMessage {
		return nil, fmt.Errorf("unexpected message type: %d", messageType)
	}

	// Unmarshal response
	var relayResponse servicetypes.RelayResponse
	if err := relayResponse.Unmarshal(responseData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Send close message for graceful shutdown
	closeMessage := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	conn.WriteMessage(websocket.CloseMessage, closeMessage)

	return &relayResponse, nil
}

// sendWebSocketRelayOnConnection sends a relay request via an existing WebSocket connection.
// This is used by the load test to reuse connections from the connection pool.
func sendWebSocketRelayOnConnection(ctx context.Context, conn *websocket.Conn, relayRequestBz []byte) (*servicetypes.RelayResponse, error) {
	// Set deadline based on context
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetWriteDeadline(deadline)
		conn.SetReadDeadline(deadline)
	}

	// Send relay request
	if err := conn.WriteMessage(websocket.BinaryMessage, relayRequestBz); err != nil {
		return nil, fmt.Errorf("failed to send: %w", err)
	}

	// Receive relay response
	messageType, responseData, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("failed to receive: %w", err)
	}

	if messageType != websocket.BinaryMessage {
		return nil, fmt.Errorf("unexpected message type: %d", messageType)
	}

	// Unmarshal response
	var relayResponse servicetypes.RelayResponse
	if err := relayResponse.Unmarshal(responseData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &relayResponse, nil
}
