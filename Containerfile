# Build Stage
FROM golang:1.24-alpine AS builder

# Install build dependencies (GCC for CGO/SQLite)
RUN apk add --no-cache build-base

WORKDIR /app

# Build the application
COPY . .
# -ldflags="-w -s" strips debug information for smaller binary
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-w -s" -o hogs .

# Runtime Stage
FROM alpine:latest

# Install runtime dependencies (CA certs for HTTPS)
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/hogs .

# Create data directory
RUN mkdir -p data/game

# Expose port
EXPOSE 8080

# Environment variables (Defaults, override in Quadlet/Docker)
ENV PORT=8080
ENV DB_PATH=/data/hogs.db
ENV GAME_DATA_PATH=/app/data/game

# Mount point for data persistence
VOLUME ["/data", "/app/data/game"]

# Run
CMD ["./hogs"]
