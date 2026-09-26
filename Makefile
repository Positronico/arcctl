GO ?= go
BINARY := arcctl
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: check vendorguard fmt vet test mirror-only build clean

check: vendorguard fmt vet test mirror-only

vendorguard:
	@bash scripts/vendorguard.sh

fmt:
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then echo "gofmt: these files need formatting:"; echo "$$out"; exit 1; fi; \
	echo "gofmt: ok"

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

mirror-only:
	@if [ -z "$$ARCCTL_MIRROR" ]; then \
		echo "mirror-only: skipped (ARCCTL_MIRROR unset)"; \
	elif [ ! -d "$$ARCCTL_MIRROR" ]; then \
		echo "mirror-only: skipped (ARCCTL_MIRROR not found: $$ARCCTL_MIRROR)"; \
	else \
		echo "mirror-only: using $$ARCCTL_MIRROR"; \
		echo "mirror-only: nothing to run yet; M1 adds the vendor-to-facts drift check and the oracle vector rerun"; \
	fi

build:
	@if [ -d cmd/arcctl ]; then \
		CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/arcctl; \
	else \
		CGO_ENABLED=0 $(GO) build ./... && echo "build: no cmd/arcctl yet, packages compiled, no binary written"; \
	fi

clean:
	rm -rf $(BINARY) dist coverage.out
