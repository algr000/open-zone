// Package dp8 runs the DirectPlay8-based server loop.
//
// It receives transport events from the DP8 shim, decodes the XML-ish
// application messages carried in RECEIVE payloads, and sends responses back to
// the requesting client.
package dp8
