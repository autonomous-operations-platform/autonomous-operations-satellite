COVERAGE_THRESHOLD := 95
COVERAGE_DIR      := coverage
COVERAGE_PROFILE  := $(COVERAGE_DIR)/coverage.out

.PHONY: build clean coverage lint proto test

build:
	go build -o bin/ocg-connector ./cmd/ocg-connector

proto:
	buf dep update
	buf lint
	buf generate

clean:
	rm -rf bin/ $(COVERAGE_DIR)/

coverage:
	go tool cover -html=$(COVERAGE_PROFILE)

lint:
	golangci-lint run ./...

test:
	mkdir -p $(COVERAGE_DIR)
	go test -coverprofile=$(COVERAGE_PROFILE) ./...
	@scripts/check-coverage.sh $(COVERAGE_PROFILE) $(COVERAGE_THRESHOLD)
