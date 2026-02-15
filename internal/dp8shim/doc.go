//go:build windows

// Package dp8shim provides a tiny Windows-only wrapper around the bundled
// `dp8shim.dll`.
//
// The shim hosts a DirectPlay8 server and exposes a minimal C ABI used by the Go
// process to pop queued events and send payloads to connected clients.
package dp8shim
