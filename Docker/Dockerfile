# Stage 1: Build the Go binary
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app
COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o hotel-server .

# Stage 2: Runtime with yt-dlp
FROM python:3.12-alpine

# Install yt-dlp and ffmpeg (needed for video/audio merging)
RUN apk add --no-cache ffmpeg && \
    pip install --no-cache-dir yt-dlp

WORKDIR /app

# Copy the Go binary
COPY --from=builder /app/hotel-server .

# Create data directory and set ownership
RUN mkdir -p /app/data && chown -R hotel:hotel /app

# Copy static assets and templates (served from embed, already in binary, 
# but keeping for clarity and potential future use)
COPY --from=builder /app/static ./static

EXPOSE 8080

VOLUME ["/app/data"]

ENTRYPOINT ["./hotel-server"]
CMD ["-port", ":8080", "-data", "./data", "-videos", "./data"]