# Build stage
FROM golang:1.22-alpine AS builder
WORKDIR /src
RUN apk add --no-cache build-base git
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd universal-till && go build -trimpath -ldflags="-s -w" -o /out/edge ./cmd/edge

# Runtime stage
FROM alpine:3.20
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /out/edge /app/edge
COPY universal-till/web /app/web
COPY universal-till/LICENSE /app/
ENV UT_LISTEN_ADDR=:8080
EXPOSE 8080
VOLUME ["/app/data"]
USER app
CMD ["/app/edge"]
