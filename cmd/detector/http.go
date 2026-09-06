package main

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// serveHTTP exposes a minimal read-only surface: health, per-stream live
// preview, and per-stream event clips (so the "🔗 Веб" button can link to one).
func serveHTTP(ctx context.Context, addr, recRoot string, previews *previewStore) {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// /s/<id>/preview.jpg
	mux.HandleFunc("/s/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/s/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) < 2 {
			http.NotFound(w, r)
			return
		}
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case parts[1] == "preview.jpg":
			jpg := previews.get(id)
			if jpg == nil {
				http.Error(w, "no frame", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(jpg)
		case strings.HasPrefix(parts[1], "clips/"):
			name := filepath.Base(strings.TrimPrefix(parts[1], "clips/"))
			http.ServeFile(w, r, filepath.Join(eventsDir(recRoot, id), name))
		default:
			http.NotFound(w, r)
		}
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sc)
	}()
	_ = srv.ListenAndServe()
}
