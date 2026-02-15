// Package autoupdate provides a minimal TCP sink for the game's update checks.
//
// The implementation intentionally accepts connections and closes them quickly.
// It exists to keep the client moving through UI flows that expect an update
// endpoint to be reachable.
package autoupdate
