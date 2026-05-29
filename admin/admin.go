package admin

import (
	"bufio"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	thumbDir string
}

func NewHandler(s *store.VideoStore, d *downloader.Downloader, dataDir string) *Handler {
	return &Handler{
		store:    s,
		download: d,
		thumbDir: filepath.Join(dataDir, "thumbnails"),
	}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", h.handleAdmin)
	mux.HandleFunc("GET /api/admin/info", h.handleInfo)
	mux.HandleFunc("POST /api/admin/download", h.handleDownload)
	mux.HandleFunc("DELETE /api/admin/videos/{id}", h.handleDelete)
	mux.HandleFunc("POST /api/admin/upload", h.handleUpload)
}

func (h *Handler) handleAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	http.ServeFileFS(w, r, templatesFS, "templates/admin.html")
}

func (h *Handler) handleInfo(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	if url == "" {
		h.jsonError(w, "url parameter is required", http.StatusBadRequest)
		return
	}

	info, err := h.download.GetInfoWithCodecs(url)
	if err != nil {
		h.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func (h *Handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL        string `json:"url"`
		Resolution int    `json:"resolution"`
		Vcodec     string `json:"vcodec"`
		Acodec     string `json:"acodec"`
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

	// Build format selector from user-selected params
	formatSelector := buildFormatSelector(req.Resolution, req.Vcodec, req.Acodec)
	sendLine("Format selector: "+formatSelector, "info")

	args := []string{
		"-f", formatSelector,
		"--merge-output-format", "mp4",
		"--embed-thumbnail",
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

	// Download and save thumbnail locally
	if video.Thumbnail != "" {
		sendLine("Downloading thumbnail...", "info")
		thumbPath, err := downloadThumbnail(video.ID, video.Thumbnail, h.thumbDir)
		if err != nil {
			sendLine("Thumbnail download failed (non-fatal): "+err.Error(), "info")
		} else {
			video.Thumbnail = thumbPath
			sendLine("Thumbnail saved: "+thumbPath, "info")
		}
	}

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

	// Delete thumbnail if local
	if strings.HasPrefix(video.Thumbnail, "/thumbnails/") {
		thumbFile := filepath.Join(h.thumbDir, videoID+".jpg")
		os.Remove(thumbFile)
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

func (h *Handler) handleUpload(w http.ResponseWriter, r *http.Request) {
	// Limit upload size to 10GB
	r.Body = http.MaxBytesReader(w, r.Body, 10<<30)

	if err := r.ParseMultipartForm(100 << 20); err != nil {
		h.jsonError(w, "file too large or invalid form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		h.jsonError(w, "missing file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	title := strings.TrimSpace(r.FormValue("title"))
	channel := strings.TrimSpace(r.FormValue("channel"))

	// Auto-derive title from filename if not provided
	if title == "" {
		title = filenameToTitle(header.Filename)
	}
	if channel == "" {
		h.jsonError(w, "channel is required", http.StatusBadRequest)
		return
	}

	// Generate a unique ID
	idBytes := make([]byte, 8)
	rand.Read(idBytes)
	id := hex.EncodeToString(idBytes)

	// Get file extension
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext == "" {
		ext = ".mp4"
	}

	videoDir := h.download.GetVideoDir()
	savePath := filepath.Join(videoDir, id+ext)

	// Create destination file
	dst, err := os.Create(savePath)
	if err != nil {
		h.jsonError(w, "failed to create file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, file)
	if err != nil {
		os.Remove(savePath)
		h.jsonError(w, "failed to write file", http.StatusInternalServerError)
		return
	}
	dst.Close()

	// Get duration via ffprobe
	duration := 0
	if dur, err := probeDuration(savePath); err == nil {
		duration = dur
	}

	// Generate thumbnail via ffmpeg
	thumbPath := ""
	if err := generateThumbnail(savePath, h.thumbDir, id); err == nil {
		thumbPath = "/thumbnails/" + id + ".jpg"
	}

	video := store.Video{
		ID:        id,
		Title:     title,
		Channel:   channel,
		FilePath:  savePath,
		Duration:  duration,
		FileSize:  written,
		Format:    strings.TrimPrefix(ext, "."),
		DateAdded: time.Now(),
		ViewCount: 0,
		Thumbnail: thumbPath,
	}

	if err := h.store.Add(video); err != nil {
		os.Remove(savePath)
		h.jsonError(w, "failed to save video metadata", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(video)
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

// buildFormatSelector constructs a yt-dlp -f selector from user params.
// resolution=0 means highest; >0 caps video height to that value.
func buildFormatSelector(resolution int, vcodec, acodec string) string {
	// Build the height constraint
	heightFilter := ""
	if resolution > 0 {
		heightFilter = fmt.Sprintf("[height<=%d]", resolution)
	}

	vcFilter := ""
	if vcodec != "" {
		vcFilter = "[codec^=" + vcodec + "]"
	}
	acFilter := ""
	if acodec != "" {
		acFilter = "[codec^=" + acodec + "]"
	}

	// Build selectors preferring merged bestvideo+bestaudio over pre-muxed formats
	// Fallback chain: ideal codecs > bestvideo with codec + any audio > any video + bestaudio with codec > best merged
	parts := []string{}
	if vcFilter != "" || acFilter != "" {
		// 1st: exact codecs match via merging
		parts = append(parts, "bestvideo"+heightFilter+vcFilter+"+bestaudio"+acFilter)
		// 2nd: video codec match + any best audio
		if vcFilter != "" {
			parts = append(parts, "bestvideo"+heightFilter+vcFilter+"+bestaudio")
		}
		// 3rd: any best video + audio codec match
		if acFilter != "" {
			parts = append(parts, "bestvideo"+heightFilter+"+bestaudio"+acFilter)
		}
	}
	// Final fallback: best merged video+audio
	parts = append(parts, "bestvideo"+heightFilter+"+bestaudio/best"+heightFilter)

	return strings.Join(parts, "/")
}

// downloadThumbnail fetches a thumbnail image from url and saves it to the thumbnails directory.
// Returns the local thumbnail path (e.g. "/thumbnails/videoID.jpg") or an error.
func downloadThumbnail(videoID, thumbURL, thumbDir string) (string, error) {
	resp, err := http.Get(thumbURL)
	if err != nil {
		return "", fmt.Errorf("fetching thumbnail: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("thumbnail HTTP %d", resp.StatusCode)
	}

	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		return "", fmt.Errorf("creating thumbnails dir: %w", err)
	}

	thumbPath := filepath.Join(thumbDir, videoID+".jpg")
	f, err := os.Create(thumbPath)
	if err != nil {
		return "", fmt.Errorf("creating thumbnail file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(thumbPath)
		return "", fmt.Errorf("writing thumbnail: %w", err)
	}

	return "/thumbnails/" + videoID + ".jpg", nil
}

// filenameToTitle derives a title from a filename by stripping extension
// and replacing underscores/hyphens with spaces.
func filenameToTitle(filename string) string {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	base = strings.ReplaceAll(base, "_", " ")
	base = strings.ReplaceAll(base, "-", " ")
	return strings.TrimSpace(base)
}

// probeDuration uses ffprobe to get the video duration in seconds.
func probeDuration(filePath string) (int, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "csv=p=0",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}
	durStr := strings.TrimSpace(string(out))
	if durStr == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var dur float64
	if _, err := fmt.Sscanf(durStr, "%f", &dur); err != nil {
		return 0, fmt.Errorf("parsing duration: %w", err)
	}
	return int(dur), nil
}

// generateThumbnail extracts a frame from the video at 5 seconds using ffmpeg.
func generateThumbnail(videoPath, thumbDir, videoID string) error {
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		return fmt.Errorf("creating thumbnails dir: %w", err)
	}

	thumbPath := filepath.Join(thumbDir, videoID+".jpg")
	cmd := exec.Command("ffmpeg",
		"-y",
		"-i", videoPath,
		"-vframes", "1",
		"-ss", "00:00:05",
		"-q:v", "2",
		"-update", "1",
		thumbPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %w, output: %s", err, string(out))
	}
	return nil
}

func init() {
	os.MkdirAll("videos", 0755)
}
