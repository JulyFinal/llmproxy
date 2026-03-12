# Build stage
FROM golang:1.22-alpine AS builder

# Install gcc and musl-dev for CGO (required by mattn/go-sqlite3)
RUN apk add --no-cache gcc musl-dev

WORKDIR /app

# Download and cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the proxyllm binary with CGO enabled
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-w -s" -o proxyllm ./cmd/proxyllm

# Final stage
FROM alpine:latest

# Install necessary runtime packages (tzdata for timezones, ca-certificates for TLS)
RUN apk add --no-cache tzdata ca-certificates

WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/proxyllm .

# Create a data directory for SQLite and config
RUN mkdir -p /app/data

# Default environment variables
ENV PROXYLLM_DB_PATH=/app/data/proxyllm.db
ENV PROXYLLM_SERVER_ADDR=:8080

EXPOSE 8080

ENTRYPOINT ["./proxyllm"]
