//go:build test

package relayer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alitto/pond/v2"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/pokt-network/pocket-relay-miner/testutil"
	"github.com/pokt-network/pocket-relay-miner/transport"
	sdktypes "github.com/pokt-network/shannon-sdk/types"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
)

// mockRelayProcessor is a mock implementation of RelayProcessor for WebSocket tests.
type mockRelayProcessor struct {
	processErr error
}

func (m *mockRelayProcessor) ProcessRelay(
	ctx context.Context,
	reqBody, respBody []byte,
	supplierAddr string,
	serviceID string,
	arrivalBlockHeight int64,
) (*transport.MinedRelayMessage, error) {
	if m.processErr != nil {
		return nil, m.processErr
	}
	// Return a basic relay message for testing
	return &transport.MinedRelayMessage{
		RelayBytes:              reqBody,
		SupplierOperatorAddress: supplierAddr,
		ServiceId:               serviceID,
		ArrivalBlockHeight:      arrivalBlockHeight,
	}, nil
}

func (m *mockRelayProcessor) GetServiceDifficulty(ctx context.Context, serviceID string) ([]byte, error) {
	return []byte{0xFF, 0xFF, 0xFF, 0xFF}, nil
}

func (m *mockRelayProcessor) SetDifficultyProvider(provider DifficultyProvider) {
	// No-op for mock
}


// wsTestBackend represents a mock WebSocket backend server for testing.
type wsTestBackend struct {
	*httptest.Server
	upgrader websocket.Upgrader
	handler  func(*websocket.Conn)
	connChan chan *websocket.Conn // Channel to access backend connections
}

// newWSTestBackend creates a new WebSocket test backend.
func newWSTestBackend(handler func(*websocket.Conn)) *wsTestBackend {
	backend := &wsTestBackend{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		handler:  handler,
		connChan: make(chan *websocket.Conn, 1),
	}

	backend.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := backend.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Send connection to test via channel
		select {
		case backend.connChan <- conn:
		default:
		}
		if backend.handler != nil {
			backend.handler(conn)
		}
	}))

	return backend
}

// TestWebSocketUpgrade_Success characterizes successful WebSocket upgrade.
func TestWebSocketUpgrade_Success(t *testing.T) {
	t.Parallel()

	// Create backend that echoes messages
	backend := newWSTestBackend(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	})
	defer backend.Close()

	// Create proxy with WebSocket support
	proxy := setupTestProxy(t, backend.URL)
	defer proxy.Close()

	// Create test server
	ts := httptest.NewServer(proxy.WebSocketHandler())
	defer ts.Close()

	// Convert http:// URL to ws://
	wsURL := strings.Replace(ts.URL, "http", "ws", 1)

	// Dial WebSocket connection with service ID header
	headers := http.Header{}
	headers.Set("Target-Service-Id", testutil.TestServiceID)
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
	require.NoError(t, err)
	require.NotNil(t, conn)
	defer conn.Close()

	// Verify upgrade response
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	require.Equal(t, "websocket", resp.Header.Get("Upgrade"))
	require.Equal(t, "Upgrade", resp.Header.Get("Connection"))
}

// TestWebSocketUpgrade_MissingHeaders characterizes upgrade failure when WebSocket headers are missing.
func TestWebSocketUpgrade_MissingHeaders(t *testing.T) {
	t.Parallel()

	backend := newWSTestBackend(nil)
	defer backend.Close()

	proxy := setupTestProxy(t, backend.URL)
	defer proxy.Close()

	ts := httptest.NewServer(proxy.WebSocketHandler())
	defer ts.Close()

	// Make regular HTTP request without WebSocket upgrade headers
	resp, err := http.Get(ts.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Expect error (not 101 Switching Protocols)
	// Gorilla returns 400 Bad Request for invalid upgrade
	require.NotEqual(t, http.StatusSwitchingProtocols, resp.StatusCode)
}

// TestWebSocketUpgrade_BackendUnavailable characterizes behavior when backend is down.
func TestWebSocketUpgrade_BackendUnavailable(t *testing.T) {
	t.Parallel()

	// Create backend but don't start server (invalid URL)
	invalidBackendURL := "ws://localhost:9999/nonexistent"

	proxy := setupTestProxy(t, invalidBackendURL)
	defer proxy.Close()

	ts := httptest.NewServer(proxy.WebSocketHandler())
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http", "ws", 1)

	// Try to connect - should fail when proxy can't reach backend
	headers := http.Header{}
	headers.Set("Target-Service-Id", testutil.TestServiceID)
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if conn != nil {
		defer conn.Close()
	}

	// Either dial fails immediately or connection is established but then closed
	// This characterizes the actual behavior
	if err == nil {
		// Connection established but should close due to backend error
		require.NotNil(t, resp)
		// Wait for close message
		_, _, readErr := conn.ReadMessage()
		require.Error(t, readErr)
	} else {
		// Dial failed - this is also acceptable behavior
		require.Error(t, err)
	}
}

// TestWebSocketMessage_TextRelay characterizes text message relay through the bridge.
func TestWebSocketMessage_TextRelay(t *testing.T) {
	t.Parallel()

	receivedMsgChan := make(chan []byte, 1)

	// Backend that captures received message and echoes back
	backend := newWSTestBackend(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			receivedMsgChan <- msg
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	})
	defer backend.Close()

	proxy := setupTestProxy(t, backend.URL)
	defer proxy.Close()

	ts := httptest.NewServer(proxy.WebSocketHandler())
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http", "ws", 1)
	headers := http.Header{}
	headers.Set("Target-Service-Id", testutil.TestServiceID)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	require.NoError(t, err)
	defer conn.Close()

	// Send text message
	testMsg := []byte("test message")
	err = conn.WriteMessage(websocket.TextMessage, testMsg)
	require.NoError(t, err)

	// Verify backend received exact bytes
	select {
	case received := <-receivedMsgChan:
		require.Equal(t, testMsg, received)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for backend to receive message")
	}

	// Read echoed response
	mt, resp, err := conn.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.BinaryMessage, mt) // Proxy sends responses as binary (protobuf)
	require.NotEmpty(t, resp)
}

