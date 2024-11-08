# Build stage
FROM golang:1.23-alpine AS builder
WORKDIR /app

# Install build dependencies
RUN apk add --no-cache gcc musl-dev sqlite-dev

# Copy go mod and sum files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the application with platform specification
RUN go build -o go-book

# Run stage
FROM alpine:3.19
WORKDIR /app

# Install runtime dependencies
RUN apk add --no-cache sqlite-dev

# Copy everything from builder stage
COPY --from=builder /app .

# Expose port
EXPOSE 8080

# Run the binary
CMD ["./go-book"]
