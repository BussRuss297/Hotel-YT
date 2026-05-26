package admin

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hotel/downloader"
	"hotel/store"
)

//go:embed templates/*
var templatesFS embed.FS

type Handler struct {
	store    *store.VideoStore
	download *downloader.Downloader
}

func NewHandler(s *store.VideoStore, d *downloader.Downloader) *Handler {
	return &Handler{store: s, download: d}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", h.handleAdmin)
	mux.HandleFunc("GET /api/admin/info", h.handleInfo)
	mux.HandleFunc("POST /api/admin/download", h.handleDownload)
	mux.HandleFunc("DELETE /api/admin/videos/{id}", h.handleDelete)
}

func (h *Handler) handleAdmin(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, templatesFS, "templates/admin.html")
}

func (h *Handler) handleInfo(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	if url == "" {
		h.jsonError(w, "url parameter is required", http.StatusBadRequest)
		return
	}

	video, err := h.download.GetInfo(url)
	if err != nil {
		h.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(video)
}

func (h *Handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.URL == "" {
		h.jsonError(w, "url is required", http.StatusBadRequest)
		return
	}

	// Get video info first
	video, err := h.download.GetInfo(req.URL)
	if err != nil {
		h.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if _, exists := h.store.Get(video.ID); exists {
		h.jsonError(w, "video already exists in library", http.StatusConflict)
		return
	}

	// Setup SSE stream
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.jsonError(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Helper to write an SSE line directly
	sendLine := func(msg string, typ string) {
		entry := map[string]string{"line": msg, "type": typ}
		data, _ := json.Marshal(entry)
		w.Write([]byte("data: " + string(data) + "\n\n"))
		flusher.Flush()
	}

	sendLine("Starting download...", "info")

	// Run download using yt-dlp directly
	videoDir := h.download.GetVideoDir()
	outputTemplate := filepath.Join(videoDir, "%(id)s.%(ext)s")

	args := []string{
		"-f", "bestvideo[ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]/best",
		"--merge-output-format", "mp4",
		"-o", outputTemplate,
		"--no-playlist",
		"--restrict-filenames",
		"--progress",
		req.URL,
	}

	sendLine("Running: yt-dlp "+strings.Join(args, " "), "info")

	cmd := exec.Command("yt-dlp", args...)

	// Get stdout and stderr pipes
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		sendLine("Failed to setup stdout pipe: "+err.Error(), "error")
		return
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		sendLine("Failed to setup stderr pipe: "+err.Error(), "error")
		return
	}

	if err := cmd.Start(); err != nil {
		sendLine("Failed to start yt-dlp: "+err.Error(), "error")
		return
	}

	// Collect output from stdout and stderr
	type lineEntry struct {
		line string
		typ  string
	}
	var mu sync.Mutex
	collected := make([]lineEntry, 0, 100)
	doneReading := make(chan struct{})
	doneStderr := make(chan struct{})

	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			l := strings.ReplaceAll(scanner.Text(), "\r", "")
			mu.Lock()
			collected = append(collected, lineEntry{line: l, typ: "info"})
			mu.Unlock()
		}
		close(doneReading)
	}()

	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			l := strings.ReplaceAll(scanner.Text(), "\r", "")
			mu.Lock()
			collected = append(collected, lineEntry{line: l, typ: "info"})
			mu.Unlock()
		}
		close(doneStderr)
	}()

	// Wait for command to finish
	cmdErr := cmd.Wait()

	// Wait for reading to finish
	<-doneReading
	<-doneStderr

	// Flush all collected output
	for _, entry := range collected {
		sendLine(entry.line, entry.typ)
	}

	if cmdErr != nil {
		sendLine("yt-dlp exited with error: "+cmdErr.Error(), "error")
		return
	}

	// Find the downloaded file
	files, err := filepath.Glob(filepath.Join(videoDir, "*"+video.ID+"*"))
	if err != nil {
		sendLine("Error searching for downloaded file: "+err.Error(), "error")
		return
	}

	// Filter to only video files
	var videoFiles []string
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" || ext == ".webm" || ext == ".mkv" || ext == ".avi" || ext == ".mov" || ext == ".flv" || ext == ".wmv" || ext == ".m4v" {
			videoFiles = append(videoFiles, f)
		}
	}

	if len(videoFiles) == 0 {
		sendLine("No video file found after download. Check the output above for errors.", "error")
		return
	}

	filePath := videoFiles[0]
	video.FilePath = filePath

	// Get file size
	info, err := os.Stat(filePath)
	if err != nil {
		sendLine("Error getting file info: "+err.Error(), "error")
		return
	}
	video.FileSize = info.Size()
	video.Format = strings.TrimPrefix(filepath.Ext(filePath), ".")
	video.DateAdded = time.Now()

	sendLine("Download complete: "+filePath, "info")
	sendLine("File size: "+formatFileSize(video.FileSize), "info")

	// Save to store
	if err := h.store.Add(*video); err != nil {
		sendLine("Failed to save video metadata: "+err.Error(), "error")
		os.Remove(filePath)
		return
	}

	sendLine("Video added to library successfully!", "done")
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	videoID := r.PathValue("id")
	video, exists := h.store.Get(videoID)
	if !exists {
		h.jsonError(w, "video not found", http.StatusNotFound)
		return
	}

	// Delete the file
	if err := h.download.DeleteFile(&video); err != nil {
		h.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := h.store.Delete(videoID); err != nil {
		h.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

func (h *Handler) jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func formatFileSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func init() {
	os.MkdirAll("videos", 0755)
}