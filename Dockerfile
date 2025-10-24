# dockerfile: Dockerfile
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY main.go ./

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o pg-migrator main.go

# Final stage - use PostgreSQL image for pg_dump/pg_restore tools
FROM postgres:16-alpine

# Copy the binary from builder
COPY --from=builder /app/pg-migrator /usr/local/bin/pg-migrator
RUN chmod +x /usr/local/bin/pg-migrator

# Set working directory
WORKDIR /tmp

# Run as non-root user (postgres user is already in the image)
USER postgres

ENTRYPOINT ["/usr/local/bin/pg-migrator"]
