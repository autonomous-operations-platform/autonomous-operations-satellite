# SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and Autonomous Operations Platform contributors
#
# SPDX-License-Identifier: Apache-2.0

COVERAGE_THRESHOLD := 95
COVERAGE_DIR      := coverage
COVERAGE_PROFILE  := $(COVERAGE_DIR)/coverage.out

.PHONY: build coverage lint test vet

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/satellite ./cmd/satellite

coverage:
	go tool cover -html=$(COVERAGE_PROFILE)

lint:
	golangci-lint run ./...

test:
	mkdir -p $(COVERAGE_DIR)
	go test -coverprofile=$(COVERAGE_PROFILE) ./...
	@scripts/check-coverage.sh $(COVERAGE_PROFILE) $(COVERAGE_THRESHOLD)

vet:
	go vet ./...
