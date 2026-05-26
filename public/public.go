package public

import (
	"embed"
	"encoding/json"
	"html/template"
	"net/http"
	"strings"

	"hotel/downloader"
	"hotel/store"
)

//go:embed templates/*
var templatesFS embed.FS

var tmpl = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

type Handler struct {
	store    *store.VideoStore
	download *downloader.Downloader
}

func NewHandler(s *store.VideoStore, d *downloader.Downloader) *Handler {
	return &Handler{store: s, download: d}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /", h.handleIndex)
	mux.HandleFunc("GET /api/videos", h.handleGetVideos)
	mux.HandleFunc("POST /api/videos/{id}/view", h.handleIncrementView)
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.ExecuteTemplate(w, "index.html", nil)
}

func (h *Handler) handleGetVideos(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	var videos []store.Video

	if strings.TrimSpace(query) != "" {
		videos = h.store.Search(query)
	} else {
		videos = h.store.GetAll()
	}

	if videos == nil {
		videos = []store.Video{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(videos)
}

func (h *Handler) handleIncrementView(w http.ResponseWriter, r *http.Request) {
	videoID := r.PathValue("id")
	h.store.IncrementView(videoID)
	w.WriteHeader(http.StatusOK)
}
