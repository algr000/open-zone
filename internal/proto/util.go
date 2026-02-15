package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

func NowTS() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func MakeRunID() string {
	// Avoid embedding timestamps in identifiers. Use a random UUID.
	id, err := uuid.NewRandom()
	if err != nil {
		// Extremely rare; keep it unique-ish without leaking wall-clock date formatting.
		return fmt.Sprintf("run-%d", time.Now().UTC().UnixNano())
	}
	return "run-" + id.String()
}

func ResetLogFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, nil, 0o644)
}

func ToHex(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.Grow(len(b) * 3)
	for i, v := range b {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(fmt.Sprintf("%02X", v))
	}
	return sb.String()
}

func SecondsSince2000UTC(now time.Time) uint64 {
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if now.Before(base) {
		return 0
	}
	return uint64(now.Sub(base) / time.Second)
}
