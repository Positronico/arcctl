GO ?= go
BINARY := arcctl
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
FUZZTIME ?= 5s
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64

.PHONY: check vendorguard fmt vet staticcheck test fuzz cross mirror-only drift oracle generate build clean

check: vendorguard fmt vet staticcheck test fuzz cross mirror-only

vendorguard:
	@bash scripts/vendorguard.sh

fmt:
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then echo "gofmt: these files need formatting:"; echo "$$out"; exit 1; fi; \
	echo "gofmt: ok"

vet:
	$(GO) vet ./...

staticcheck:
	$(GO) tool staticcheck ./...

test:
	$(GO) test -race ./...

fuzz:
	@set -e; log=$$(mktemp); trap 'rm -f "$$log"' EXIT; \
	for pkg in $$($(GO) list ./...); do \
		for f in $$($(GO) test -list '^Fuzz' $$pkg | grep '^Fuzz' || true); do \
			echo "fuzz: $$pkg $$f ($(FUZZTIME))"; \
			if ! $(GO) test -run '^$$' -fuzz "^$$f$$" -fuzztime $(FUZZTIME) $$pkg >"$$log" 2>&1; then \
				tail -40 "$$log"; exit 1; \
			fi; \
		done; \
	done

cross:
	@set -e; for p in $(PLATFORMS); do \
		echo "cross: $$p"; \
		CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} $(GO) build ./...; \
	done

mirror-only: drift oracle

drift:
	@GO="$(GO)" bash scripts/drift.sh

oracle:
	@bash scripts/oracle.sh

generate:
	$(GO) generate ./internal/catalog/...

build:
	@if [ -d cmd/arcctl ]; then \
		CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/arcctl; \
	else \
		CGO_ENABLED=0 $(GO) build ./... && echo "build: no cmd/arcctl yet, packages compiled, no binary written"; \
	fi

clean:
	rm -rf $(BINARY) dist coverage.out
