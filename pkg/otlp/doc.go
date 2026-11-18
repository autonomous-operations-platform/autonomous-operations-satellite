// Package otlp lets satellite collectors ingest OpenTelemetry Protocol (OTLP)
// telemetry over HTTP with one call.
//
// A collector calls Start from its Setup with the collector store and one or
// more endpoints. Each endpoint serves POST <BasePath>/<signal> for its
// signals, decodes OTLP JSON bodies into store.NewTelemetryRecord values,
// runs an optional Processor, and writes the result to the store.
//
// Separate Start calls MAY use the same Addr string. They share one listener
// when their signal paths differ and their timeouts match. Different strings
// for one port, such as "localhost:4318" and "127.0.0.1:4318", do not share.
//
// Responses follow OTLP/HTTP: 200 with body {} on success, 400 for invalid
// bodies, 404 for unknown paths, 405 for wrong methods, 413 for oversized
// bodies, 415 for unsupported content types or encodings, 500 for processor
// errors, and 503 for store errors and stopping endpoints so exporters
// retry. Error bodies are JSON-encoded google.rpc.Status messages with a
// fixed message. They never carry internal error text, and the library
// never logs headers or payloads.
//
// Only OTLP JSON is supported, optionally gzip-encoded.
package otlp