// TestWebSocketMessage_BinaryRelay characterizes binary message relay.
func TestWebSocketMessage_BinaryRelay(t *testing.T) {
	t.Parallel()

	receivedMsgChan := make(chan []byte, 1)

	backend := newWSTestBackend(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			receivedMsgChan <- msg
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	})
	defer backend.Close()

	proxy := setupTestProxy(t, backend.URL)
	defer proxy.Close()

	ts := httptest.NewServer(proxy.WebSocketHandler())
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http", "ws", 1)
	headers := http.Header{}
	headers.Set("Target-Service-Id", testutil.TestServiceID)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	require.NoError(t, err)
	defer conn.Close()

	// Send binary message (protobuf relay request)
	relayReq := createTestRelayRequest(t)
	err = conn.WriteMessage(websocket.BinaryMessage, relayReq)
	require.NoError(t, err)

	// Verify backend received message (payload extracted from RelayRequest)
	select {
	case received := <-receivedMsgChan:
		require.NotEmpty(t, received)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for backend to receive message")
	}

	// Read response
	mt, resp, err := conn.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.BinaryMessage, mt)
	require.NotEmpty(t, resp)
}

// TestWebSocketConnection_ClientClose characterizes client-initiated connection close.
func TestWebSocketConnection_ClientClose(t *testing.T) {
	t.Parallel()

	backendCloseChan := make(chan bool, 1)

	backend := newWSTestBackend(func(conn *websocket.Conn) {
		defer conn.Close()
		defer func() { backendCloseChan <- true }()
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	})
	defer backend.Close()

	proxy := setupTestProxy(t, backend.URL)
	defer proxy.Close()

	ts := httptest.NewServer(proxy.WebSocketHandler())
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http", "ws", 1)
	headers := http.Header{}
	headers.Set("Target-Service-Id", testutil.TestServiceID)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	require.NoError(t, err)

	// Send close frame
	err = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "test close"))
	require.NoError(t, err)

	// Verify backend handler exits (connection closed)
	select {
	case <-backendCloseChan:
		// Expected: backend connection closed
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for backend connection to close")
	}

	conn.Close()
}

// TestWebSocketConnection_BackendClose characterizes backend-initiated connection close.
func TestWebSocketConnection_BackendClose(t *testing.T) {
	t.Parallel()

	backendConnChan := make(chan *websocket.Conn, 1)

	backend := newWSTestBackend(func(conn *websocket.Conn) {
		backendConnChan <- conn
		// Don't handle messages - test will close from backend side
		select {}
	})
	defer backend.Close()

	proxy := setupTestProxy(t, backend.URL)
	defer proxy.Close()

	ts := httptest.NewServer(proxy.WebSocketHandler())
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http", "ws", 1)
	headers := http.Header{}
	headers.Set("Target-Service-Id", testutil.TestServiceID)
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	require.NoError(t, err)
	defer clientConn.Close()

	// Wait for backend connection
	var backendConn *websocket.Conn
	select {
	case backendConn = <-backendConnChan:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for backend connection")
	}

	// Close backend connection
	err = backendConn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "backend close"))
	require.NoError(t, err)
	backendConn.Close()

	// Client should receive close message
	_, _, err = clientConn.ReadMessage()
	require.Error(t, err)

	// Verify it's a close error
	closeErr, ok := err.(*websocket.CloseError)
	require.True(t, ok, "expected websocket.CloseError")
	require.NotZero(t, closeErr.Code)
}

