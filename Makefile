.PHONY: build test clean install help docker-build docker-push

# Binary name
BINARY_NAME=pocket-relay-miner

# Build directory
BUILD_DIR=bin

# Docker image configuration
DOCKER_IMAGE?=ghcr.io/pokt-network/pocket-relay-miner:rc

# Go build flags
LDFLAGS=-ldflags "-s -w"

help: ## Display this help message
	@echo "Pocket RelayMiner Makefile"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

build: ## Build the pocket-relay-miner binary
	@echo "Building $(BINARY_NAME)..."
	@go build $(LDFLAGS) -o ./$(BUILD_DIR)/$(BINARY_NAME) .
	@echo "Build complete: ./$(BUILD_DIR)/$(BINARY_NAME)"

build-release: ## Build optimized release binary
	@echo "Building release binary..."
	@mkdir -p $(BUILD_DIR)
	@CGO_ENABLED=0 go build $(LDFLAGS) -trimpath -o $(BUILD_DIR)/$(BINARY_NAME) .
	@echo "Release build complete: $(BUILD_DIR)/$(BINARY_NAME)"

install: ## Install the binary to $GOPATH/bin
	@echo "Installing $(BINARY_NAME) to $$GOPATH/bin..."
	@go install $(LDFLAGS) .
	@echo "Install complete"

test: ## Run tests
	@echo "Running tests..."
	@go test -v -race -tags test ./...

test-coverage: ## Run tests with coverage
	@echo "Running tests with coverage..."
	@go test -v -race -tags test -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

clean: ## Clean build artifacts
	@echo "Cleaning build artifacts..."
	@rm -f $(BINARY_NAME)
	@rm -rf $(BUILD_DIR)
	@rm -f coverage.out coverage.html
	@echo "Clean complete"

tidy: ## Run go mod tidy
	@echo "Running go mod tidy..."
	@go mod tidy

fmt: ## Format code
	@echo "Formatting code..."
	@go fmt ./...

lint: ## Run golangci-lint
	@echo "Running linters..."
	@golangci-lint run

docker-build: ## Build Docker image (override with DOCKER_IMAGE env var)
	@echo "Building Docker image: $(DOCKER_IMAGE)..."
	@docker build -t $(DOCKER_IMAGE) .
	@echo "Docker build complete: $(DOCKER_IMAGE)"

docker-push: ## Push Docker image to registry
	@echo "Pushing Docker image: $(DOCKER_IMAGE)..."
	@docker push $(DOCKER_IMAGE)
	@echo "Docker push complete: $(DOCKER_IMAGE)"

.DEFAULT_GOAL := help
