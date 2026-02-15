package packetlog

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
)

type Record struct {
	RunID       string `json:"run_id"`
	Timestamp   string `json:"ts"`
	Type        string `json:"type"`
	Direction   string `json:"direction,omitempty"`
	Source      string `json:"src,omitempty"`
	Destination string `json:"dst,omitempty"`
	Length      int    `json:"len,omitempty"`
	ReplyMode   string `json:"reply_mode,omitempty"`
	Tag         string `json:"tag,omitempty"`
	Experiment  string `json:"exp,omitempty"`
	Message     string `json:"message,omitempty"`
}

type Logger struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

func New(path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Logger{
		f: f,
		w: bufio.NewWriterSize(f, 256*1024),
	}, nil
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w != nil {
		_ = l.w.Flush()
	}
	if l.f != nil {
		return l.f.Close()
	}
	return nil
}

func (l *Logger) Log(rec Record) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.w == nil {
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_, _ = l.w.Write(append(line, '\n'))
	_ = l.w.Flush()
}
