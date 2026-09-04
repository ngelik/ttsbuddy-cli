MODULE          := github.com/ngelik/ttsbuddy-cli
GORELEASER_VER  := v2.18.0
LINT_VERSION    := v2.13.2
GOVULNCHECK_VER := v1.7.0
SYFT_VERSION    := v1.51.1

VERSION ?= dev
CLERK_OAUTH_CLIENT_ID ?= gRApqxGCscvVfceh
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
  -X $(MODULE)/cmd.Version=$(VERSION) \
  -X $(MODULE)/cmd.Commit=$(COMMIT) \
  -X $(MODULE)/cmd.Date=$(DATE) \
  -X $(MODULE)/cmd.ClerkOAuthClientID=$(CLERK_OAUTH_CLIENT_ID)

.PHONY: build test test-live test-acceptance lint vuln tools release-snapshot check-actions-pinned clerk-auth-probe clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/ttsbuddy ./cmd/ttsbuddy

test:
	go test -race -count=1 ./...
	./tests/acceptance_privacy_test.sh

test-live:
	TTSBUDDY_TEST_LIVE=1 go test -race -count=1 -run TestLive ./...

clerk-auth-probe:
	go run ./tools/clerk-fapi-probe

test-acceptance:
	@echo "Running acceptance tests (requires TTSBUDDY_API_KEY)..."
	BINARY=bin/ttsbuddy ./tests/acceptance_test.sh

lint:
	golangci-lint run

vuln:
	govulncheck ./...

tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VERSION)
	go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VER)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VER)
	go install github.com/anchore/syft/cmd/syft@$(SYFT_VERSION)

release-snapshot:
	test -n "$(CLERK_OAUTH_CLIENT_ID)"
	CLERK_OAUTH_CLIENT_ID="$(CLERK_OAUTH_CLIENT_ID)" goreleaser release --snapshot --clean --skip=sign

check-actions-pinned:
	./scripts/check-github-actions-pinned.sh

clean:
	rm -rf bin/ dist/
