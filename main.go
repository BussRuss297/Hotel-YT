package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"hotel/admin"
	"hotel/downloader"
	hotelpublic "hotel/public"
	"hotel/store"
)

func main() {
	port := flag.String("port", ":8080", "HTTP server port")
	dataDir := flag.String("data", "./data", "Data directory for metadata")
	videoDir := flag.String("videos", "./data", "Directory for downloaded videos")
	flag.Parse()

	// Ensure directories exist
	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}
	if err := os.MkdirAll(*videoDir, 0755); err != nil {
		log.Fatalf("Failed to create videos directory: %v", err)
	}

	// Initialize store
	storePath := filepath.Join(*dataDir, "videos.json")
	videoStore, err := store.NewVideoStore(storePath)
	if err != nil {
		log.Fatalf("Failed to initialize video store: %v", err)
	}

	// Initialize downloader
	download := downloader.NewDownloader(*videoDir)

	// Setup routes
	mux := http.NewServeMux()

	// Public routes
	publicHandler := hotelpublic.NewHandler(videoStore, download)
	publicHandler.RegisterRoutes(mux)

	// Admin routes
	adminHandler := admin.NewHandler(videoStore, download)
	adminHandler.RegisterRoutes(mux)

	// Serve static files
	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		http.StripPrefix("/static/", http.FileServer(http.Dir("static"))).ServeHTTP(w, r)
	})

	// Serve downloaded videos
	mux.HandleFunc("GET /videos/", func(w http.ResponseWriter, r *http.Request) {
		http.StripPrefix("/videos/", http.FileServer(http.Dir(*videoDir))).ServeHTTP(w, r)
	})

	// Start server
	addr := *port
	log.Printf("Server starting on http://localhost%s", addr)
	log.Printf("Data directory: %s", *dataDir)
	log.Printf("Videos directory: %s", *videoDir)

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func init() {
	// Print startup banner
	fmt.Println("=================================")
	fmt.Println("   Hotel Video Download Server")
	fmt.Println("=================================")
}