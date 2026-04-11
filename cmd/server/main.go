package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	tokens, blocks, dids := s.GetStats()
	log.Printf("  Loaded: %d tokens, %d blocks, %d DIDs", tokens, blocks, dids)

	// Initialize push sender
	sender := push.NewMultiSender(expoPushToken)

	// Initialize Jetstream consumer
	consumer := jetstream.NewConsumer(jetstreamURL, s, sender)
	go consumer.Run()

	// Initialize HTTP server
	mux := http.NewServeMux()
	handler := xrpc.NewHandler(s, devMode, func() interface{} { return consumer.GetStats() })
	handler.RegisterRoutes(mux, serviceDID)

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	// Start HTTP server in a goroutine
	go func() {
		log.Printf("  Listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("Received signal %v, shutting down...", sig)

	// Stop Jetstream consumer
	consumer.Stop()

	// Gracefully shutdown HTTP server with a 10-second timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	// Close SQLite database
	if err := s.Close(); err != nil {
		log.Printf("Store close error: %v", err)
	}

	log.Println("Shutdown complete")
}
