# Run everything from the repo root:  make build && make run && make serve
BIN := backend/bin/screener

.PHONY: build run serve dev test show

build:
	cd frontend && npm install && npm run build
	cd backend && go build -o bin/screener ./cmd/screener

# Full screen: downloads SEC data (≈1.4GB, at most weekly) + Yahoo market data, prints results.
run:
	cd backend && ./bin/screener run

# Web UI + API at http://127.0.0.1:8090
serve:
	cd backend && ./bin/screener serve

# Frontend hot reload on :5180 (needs `make serve` running for /api).
dev:
	cd frontend && npm run dev

test:
	cd backend && go vet ./... && go test ./...

# make show T=JD
show:
	cd backend && ./bin/screener show $(T)
