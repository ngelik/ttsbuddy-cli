MODULE          := github.com/ngelik/ttsbuddy-cli
COBRA_VERSION   := v1.8.1
GORELEASER_VER  := v2.6.1
LINT_VERSION    := v2.11.4

VERSION ?= dev
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
  -X $(MODULE)/cmd.Version=$(VERSION) \
  -X $(MODULE)/cmd.Commit=$(COMMIT) \
  -X $(MODULE)/cmd.Date=$(DATE)

.PHONY: build test test-live test-acceptance lint tools release-snapshot clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/ttsbuddy .

test:
	go test -race -count=1 ./...

test-live:
	TTSBUDDY_TEST_LIVE=1 go test -race -count=1 -run TestLive ./...

test-acceptance:
	@echo "Running acceptance tests (requires TTSBUDDY_API_KEY)..."
	BINARY=bin/ttsbuddy ./tests/acceptance_test.sh

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not found on PATH; running pinned $(LINT_VERSION) via go run"; \
		go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VERSION) run; \
	fi

tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VERSION)
	go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VER)

release-snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf bin/ dist/
