# Needless — developer entry points.
#
# `make` on its own prints the target list. Everything here is a thin wrapper
# around the go tool plus a few file operations; there is no hidden state.

MODULE := github.com/mhs003/needless

# Where the binaries land. The default keeps `n` and `needle-worker` next to
# each other, which is how the worker is discovered at runtime, and leaves
# models/ resolvable when run from a checkout.
BIN_DIR    ?= .
N_BIN      ?= $(BIN_DIR)/n
WORKER_BIN ?= $(BIN_DIR)/needle-worker

# Where `make install-examples` puts the shipped examples.
COMMANDS_DIR ?= $(HOME)/.needless/commands

# Where `make install` puts the binaries.
PREFIX ?= $(HOME)/.local

GO      ?= go
GOFLAGS ?=
GOFMT   ?= gofmt

.DEFAULT_GOAL := help

.PHONY: help all build worker test test-short test-e2e test-race vet fmt fmt-check check \
        clean install-examples install install-model uninstall doctor run

help: ## Show this help
	@echo "Needless"
	@echo
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "Variables:"
	@echo "  BIN_DIR=$(BIN_DIR)  COMMANDS_DIR=$(COMMANDS_DIR)  PREFIX=$(PREFIX)"

all: check build ## Format-check, vet, test, then build

build: ## Build n and needle-worker
	$(GO) build $(GOFLAGS) -o $(N_BIN) ./cmd/n
	$(GO) build $(GOFLAGS) -o $(WORKER_BIN) ./needle/cmd/needle-worker

worker: ## Build only needle-worker
	$(GO) build $(GOFLAGS) -o $(WORKER_BIN) ./needle/cmd/needle-worker

test: ## Run the whole test suite (includes the real-model tests)
	$(GO) test $(GOFLAGS) ./...

test-short: ## Run the suite without anything that needs the model
	$(GO) test $(GOFLAGS) -short ./...

test-e2e: ## Run only the real-model and real-engine tests, verbosely
	$(GO) test $(GOFLAGS) -count=1 -v -run 'E2E|RealModel|RealEngine' ./internal/... ./cmd/... ./needle/...

test-race: ## Run the suite under the race detector
	$(GO) test $(GOFLAGS) -race ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format all Go source
	$(GO) fmt ./...

fmt-check: ## Fail if anything is not gofmt-formatted
	@unformatted=$$($(GOFMT) -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt-formatted:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

check: fmt-check vet test ## The pre-commit gate: fmt-check, vet, test

clean: ## Remove built binaries and the test cache
	rm -f $(N_BIN) $(WORKER_BIN)
	$(GO) clean -testcache

install-examples: ## Copy the example commands into COMMANDS_DIR
	@mkdir -p $(COMMANDS_DIR)
	@cp -r examples/commands/. $(COMMANDS_DIR)/
	@echo "installed into $(COMMANDS_DIR):"
	@find $(COMMANDS_DIR) -name '*.nsc' \
		| sed "s|^$(COMMANDS_DIR)/||; s|\.nsc$$||" \
		| sort | sed 's|^|  |'

install: build ## Install n and needle-worker into PREFIX/bin
	@mkdir -p $(PREFIX)/bin
	@install -m 0755 $(N_BIN) $(PREFIX)/bin/n
	@install -m 0755 $(WORKER_BIN) $(PREFIX)/bin/needle-worker
	@echo "installed into $(PREFIX)/bin"
	@case ":$$PATH:" in *":$(PREFIX)/bin:"*) ;; *) \
		echo "note: $(PREFIX)/bin is not on your PATH";; esac

install-model: ## Copy the model archive into ~/.needless/models
	@test -f models/needle3.cact || { echo "models/needle3.cact is missing"; exit 1; }
	@mkdir -p $(HOME)/.needless/models
	@cp models/needle3.cact $(HOME)/.needless/models/needle3.cact
	@echo "installed the model into $(HOME)/.needless/models"

uninstall: ## Remove the installed binaries
	@rm -f $(PREFIX)/bin/n $(PREFIX)/bin/needle-worker
	@echo "removed n and needle-worker from $(PREFIX)/bin"

doctor: ## Report where each piece is and whether it is found
	@echo "go:        $$($(GO) version)"
	@echo -n "commands:  $(COMMANDS_DIR) "; \
		if [ -d "$(COMMANDS_DIR)" ]; then \
			echo "($$(find $(COMMANDS_DIR) -name '*.nsc' | wc -l) commands)"; \
		else \
			echo "(missing; run make install-examples)"; \
		fi
	@echo -n "model:     "; \
		if [ -f models/needle3.cact ]; then \
			echo "models/needle3.cact ($$(du -h models/needle3.cact | cut -f1))"; \
		elif [ -f "$(HOME)/.needless/models/needle3.cact" ]; then \
			echo "$(HOME)/.needless/models/needle3.cact"; \
		else \
			echo "(missing; see README)"; \
		fi
	@echo -n "engine:    "; \
		ls needle/engine/*/libneedle.* 2>/dev/null || echo "(missing)"
	@echo -n "worker:    "; \
		if [ -x "$(WORKER_BIN)" ]; then \
			echo "$(WORKER_BIN)"; \
		else \
			echo "$(WORKER_BIN) (not built; run make build)"; \
		fi

run: build ## Build, then run n; pass a prompt with ARGS="..."
	$(N_BIN) $(ARGS)
