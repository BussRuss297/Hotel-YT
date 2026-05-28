package downloader

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"hotel/store"
)

type Downloader struct {
	videoDir string
}

func NewDownloader(videoDir string) *Downloader {
	return &Downloader{videoDir: videoDir}
}

type ytDlpInfo struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Channel     string        `json:"channel"`
	Uploader    string        `json:"uploader"`
	Thumbnail   string        `json:"thumbnail"`
	Duration    int           `json:"duration"`
	Format      string        `json:"format"`
	FilePath    string        `json:"filepath"`
	FileName    string        `json:"filename"`
	Formats     []ytDlpFormat `json:"formats"`
}

type ytDlpFormat struct {
	FormatID   string `json:"format_id"`
	Vcodec     string `json:"vcodec"`
	Acodec     string `json:"acodec"`
	Resolution string `json:"resolution"`
	Height     int    `json:"height"`
}

// VideoInfo wraps a Video with available codec/resolution info for the admin UI.
type VideoInfo struct {
	*store.Video
	VcodecOptions []CodecOption `json:"vcodec_options"`
	AcodecOptions []CodecOption `json:"acodec_options"`
}

type CodecOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// codecFamily maps yt-dlp vcodec/acodec prefix to display info.
var vcodecFamilies = []struct {
	prefix string
	value  string
	label  string
}{
	{"avc1", "avc1", "H.264"},
	{"vp9", "vp9", "VP9"},
	{"av01", "av01", "AV1"},
	{"hvc1", "hvc1|hev1", "H.265"},
	{"hev1", "hvc1|hev1", "H.265"},
}

var acodecFamilies = []struct {
	prefix string
	value  string
	label  string
}{
	{"mp4a", "mp4a", "AAC"},
	{"opus", "opus", "Opus"},
	{"vorbis", "vorbis", "Vorbis"},
	{"mp3", "mp3", "MP3"},
}

func (d *Downloader) GetInfo(url string) (*store.Video, error) {
	info, err := d.fetchInfo(url)
	if err != nil {
		return nil, err
	}

	channel := info.Channel
	if channel == "" {
		channel = info.Uploader
	}

	video := &store.Video{
		ID:        info.ID,
		Title:     info.Title,
		Channel:   channel,
		URL:       url,
		Thumbnail: info.Thumbnail,
		Duration:  info.Duration,
		DateAdded: time.Now(),
	}

	return video, nil
}

func (d *Downloader) GetInfoWithCodecs(url string) (*VideoInfo, error) {
	info, err := d.fetchInfo(url)
	if err != nil {
		return nil, err
	}

	channel := info.Channel
	if channel == "" {
		channel = info.Uploader
	}

	video := &store.Video{
		ID:        info.ID,
		Title:     info.Title,
		Channel:   channel,
		URL:       url,
		Thumbnail: info.Thumbnail,
		Duration:  info.Duration,
		DateAdded: time.Now(),
	}

	return &VideoInfo{
		Video:         video,
		VcodecOptions: extractCodecOptions(info.Formats, true),
		AcodecOptions: extractCodecOptions(info.Formats, false),
	}, nil
}

func (d *Downloader) fetchInfo(url string) (*ytDlpInfo, error) {
	cmd := exec.Command("yt-dlp", "--dump-json", "--no-warnings", url)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("yt-dlp info failed: %w, output: %s", err, string(output))
	}

	var jsonLine string
	for _, line := range strings.Split(string(output), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "{") {
			jsonLine = trimmed
			break
		}
	}
	if jsonLine == "" {
		return nil, fmt.Errorf("no JSON output from yt-dlp, raw output: %s", string(output))
	}

	var info ytDlpInfo
	if err := json.Unmarshal([]byte(jsonLine), &info); err != nil {
		return nil, fmt.Errorf("parsing yt-dlp info: %w, json line: %s", err, jsonLine)
	}

	return &info, nil
}

func extractCodecOptions(formats []ytDlpFormat, isVideo bool) []CodecOption {
	seen := make(map[string]bool)
	var options []CodecOption
	families := acodecFamilies
	if isVideo {
		families = vcodecFamilies
	}

	for _, f := range formats {
		codecField := f.Acodec
		if isVideo {
			codecField = f.Vcodec
		}
		if codecField == "" || codecField == "none" {
			continue
		}
		// Skip mhtml / storyboard formats
		if strings.HasPrefix(f.FormatID, "sb") {
			continue
		}
		// For video, skip audio-only and muxed formats where vcodec is unset
		if isVideo && f.Height == 0 {
			continue
		}

		for _, family := range families {
			if strings.HasPrefix(codecField, family.prefix) {
				if !seen[family.value] {
					seen[family.value] = true
					options = append(options, CodecOption{Value: family.value, Label: family.label})
				}
				break
			}
		}
	}
	return options
}

func (d *Downloader) Download(url string, video *store.Video) error {
	// Ensure video directory exists
	if err := os.MkdirAll(d.videoDir, 0755); err != nil {
		return fmt.Errorf("creating video dir: %w", err)
	}

	outputTemplate := filepath.Join(d.videoDir, "%(id)s.%(ext)s")

	args := []string{
		"-f", "bestvideo[ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]/best",
		"--merge-output-format", "mp4",
		"-o", outputTemplate,
		"--no-playlist",
		"--restrict-filenames",
		url,
	}

	cmd := exec.Command("yt-dlp", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("yt-dlp download failed: %w, output: %s", err, string(output))
	}

	// Find the downloaded file
	files, err := filepath.Glob(filepath.Join(d.videoDir, fmt.Sprintf("%s.*", video.ID)))
	if err != nil {
		return fmt.Errorf("glob failed: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no video file found after download")
	}

	filePath := files[0]
	video.FilePath = filePath

	// Get file size
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}
	video.FileSize = info.Size()

	// Determine extension
	video.Format = strings.TrimPrefix(filepath.Ext(filePath), ".")

	return nil
}

func (d *Downloader) GetVideoDir() string {
	return d.videoDir
}

func (d *Downloader) DeleteFile(video *store.Video) error {
	if video.FilePath == "" {
		return nil
	}
	if err := os.Remove(video.FilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting file: %w", err)
	}
	return nil
}

func formatDuration(seconds int) string {
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	return fmt.Sprintf("%dm %ds", m, s)
}

func FormatFileSize(bytes int64) string {
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

// Make helper functions available externally
var (
	FormatDur      = formatDuration
)