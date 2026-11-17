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

# Apply migrations to the dev database (idempotent DDL).
migrate:
	@for f in migrations/*.sql; do \
		echo "applying $$f"; \
		curl -sS --fail --data-binary @$$f "$${LEUTIK_CLICKHOUSE_URL%/}/"; \
	done

check: fmt-check vet build test
