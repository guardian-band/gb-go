# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Cache downloaded modules unless go.mod or go.sum changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gb-go .

# Runtime stage
FROM alpine:3.22

RUN adduser -D -H -u 10001 appuser

WORKDIR /app
COPY --from=builder /out/gb-go ./gb-go

USER appuser
EXPOSE 3000

ENTRYPOINT ["/app/gb-go"]