// TestWebSocketConnection_PingPong characterizes ping/pong keepalive handling.
func TestWebSocketConnection_PingPong(t *testing.T) {
	t.Parallel()

	pongReceivedChan := make(chan bool, 10)

	backend := newWSTestBackend(func(conn *websocket.Conn) {
		defer conn.Close()
		// Set pong handler
		conn.SetPongHandler(func(appData string) error {
			pongReceivedChan <- true
			return nil
		})
		// Keep connection alive
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	})
	defer backend.Close()

	proxy := setupTestProxy(t, backend.URL)
	defer proxy.Close()

	ts := httptest.NewServer(proxy.WebSocketHandler())
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http", "ws", 1)
	headers := http.Header{}
	headers.Set("Target-Service-Id", testutil.TestServiceID)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	require.NoError(t, err)
	defer conn.Close()

	// Send ping message
	err = conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(time.Second))
	require.NoError(t, err)

	// Note: Gorilla WebSocket automatically responds to pings with pongs.
	// The bridge's ping loops are for keepalive, not request/response.
	// This test characterizes that connections support ping/pong control frames.

	// Wait a bit to ensure no errors
	time.Sleep(100 * time.Millisecond)

	// Connection should still be alive
	err = conn.WriteMessage(websocket.TextMessage, []byte("still alive"))
	require.NoError(t, err)
}

// TestWebSocketConcurrent_MultipleConnections characterizes multiple simultaneous connections.
func TestWebSocketConcurrent_MultipleConnections(t *testing.T) {
	t.Parallel()

	var backendMsgCount atomic.Int32

	backend := newWSTestBackend(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			backendMsgCount.Add(1)
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	})
	defer backend.Close()

	proxy := setupTestProxy(t, backend.URL)
	defer proxy.Close()

	ts := httptest.NewServer(proxy.WebSocketHandler())
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http", "ws", 1)

	// Open multiple concurrent connections
	numConns := 10
	var wg sync.WaitGroup
	wg.Add(numConns)

	for i := 0; i < numConns; i++ {
		go func(connID int) {
			defer wg.Done()

			headers := http.Header{}
	headers.Set("Target-Service-Id", testutil.TestServiceID)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
			require.NoError(t, err)
			defer conn.Close()

			// Send unique message
			testMsg := []byte("message from connection " + string(rune(connID+'0')))
			err = conn.WriteMessage(websocket.TextMessage, testMsg)
			require.NoError(t, err)

			// Read response
			_, _, err = conn.ReadMessage()
			require.NoError(t, err)
		}(i)
	}

	wg.Wait()

	// Verify all messages were received by backend
	require.Equal(t, int32(numConns), backendMsgCount.Load())
}

// TestWebSocketConcurrent_MessageBurst characterizes many messages on a single connection.
func TestWebSocketConcurrent_MessageBurst(t *testing.T) {
	t.Parallel()

	var backendMsgCount atomic.Int32
	responseReady := make(chan struct{})

	backend := newWSTestBackend(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			backendMsgCount.Add(1)
			// Signal that we're ready to respond
			select {
			case <-responseReady:
			default:
				close(responseReady)
			}
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	})
	defer backend.Close()

	proxy := setupTestProxy(t, backend.URL)
	defer proxy.Close()

	ts := httptest.NewServer(proxy.WebSocketHandler())
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http", "ws", 1)
	headers := http.Header{}
	headers.Set("Target-Service-Id", testutil.TestServiceID)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	require.NoError(t, err)
	defer conn.Close()

	// Send burst of messages
	numMessages := 50
	var sendWg sync.WaitGroup
	sendWg.Add(1)

	go func() {
		defer sendWg.Done()
		for i := 0; i < numMessages; i++ {
			testMsg := []byte("burst message")
			err := conn.WriteMessage(websocket.TextMessage, testMsg)
			require.NoError(t, err)
		}
	}()

	// Wait for backend to start processing
	<-responseReady

	// Read responses
	for i := 0; i < numMessages; i++ {
		_, _, err := conn.ReadMessage()
		require.NoError(t, err)
	}

	sendWg.Wait()

	// Verify all messages received by backend
	require.Equal(t, int32(numMessages), backendMsgCount.Load())
}

