// Package proto implements the XML-ish application protocol used by the UI.
//
// The dp8 engine parses inbound messages into a small `Msg` shape and routes
// them through `Engine.Handle`, which emits response messages to send back over
// the DirectPlay transport.
package proto
