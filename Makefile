# SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and Autonomous Operations Platform contributors
#
# SPDX-License-Identifier: Apache-2.0

.PHONY: build test test-race vet

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/satellite ./cmd/satellite

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...
