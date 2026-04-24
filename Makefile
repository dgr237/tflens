# tflens Makefile
# Run `make` or `make help` to see the available targets.

BINARY := tflens
GO     := go

.DEFAULT_GOAL := help

.PHONY: help build install test test-race test-verbose coverage vet fmt fmt-check check clean release release-push

help:
	@echo "tflens - targets:"
	@echo ""
	@echo "  build         Build the tflens binary"
	@echo "  install       Install the binary into \$$(go env GOPATH)/bin"
	@echo ""
	@echo "  test          Run all tests"
	@echo "  test-race     Run tests with the race detector"
	@echo "  test-verbose  Run tests with -v"
	@echo "  coverage      Run tests and produce coverage.html"
	@echo ""
	@echo "  vet           Run go vet"
	@echo "  fmt           Format all .go files with gofmt"
	@echo "  fmt-check     Verify formatting without writing (suitable for CI)"
	@echo "  check         Run vet, fmt-check, and tests"
	@echo ""
	@echo "  release       Promote CHANGELOG [Unreleased] to v\$$(VERSION), commit and tag locally"
	@echo "  release-push  Same as release, plus push commit and tag to origin"
	@echo ""
	@echo "  clean         Remove build artifacts"

# ---- build ----

build:
	$(GO) build -o $(BINARY) .

install:
	$(GO) install .

# ---- test ----

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

test-verbose:
	$(GO) test -v ./...

coverage:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report written to coverage.html"

# ---- static checks ----

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
		echo "The following files are not gofmt'd:"; \
		echo "$$out"; \
		exit 1; \
	fi

check: vet fmt-check test

# ---- release ----
#
# `make release VERSION=0.1.4` runs scripts/release.sh, which promotes
# CHANGELOG.md's [Unreleased] section to a versioned section,
# commits the change, and creates an annotated tag — all locally.
# Add release-push to push commit + tag to origin.

release:
	@if [ -z "$(VERSION)" ]; then \
		echo "error: VERSION not set. Usage: make release VERSION=X.Y.Z"; exit 1; \
	fi
	./scripts/release.sh $(VERSION)

release-push:
	@if [ -z "$(VERSION)" ]; then \
		echo "error: VERSION not set. Usage: make release-push VERSION=X.Y.Z"; exit 1; \
	fi
	./scripts/release.sh $(VERSION) --push

# ---- housekeeping ----

clean:
	$(GO) clean
	@rm -f $(BINARY) $(BINARY).exe coverage.out coverage.html
