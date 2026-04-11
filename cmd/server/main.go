package main

import (
	"log"
	"net/http"
	"os"

	"github.com/dracoblue/atproto-push-gateway/internal/jetstream"
	"github.com/dracoblue/atproto-push-gateway/internal/push"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
	"github.com/dracoblue/atproto-push-gateway/internal/xrpc"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	port := getEnv("PUSH_GATEWAY_PORT", "8080")
	serviceDID := getEnv("PUSH_GATEWAY_DID", "did:web:localhost")
	sqlitePath := getEnv("SQLITE_PATH", "./push-gateway.db")
	jetstreamURL := getEnv("JETSTREAM_URL", "wss://jetstream2.us-east.bsky.network/subscribe")
	expoPushToken := getEnv("EXPO_PUSH_ACCESS_TOKEN", "")
	devMode := getEnv("DEV_MODE", "") == "true"

	log.Printf("Starting atproto-push-gateway")
	log.Printf("  DID:       %s", serviceDID)
	log.Printf("  Port:      %s", port)
	log.Printf("  SQLite:    %s", sqlitePath)
	log.Printf("  Jetstream: %s", jetstreamURL)
	log.Printf("  Dev mode:  %v", devMode)

	// Initialize store
	s, err := store.New(sqlitePath)
	if err != nil {
		log.Fatalf("Failed to initialize store: %v", err)
	}
	defer s.Close()

	tokens, blocks, dids := s.GetStats()
	log.Printf("  Loaded: %d tokens, %d blocks, %d DIDs", tokens, blocks, dids)

	// Initialize push sender
	sender := push.NewMultiSender(expoPushToken)

	// Initialize Jetstream consumer
	consumer := jetstream.NewConsumer(jetstreamURL, s, sender)
	go consumer.Run()

	// Initialize HTTP server
	mux := http.NewServeMux()
	handler := xrpc.NewHandler(s, devMode)
	handler.RegisterRoutes(mux, serviceDID)

	log.Printf("  Listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
