FROM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source
COPY . .

# Build with CGO enabled for SQLite
RUN CGO_ENABLED=1 go build -o whatsapp-bot .

# Production image
FROM alpine:latest

RUN apk add --no-cache sqlite-libs ca-certificates

WORKDIR /app

COPY --from=builder /app/whatsapp-bot .

EXPOSE 3000

CMD ["./whatsapp-bot"]
