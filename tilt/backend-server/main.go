package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"gopkg.in/yaml.v3"

	"github.com/pokt-network/pocket-relay-miner/tilt/backend-server/pb"
)

// Config holds backend server configuration.
type Config struct {
	HTTPPort    int           `yaml:"http_port"`
	GRPCPort    int           `yaml:"grpc_port"`
	MetricsPort int           `yaml:"metrics_port"`
	ErrorRate   float64       `yaml:"error_rate"`   // 0.0-1.0
	ErrorCode   int           `yaml:"error_code"`   // HTTP error code to inject
	DelayMs     int           `yaml:"delay_ms"`     // Delay in milliseconds
}

var (
	// Prometheus metrics
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "demo_requests_total",
			Help: "Total number of requests by protocol and method",
		},
		[]string{"protocol", "method"},
	)

	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "demo_request_duration_seconds",
			Help:    "Request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"protocol"},
	)
)

func init() {
	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(requestDuration)
}

func main() {
	// Load config
	cfg := loadConfig()

	// Start metrics server
	go startMetricsServer(cfg.MetricsPort)

	// Start gRPC server
	go startGRPCServer(cfg)

	// Start HTTP/WebSocket/SSE server
	startHTTPServer(cfg)
}

func loadConfig() *Config {
	cfg := &Config{
		HTTPPort:    8545,
		GRPCPort:    50051,
		MetricsPort: 9095,
		ErrorRate:   0.0,
		ErrorCode:   500,
		DelayMs:     0,
	}

	if data, err := os.ReadFile("config.yaml"); err == nil {
		yaml.Unmarshal(data, cfg)
	}

	return cfg
}

func startMetricsServer(port int) {
	http.Handle("/metrics", promhttp.Handler())
	addr := fmt.Sprintf(":%d", port)
	log.Printf("Metrics server listening on %s", addr)
	http.ListenAndServe(addr, nil)
}

func startHTTPServer(cfg *Config) {
	mux := http.NewServeMux()

	// HTTP JSON-RPC endpoint
	mux.HandleFunc("/", handleJSONRPC(cfg))

	// WebSocket endpoint
	mux.HandleFunc("/ws", handleWebSocket(cfg))

	// SSE streaming endpoint
	mux.HandleFunc("/stream/sse", handleSSE(cfg))

	// NDJSON streaming endpoint
	mux.HandleFunc("/stream/ndjson", handleNDJSON(cfg))

	// Health check
	mux.HandleFunc("/health", handleHealth())

	addr := fmt.Sprintf(":%d", cfg.HTTPPort)
	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	log.Printf("HTTP server listening on %s", addr)

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down HTTP server...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

func handleJSONRPC(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			requestDuration.WithLabelValues("http").Observe(time.Since(start).Seconds())
		}()

		// Apply delay if configured
		if cfg.DelayMs > 0 {
			time.Sleep(time.Duration(cfg.DelayMs) * time.Millisecond)
		}

		// Inject error if configured
		if shouldInjectError(cfg.ErrorRate) {
			requestsTotal.WithLabelValues("http", "error").Inc()
			http.Error(w, fmt.Sprintf("injected error (code: %d)", cfg.ErrorCode), cfg.ErrorCode)
			return
		}

		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		method, _ := req["method"].(string)
		id, _ := req["id"]

		requestsTotal.WithLabelValues("http", method).Inc()

		// Static responses based on method
		var result interface{}
		switch method {
		case "eth_blockNumber":
			result = "0x1"
		case "eth_getBlockByNumber":
			result = getStaticBlock()
		case "eth_getBalance":
			result = "0x1000000000000000000" // 1 ETH
		case "eth_call":
			result = "0x" // Empty bytes
		default:
			result = "0x0"
		}

		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  result,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func handleWebSocket(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WebSocket upgrade error: %v", err)
			return
		}
		defer conn.Close()

		requestsTotal.WithLabelValues("websocket", "connection").Inc()

		// Echo server: read message and send it back N times
		for {
			messageType, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}

			// Echo message back 1 time (can be made configurable)
			// For JSON messages, parse and respond
			if messageType == websocket.TextMessage {
				var jsonMsg map[string]interface{}
				if err := json.Unmarshal(msg, &jsonMsg); err == nil {
					method, _ := jsonMsg["method"].(string)
					id, _ := jsonMsg["id"]

					requestsTotal.WithLabelValues("websocket", method).Inc()

					// Handle subscription specially
					if method == "eth_subscribe" {
						resp := map[string]interface{}{
							"jsonrpc": "2.0",
							"id":      id,
							"result":  "0xabc123",
						}
						conn.WriteJSON(resp)
						continue
					}

					// Regular JSON-RPC: respond like HTTP endpoint
					var result interface{}
					switch method {
					case "eth_blockNumber":
						result = "0x1"
					case "eth_getBlockByNumber":
						result = getStaticBlock()
					case "eth_getBalance":
						result = "0x1000000000000000000"
					case "eth_call":
						result = "0x"
					default:
						result = "0x0"
					}

					resp := map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      id,
						"result":  result,
					}
					conn.WriteJSON(resp)
				}
			} else {
				// Binary message: echo it back as-is
				requestsTotal.WithLabelValues("websocket", "binary").Inc()
				if err := conn.WriteMessage(messageType, msg); err != nil {
					break
				}
			}
		}
	}
}

