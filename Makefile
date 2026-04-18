# Velocity framework Makefile
#
# Integration tests are gated behind real external services (Postgres,
# Redis, MinIO). The default `go test ./...` skips them via build tags.
# This file's `test-integration` target performs env-var gating BEFORE
# invoking `go test` — a missing var here prints an actionable setup
# message. A missing var inside a test would fire TestMain's os.Exit(1)
# with the same list, but doing it here short-circuits the go test
# startup cost and gives operators the instructions in one place.
#
# Design note: we deliberately do NOT use `t.Skip` to handle a missing
# service in CI. A silent skip turns "MinIO sidecar failed to start"
# into a green build — the exact signal-drop we're trying to avoid.

.PHONY: test test-integration test-all

# Default: run the fast, hermetic suite with the race detector.
test:
	go test -race -timeout 300s ./...

# Integration suite: real services required. Invoke via `make test-integration`.
# Set the env vars below before running; the target fails fast if any are missing.
test-integration:
	@missing=""; \
	for v in POSTGRES_URL MINIO_ENDPOINT MINIO_ACCESS_KEY MINIO_SECRET_KEY MINIO_BUCKET MINIO_REGION REDIS_HOST REDIS_PORT; do \
		if [ -z "$$(eval echo \$$$$v)" ]; then missing="$$missing $$v"; fi; \
	done; \
	if [ -n "$$missing" ]; then \
		echo "Integration tests require the following env vars:"; \
		for v in $$missing; do echo "  $$v (unset)"; done; \
		echo ""; \
		echo "Minimum setup (using local docker):"; \
		echo "  docker run -d -p 5432:5432 -e POSTGRES_PASSWORD=test postgres:16"; \
		echo "  docker run -d -p 6379:6379 redis:7"; \
		echo "  docker run -d -p 9000:9000 -e MINIO_ROOT_USER=minioadmin -e MINIO_ROOT_PASSWORD=minioadmin minio/minio server /data"; \
		echo ""; \
		echo "Then export:"; \
		echo "  export POSTGRES_URL='postgres://postgres:test@localhost:5432/postgres?sslmode=disable'"; \
		echo "  export REDIS_HOST=localhost REDIS_PORT=6379"; \
		echo "  export MINIO_ENDPOINT=http://localhost:9000 MINIO_ACCESS_KEY=minioadmin MINIO_SECRET_KEY=minioadmin"; \
		echo "  export MINIO_BUCKET=velocity-test MINIO_REGION=us-east-1"; \
		echo "  (create the bucket once: mc alias set local http://localhost:9000 minioadmin minioadmin && mc mb local/velocity-test)"; \
		exit 1; \
	fi
	go test -tags=integration -race -timeout 600s ./...

# Run everything: unit + integration.
test-all: test test-integration
