package main

import (
	"context"
	"encoding/base64"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dracoblue/atproto-push-gateway/internal/jetstream"
	"github.com/dracoblue/atproto-push-gateway/internal/profile"
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

	// APNs direct delivery (optional)
	apnsKeyPath := getEnv("APNS_KEY_PATH", "")
	apnsKeyBase64 := getEnv("APNS_KEY_BASE64", "")
	apnsKeyID := getEnv("APNS_KEY_ID", "")
	apnsTeamID := getEnv("APNS_TEAM_ID", "")
	apnsTopic := getEnv("APNS_TOPIC", "")
	apnsSandbox := getEnv("APNS_SANDBOX", "") == "true"

	// FCM direct delivery (optional)
	fcmServiceAccountPath := getEnv("FCM_SERVICE_ACCOUNT_PATH", "")
	fcmServiceAccountBase64 := getEnv("FCM_SERVICE_ACCOUNT_BASE64", "")

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

	// Configure direct APNs if key is available (file path or base64)
	if apnsKeyID != "" && apnsTeamID != "" && apnsTopic != "" {
		var apnsSender *push.APNsSender
		var err error

		if apnsKeyBase64 != "" {
			// Try standard base64 first, then raw (no padding)
			keyData, decErr := base64.StdEncoding.DecodeString(apnsKeyBase64)
			if decErr != nil {
				keyData, decErr = base64.RawStdEncoding.DecodeString(apnsKeyBase64)
				if decErr != nil {
					log.Fatalf("Failed to decode APNS_KEY_BASE64: %v", decErr)
				}
			}
			apnsSender, err = push.NewAPNsSenderFromBytes(keyData, apnsKeyID, apnsTeamID, apnsTopic, apnsSandbox)
		} else if apnsKeyPath != "" {
			apnsSender, err = push.NewAPNsSender(apnsKeyPath, apnsKeyID, apnsTeamID, apnsTopic, apnsSandbox)
		}

		if err != nil {
			log.Fatalf("Failed to initialize APNs sender: %v", err)
		}
		if apnsSender != nil {
			sender.APNs = apnsSender
			env := "production"
		if apnsSandbox {
			env = "sandbox"
		}
		log.Printf("  APNs:      enabled (key=%s, team=%s, topic=%s, env=%s)", apnsKeyID, apnsTeamID, apnsTopic, env)
		} else {
			log.Printf("  APNs:      disabled (no key configured)")
		}
	} else {
		log.Printf("  APNs:      disabled (using Expo for iOS)")
	}

	// Configure direct FCM if service account is available
	if fcmServiceAccountBase64 != "" || fcmServiceAccountPath != "" {
		var fcmSender *push.FCMSender
		var err error

		if fcmServiceAccountBase64 != "" {
			saData, decErr := base64.StdEncoding.DecodeString(fcmServiceAccountBase64)
			if decErr != nil {
				saData, decErr = base64.RawStdEncoding.DecodeString(fcmServiceAccountBase64)
				if decErr != nil {
					log.Fatalf("Failed to decode FCM_SERVICE_ACCOUNT_BASE64: %v", decErr)
				}
			}
			fcmSender, err = push.NewFCMSenderFromBytes(saData)
		} else {
			fcmSender, err = push.NewFCMSender(fcmServiceAccountPath)
		}

		if err != nil {
			log.Fatalf("Failed to initialize FCM sender: %v", err)
		}
		sender.FCM = fcmSender
		log.Printf("  FCM:       enabled")
	} else {
		log.Printf("  FCM:       disabled (using Expo for Android)")
	}

	// Initialize profile resolver for display names
	profileResolver := profile.NewResolver()

	// Initialize Jetstream consumer
	consumer := jetstream.NewConsumer(jetstreamURL, s, sender, profileResolver)
	go consumer.Run()

	// Initialize HTTP server
	mux := http.NewServeMux()
	handler := xrpc.NewHandler(s, devMode, serviceDID, func() interface{} { return consumer.GetStats() }, consumer.NotifyTokenRegistered)
	handler.RegisterRoutes(mux, serviceDID)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16, // 64 KiB
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
