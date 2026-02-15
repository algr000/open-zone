package news

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	srv *http.Server
}

func Start(ctx context.Context, addr string, provider func() Data) (*Server, error) {
	if addr == "" {
		return nil, fmt.Errorf("news addr is empty")
	}

	tmpl, err := loadTemplate()
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		var data Data
		if provider != nil {
			data = provider()
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			http.Error(w, "News Template Error", http.StatusInternalServerError)
			return
		}

		// The client is happiest with CRLF. Normalize to avoid mixed newline styles.
		body := ensureCRLF(buf.String())

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = ioWriteString(w, body)
	})

	s := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ns := &Server{srv: s}
	go func() {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	}()

	go func() { _ = s.ListenAndServe() }()
	return ns, nil
}

func ensureCRLF(s string) string {
	// Convert lone LF into CRLF; keep existing CRLF as-is.
	if !strings.Contains(s, "\n") {
		return s
	}
	// First, normalize CRLF -> LF, then LF -> CRLF.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", "\r\n")
	return s
}

func ioWriteString(w http.ResponseWriter, s string) (int, error) {
	// Small helper to keep server.go free from an extra io import.
	return w.Write([]byte(s))
}