func handleSSE(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestsTotal.WithLabelValues("sse", "stream").Inc()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			event := map[string]interface{}{
				"type":   "block",
				"number": 1,
				"hash":   "0x1234567890abcdef",
			}
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func handleNDJSON(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestsTotal.WithLabelValues("ndjson", "stream").Inc()

		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-cache")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			event := map[string]interface{}{
				"type":   "block",
				"number": 1,
				"hash":   "0x1234567890abcdef",
			}
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "%s\n", data)
			flusher.Flush()
		}
	}
}

func handleHealth() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"status":  "healthy",
			"service": "demo-backend",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// gRPC server implementation
type demoServer struct {
	pb.UnimplementedDemoServiceServer
	cfg *Config
}

func (s *demoServer) GetBlockHeight(ctx context.Context, req *pb.Empty) (*pb.BlockHeightResponse, error) {
	requestsTotal.WithLabelValues("grpc", "GetBlockHeight").Inc()
	return &pb.BlockHeightResponse{Height: 1}, nil
}

func (s *demoServer) GetBlock(ctx context.Context, req *pb.BlockRequest) (*pb.BlockResponse, error) {
	requestsTotal.WithLabelValues("grpc", "GetBlock").Inc()
	return &pb.BlockResponse{
		Number:       req.Number,
		Hash:         "0x1234567890abcdef",
		Timestamp:    time.Now().Unix(),
		Transactions: []string{"0xabc", "0xdef"},
	}, nil
}

func (s *demoServer) StreamBlocks(req *pb.StreamRequest, stream pb.DemoService_StreamBlocksServer) error {
	requestsTotal.WithLabelValues("grpc", "StreamBlocks").Inc()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	height := req.StartHeight
	for range ticker.C {
		block := &pb.BlockResponse{
			Number:       height,
			Hash:         "0x1234567890abcdef",
			Timestamp:    time.Now().Unix(),
			Transactions: []string{"0xabc"},
		}
		if err := stream.Send(block); err != nil {
			return err
		}
		height++
	}
	return nil
}

func (s *demoServer) HealthCheck(ctx context.Context, req *pb.Empty) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{
		Healthy: true,
		Status:  "ok",
	}, nil
}

func startGRPCServer(cfg *Config) {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPort))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterDemoServiceServer(grpcServer, &demoServer{cfg: cfg})

	log.Printf("gRPC server listening on :%d", cfg.GRPCPort)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("gRPC server error: %v", err)
	}
}

// Helper functions

func getStaticBlock() map[string]interface{} {
	return map[string]interface{}{
		"number":       "0x1",
		"hash":         "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		"parentHash":   "0x0000000000000000000000000000000000000000000000000000000000000000",
		"timestamp":    "0x" + fmt.Sprintf("%x", time.Now().Unix()),
		"transactions": []string{},
	}
}

func shouldInjectError(rate float64) bool {
	return false // Simplified: no random errors for now
}
