# psxdh developer commands. Run `make` or `make help` to list them.

BINARY  := psxdh
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
DIST    := dist

# Pass extra flags to `make run`, e.g. make run ARGS="--library ~/Games/ps5"
ARGS ?=

ifeq ($(OS),Windows_NT)
  EXE := .exe
endif

.DEFAULT_GOAL := help

.PHONY: help
help: ## List the available commands
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: run
run: build ## Build and run psxdh (ARGS="--library DIR" to pass flags)
	@./$(BINARY)$(EXE) $(ARGS) || [ $$? -eq 130 ]

.PHONY: build
build: ## Build ./psxdh for this machine
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY)$(EXE) ./cmd/psxdh
	@echo "Built ./$(BINARY)$(EXE) ($(VERSION))"

.PHONY: test
test: ## Run all tests with the race detector, end-to-end tests included
	go test -race -timeout 300s ./...

.PHONY: test-quick
test-quick: ## Run unit tests only (skips the end-to-end tests)
	go test ./internal/...

.PHONY: check
check: fmt-check vet test ## Everything CI checks except lint: formatting, vet, tests

.PHONY: fmt
fmt: ## Format the code
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any file needs formatting
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "Needs gofmt:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint (install: https://golangci-lint.run/welcome/install/)
	golangci-lint run ./...

.PHONY: dist
dist: ## Build release binaries for every OS/arch into ./dist
	@mkdir -p $(DIST)
	@for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; ext=""; [ "$$os" = windows ] && ext=".exe"; \
		out="$(DIST)/$(BINARY)_$(VERSION)_$${os}_$${arch}$${ext}"; \
		echo "  $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o "$$out" ./cmd/psxdh || exit 1; \
	done

.PHONY: clean
clean: ## Remove build output
	rm -rf $(DIST) $(BINARY) $(BINARY).exe