// TestWebSocketBridge_RelayEmission characterizes relay emission for WebSocket messages.
func TestWebSocketBridge_RelayEmission(t *testing.T) {
	t.Parallel()

	backend := newWSTestBackend(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			// Echo back
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	})
	defer backend.Close()

	proxy := setupTestProxyWithPublisher(t, backend.URL)
	defer proxy.Close()

	publisher := proxy.publisher.(*mockMinedRelayPublisher)

	ts := httptest.NewServer(proxy.WebSocketHandler())
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http", "ws", 1)
	headers := http.Header{}
	headers.Set("Target-Service-Id", testutil.TestServiceID)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	require.NoError(t, err)
	defer conn.Close()

	// Send relay request
	relayReq := createTestRelayRequest(t)
	err = conn.WriteMessage(websocket.BinaryMessage, relayReq)
	require.NoError(t, err)

	// Read response
	_, _, err = conn.ReadMessage()
	require.NoError(t, err)

	// Wait for relay to be published
	require.Eventually(t, func() bool {
		publisher.mu.Lock()
		defer publisher.mu.Unlock()
		return len(publisher.published) > 0
	}, 2*time.Second, 50*time.Millisecond)

	// Verify relay was emitted
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	require.Greater(t, len(publisher.published), 0)
}

// Helper functions

// setupTestProxy creates a minimal proxy for WebSocket testing.
func setupTestProxy(t *testing.T, backendURL string) *ProxyServer {
	logger := zerolog.New(io.Discard).With().Logger()

	// Convert http:// test server URL to ws://
	wsURL := strings.Replace(backendURL, "http", "ws", 1)

	config := &Config{
		ListenAddr:                   "0.0.0.0:8080",
		DefaultValidationMode:        ValidationModeOptimistic,
		DefaultRequestTimeoutSeconds: 30,
		Services: map[string]ServiceConfig{
			testutil.TestServiceID: {
				Backends: map[string]BackendConfig{
					"websocket": {
						URL: wsURL,
					},
				},
			},
		},
		HTTPTransport: HTTPTransportConfig{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			MaxConnsPerHost:     100,
		},
	}

	// Create minimal worker pool for tests
	workerPool := pond.NewPool(10)

	healthChecker := NewHealthChecker(logger)
	proxy, err := NewProxyServer(logger, config, healthChecker, nil, workerPool)
	require.NoError(t, err)

	// Setup minimal dependencies
	validator := &mockRelayValidator{}
	publisher := &mockMinedRelayPublisher{}

	responseSigner := &ResponseSigner{
		logger:            logger,
		signers:           make(map[string]Signer),
		operatorAddresses: []string{testutil.TestSupplierAddress()},
	}
	responseSigner.signers[testutil.TestSupplierAddress()] = &mockSigner{}

	proxy.SetValidator(validator)
	proxy.SetResponseSigner(responseSigner)
	proxy.publisher = publisher
	proxy.SetBlockHeight(100)

	// Create relay pipeline for validation
	relayPipeline := &RelayPipeline{
		logger:    logger,
		validator: validator,
	}
	proxy.relayPipeline = relayPipeline

	// Create mock relay processor
	relayProcessor := &mockRelayProcessor{}
	proxy.relayProcessor = relayProcessor

	return proxy
}

// setupTestProxyWithPublisher creates a proxy with a real publisher for testing relay emission.
func setupTestProxyWithPublisher(t *testing.T, backendURL string) *ProxyServer {
	proxy := setupTestProxy(t, backendURL)
	// Publisher is already set up in setupTestProxy
	return proxy
}

// createTestRelayRequest creates a valid RelayRequest for testing.
func createTestRelayRequest(t *testing.T) []byte {
	poktReq := &sdktypes.POKTHTTPRequest{
		Method: "POST",
		Url:    "/",
		Header: make(map[string]*sdktypes.Header),
		BodyBz: []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`),
	}
	poktReq.Header["Content-Type"] = &sdktypes.Header{
		Key:    "Content-Type",
		Values: []string{"application/json"},
	}

	payloadBz, err := proto.Marshal(poktReq)
	require.NoError(t, err)

	relayReq := &servicetypes.RelayRequest{
		Meta: servicetypes.RelayRequestMetadata{
			SessionHeader: &sessiontypes.SessionHeader{
				ApplicationAddress:      testutil.TestAppAddress(),
				ServiceId:               testutil.TestServiceID,
				SessionId:               "test-session-ws",
				SessionStartBlockHeight: 90,
				SessionEndBlockHeight:   100,
			},
			SupplierOperatorAddress: testutil.TestSupplierAddress(),
			Signature:               []byte("test-signature"),
		},
		Payload: payloadBz,
	}

	relayReqBytes, err := relayReq.Marshal()
	require.NoError(t, err)
	return relayReqBytes
}
