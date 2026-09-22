# CDNNow calc: build / test / run shortcuts.
#
#   make libs       — build native .so via build.sh (needs gcc + cargo)
#   make build      — build Go binaries (works even without .so: Go fallback)
#   make test       — unit tests
#   make test-race  — unit tests with race detector
#   make tests      — all checks: build + fmt + vet + test + test-race
#   make vet fmt    — static checks
#   make run-server — build + run server  (PORT=8080 INTERVAL=5.0)
#   make run-gen    — build + run generator (GEN_URL=... GEN_THREADS=10 GEN_INTERVAL=0.1)
#   make clean      — remove built binaries

GO ?= go
PORT ?= 8080
INTERVAL ?= 5.0
GEN_URL ?= http://localhost:8080/calc
GEN_THREADS ?= 10
GEN_INTERVAL ?= 0.1

SERVER_BIN = bin/calculator_server
GEN_BIN = bin/generator

.PHONY: help libs build test test-race tests vet fmt run-server run-gen clean

help:
	@echo "Available targets:"
	@echo "  libs        build C/Rust shared libraries via build.sh"
	@echo "  build       build Go binaries into bin/"
	@echo "  test        run unit tests"
	@echo "  test-race   run unit tests with race detector"
	@echo "  tests       run all checks: build + fmt + vet + test + test-race"
	@echo "  vet         go vet"
	@echo "  fmt         check gofmt (fails on unformatted files)"
	@echo "  run-server  build and run server (PORT, INTERVAL)"
	@echo "  run-gen     build and run generator (GEN_URL, GEN_THREADS, GEN_INTERVAL)"
	@echo "  clean       remove built binaries"

libs:
	./build.sh
	mkdir -p bin
	cp libcalculator.so libcalculator_rust.so bin/
	@echo "libraries copied to bin/ next to the server binary"

build:
	mkdir -p bin
	$(GO) build -o $(SERVER_BIN) ./server
	$(GO) build -o $(GEN_BIN) ./generator

test:
	$(GO) test ./server/ ./generator/

test-race:
	$(GO) test -race -count=1 ./server/ ./generator/

tests: build fmt vet test test-race

vet:
	$(GO) vet ./server/ ./generator/

fmt:
	@test -z "$$(gofmt -l server generator)" || (echo "unformatted files:"; gofmt -l server generator; exit 1)

run-server: build
	./$(SERVER_BIN) --port $(PORT) --interval $(INTERVAL)

run-gen: build
	./$(GEN_BIN) --url $(GEN_URL) -n $(GEN_THREADS) --interval $(GEN_INTERVAL)

clean:
	rm -rf bin
	$(GO) clean
	rm -rf rust_lib/target
