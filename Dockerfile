# Build: static Go binary, no cgo (YOLO now runs as a remote HTTP service).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/detector ./cmd/detector

# Runtime: ffmpeg/ffprobe + a TTF font for burn-in captions.
FROM alpine:3.21
RUN apk add --no-cache ffmpeg font-dejavu ca-certificates tzdata && mkdir -p /data
WORKDIR /app
COPY --from=build /out/detector /app/detector
COPY config.docker.yaml /app/config.yaml
VOLUME ["/data"]
EXPOSE 8000
ENTRYPOINT ["/app/detector", "-config", "/app/config.yaml"]
