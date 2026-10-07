# SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and Autonomous Operations Platform contributors
#
# SPDX-License-Identifier: Apache-2.0

FROM golang:1.27.1 AS builder
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /satellite ./cmd/satellite

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /satellite /satellite
ENTRYPOINT ["/satellite"]
