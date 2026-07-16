# syntax=docker/dockerfile:1
FROM golang:1.26.5-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/cabot-cup ./cmd/cabot

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build --chown=nonroot:nonroot /out/cabot-cup /app/cabot-cup

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/cabot-cup"]
