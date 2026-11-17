GO ?= go

.PHONY: all build test vet fmt fmt-check run db-up db-down db-shell migrate check

all: check

build:
	$(GO) build ./...

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "not gofmt'd:"; echo "$$out"; exit 1; fi

run:
	$(GO) run ./cmd/leutik-tarik

# Dev ClickHouse. Integration tests are skipped unless LEUTIK_CLICKHOUSE_URL is set.
db-up:
	docker compose up -d --wait clickhouse

db-down:
	docker compose down

db-shell:
	docker compose exec clickhouse clickhouse-client

# Apply pending migrations. Versioned via schema_migrations, so this is safe to
# re-run and detects a migration edited after being applied.
migrate:
	LEUTIK_CLICKHOUSE_URL=$${LEUTIK_CLICKHOUSE_URL:?set LEUTIK_CLICKHOUSE_URL} \
		$(GO) run ./cmd/leutik-tarik -migrate

# Rebuild the eBPF object. Runs in a container because Apple's clang cannot
# target bpf at all (docs/agent-portability.md §5b). The .o is committed, so
# normal builds and CI need no clang.
agent-bpf:
	docker run --rm -v "$(CURDIR)":/w -w /w alpine:3.20 sh -c \
		'apk add --no-cache clang llvm >/dev/null && \
		 clang -O2 -g -target bpf -mcpu=v3 -c internal/agent/bpf/src/probe.bpf.c \
		       -o internal/agent/bpf/probe.bpf.o'

check: fmt-check vet build test
