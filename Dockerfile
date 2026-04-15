FROM golang:1.25-alpine AS builder
RUN apk add --no-cache gcc musl-dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o push-gateway ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/push-gateway /usr/local/bin/
VOLUME /data
EXPOSE 8080
ENV SQLITE_PATH=/data/push-gateway.db
CMD ["push-gateway"]
