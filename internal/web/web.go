// Package web serves the embedded inspection UI and its read-only JSON API.
package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/travisennis/cake-proxy/internal/capture"
)

//go:embed static
var staticFiles embed.FS

// Info describes the running proxy to the UI.
type Info struct {
	Upstream string `json:"upstream"`
	Listen   string `json:"listen"`
	DataDir  string `json:"data_dir"`
	Keep     int    `json:"keep"`
}

type handler struct {
	store  *capture.Store
	info   Info
	log    *slog.Logger
	index  []byte
	assets http.Handler
}

// New builds the UI handler. Mount it at / alongside the proxy's /v1 mount.
func New(store *capture.Store, info Info, logger *slog.Logger) (http.Handler, error) {
	if logger == nil {
		logger = slog.Default()
	}
	index, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		return nil, err
	}
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	h := &handler{
		store:  store,
		info:   info,
		log:    logger,
		index:  index,
		assets: http.StripPrefix("/assets/", http.FileServer(http.FS(sub))),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.serveIndex)
	mux.HandleFunc("GET /assets/{path...}", h.serveAsset)
	mux.HandleFunc("GET /api/config", h.serveConfig)
	mux.HandleFunc("GET /api/exchanges", h.serveList)
	mux.HandleFunc("DELETE /api/exchanges", h.clear)
	mux.HandleFunc("GET /api/exchanges/{id}", h.serveExchange)
	mux.HandleFunc("GET /healthz", h.serveHealth)
	mux.HandleFunc("/", h.notFound)
	return mux, nil
}

func (h *handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	_, _ = w.Write(h.index)
}

func (h *handler) serveAsset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	h.assets.ServeHTTP(w, r)
}

func (h *handler) serveConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"upstream": h.info.Upstream,
		"listen":   h.info.Listen,
		"data_dir": h.info.DataDir,
		"keep":     h.info.Keep,
		"count":    h.store.Count(),
	})
}

func (h *handler) serveList(w http.ResponseWriter, r *http.Request) {
	exchanges := h.store.List()
	writeJSON(w, http.StatusOK, map[string]any{
		"exchanges": exchanges,
		"count":     len(exchanges),
	})
}

func (h *handler) serveExchange(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	exchange, ok := h.store.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "exchange not found")
		return
	}
	writeJSON(w, http.StatusOK, exchange)
}

func (h *handler) clear(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Clear(); err != nil {
		h.log.Warn("clear exchanges", "error", err)
		writeError(w, http.StatusInternalServerError, "clear exchanges: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) serveHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *handler) notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not found; the proxy API is under /v1, the inspector is at /")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}
