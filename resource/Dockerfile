# syntax=docker/dockerfile:1.7

FROM golang:1.25-alpine AS build
WORKDIR /src

# Optional: ca-certs for TLS during go mod download
RUN apk add --no-cache ca-certificates

# Cache deps first
COPY go.mod go.sum .
WORKDIR /src

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source and build
COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/resource .

# Final runtime: distroless static (nonroot)
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/resource /resource
USER nonroot:nonroot

ENTRYPOINT ["/resource"]
