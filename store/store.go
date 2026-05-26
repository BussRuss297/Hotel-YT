package store

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

type Video struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Channel     string    `json:"channel"`
	URL         string    `json:"url"`
	Thumbnail   string    `json:"thumbnail"`
	FilePath    string    `json:"file_path"`
	Duration    int       `json:"duration"`
	FileSize    int64     `json:"file_size"`
	Format      string    `json:"format"`
	DateAdded   time.Time `json:"date_added"`
	ViewCount   int       `json:"view_count"`
}

type VideoStore struct {
	mu      sync.RWMutex
	videos  map[string]Video
	filePath string
}

func NewVideoStore(filePath string) (*VideoStore, error) {
	s := &VideoStore{
		videos:   make(map[string]Video),
		filePath: filePath,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *VideoStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading store: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	var videos []Video
	if err := json.Unmarshal(data, &videos); err != nil {
		return fmt.Errorf("parsing store: %w", err)
	}
	for _, v := range videos {
		s.videos[v.ID] = v
	}
	return nil
}

func (s *VideoStore) save() error {
	// Caller must hold the lock
	var videos []Video
	for _, v := range s.videos {
		videos = append(videos, v)
	}
	// Sort by date added descending
	for i := 0; i < len(videos); i++ {
		for j := i + 1; j < len(videos); j++ {
			if videos[j].DateAdded.After(videos[i].DateAdded) {
				videos[i], videos[j] = videos[j], videos[i]
			}
		}
	}

	data, err := json.MarshalIndent(videos, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling store: %w", err)
	}
	tmpPath := s.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("writing store tmp: %w", err)
	}
	return os.Rename(tmpPath, s.filePath)
}

func (s *VideoStore) Add(v Video) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.videos[v.ID] = v
	return s.save()
}

func (s *VideoStore) Get(id string) (Video, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.videos[id]
	return v, ok
}

func (s *VideoStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.videos, id)
	return s.save()
}

func (s *VideoStore) GetAll() []Video {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var videos []Video
	for _, v := range s.videos {
		videos = append(videos, v)
	}
	for i := 0; i < len(videos); i++ {
		for j := i + 1; j < len(videos); j++ {
			if videos[j].DateAdded.After(videos[i].DateAdded) {
				videos[i], videos[j] = videos[j], videos[i]
			}
		}
	}
	return videos
}

func (s *VideoStore) Search(query string) []Video {
	s.mu.RLock()
	defer s.mu.RUnlock()
	queryLower := strings.ToLower(query)
	var results []Video
	for _, v := range s.videos {
		if containsLower(v.Title, queryLower) || containsLower(v.Channel, queryLower) || containsLower(v.URL, queryLower) {
			results = append(results, v)
		}
	}
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].DateAdded.After(results[i].DateAdded) {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
	return results
}

func (s *VideoStore) IncrementView(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.videos[id]; ok {
		v.ViewCount++
		s.videos[id] = v
	}
	// Save outside the lock to avoid deadlock
	// Actually save inside to be safe - just don't re-lock
	_ = s.save()
}

func containsLower(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
