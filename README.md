# Hotel — Video Download Server

A self-hosted web application for downloading and managing a personal video library. Built with Go, it uses `yt-dlp` to download videos and serves them through a clean web interface with an admin dashboard.

## Features

- **Public video library** — browse and watch downloaded videos with a searchable grid layout
- **Admin dashboard** — paste a URL, fetch video info, download with real-time console output
- **Video management** — view metadata (title, channel, duration, file size), delete videos
- **SSE streaming logs** — live progress output during downloads
- **View tracking** — counts how many times each video has been watched

## Prerequisites

- **Go** 1.22 or later (uses `net/http` enhanced routing with `{id}` path parameters)
- **yt-dlp** — required for downloading videos

### Installing yt-dlp

```bash
# macOS / Linux (Homebrew)
brew install yt-dlp

# Via pip
pip install yt-dlp

# apt (however this is an older version, check github for newest)
apt install yt-dlp

# Manual download
wget https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -O /usr/local/bin/yt-dlp
chmod +x /usr/local/bin/yt-dlp
```

Make sure `yt-dlp` is in your `$PATH` before starting the server.

## Build & Run

```bash
# Clone and enter the project
cd Hotel

# Build the binary
go build -o hotel-server .

# Run (defaults: port :8080, data ./data, videos ./videos)
./hotel-server

# Custom options
./hotel-server -port :3000 -data ./my-data -videos ./my-videos
```

### Flags

| Flag     | Default     | Description                   |
|----------|-------------|-------------------------------|
| `-port`  | `:8080`     | HTTP server listen address    |
| `-data`  | `./data`    | Directory for metadata store  |
| `-videos`| `./videos`  | Directory for video files     |

## Docker

Build and run with Docker — no Go toolchain or yt-dlp needed on the host:

```bash
# Build the image
docker build -t hotel-server -f Docker/Dockerfile .

# Run with volume mounts for persistent data and videos
docker run -p 8080:8080 \
  -v "$(pwd)/data:/app/data" \
  -v "$(pwd)/videos:/app/videos" \
  hotel-server
  
# or use pre-built image:
docker run --name hotel1 -p 8080:8080 \
  -v "$(pwd)/data:/app/data" \
  -v "$(pwd)/videos:/app/videos" \
  ghcr.io/bussruss297/hotel-yt:a9fc0eb
```

The image includes `yt-dlp` and `ffmpeg`, runs as a non-root user, and exposes port 8080.

## Usage

| Page            | URL       | Description                    |
|-----------------|-----------|--------------------------------|
| Public Library  | `/`       | Browse and watch videos       |
| Admin Dashboard | `/admin`  | Download and manage videos    |

### Downloading a Video

1. Navigate to `/admin`
2. Paste a YouTube URL and click **Fetch Info**
3. Review the video details, then click **Download**
4. Watch the console output for progress — the video appears in the library when complete

## Project Structure

```
Hotel/
├── main.go                  # Entry point, server setup
├── admin/
│   ├── admin.go             # Admin API handlers & SSE download
│   └── templates/
│       └── admin.html       # Admin dashboard UI (go:embed)
├── public/
│   ├── public.go            # Public API handlers
│   └── templates/
│       └── index.html       # Public video library UI (go:embed)
├── store/
│   └── store.go             # JSON-based video metadata store
├── downloader/
│   └── downloader.go        # yt-dlp integration wrapper
├── static/
│   ├── css/style.css        # Shared styles
│   └── js/app.js            # Shared JavaScript utilities
├── data/
│   └── videos.json          # Video metadata (auto-created)
├── videos/                  # Downloaded video files
├── go.mod
├── .gitignore
└── README.md
```

## Tech Stack

- **Backend:** Go (standard library — `net/http`, `embed`, `encoding/json`)
- **Downloader:** [yt-dlp](https://github.com/yt-dlp/yt-dlp)
- **Frontend:** Vanilla HTML/CSS/JS with Server-Sent Events for streaming logs