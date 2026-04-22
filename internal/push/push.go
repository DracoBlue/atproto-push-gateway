package push

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type Notification struct {
	Token    string
	Platform string
	Title    string
	Body     string
	Data     map[string]string
}

type Sender interface {
	Send(n Notification) error
}

// ExpoPushSender sends via Expo Push API
type ExpoPushSender struct {
	AccessToken string
	Client      *http.Client
}

type expoMessage struct {
	To             string            `json:"to"`
	Title          string            `json:"title"`
	Body           string            `json:"body"`
	Data           map[string]string `json:"data,omitempty"`
	Sound          string            `json:"sound,omitempty"`
	MutableContent bool              `json:"mutableContent,omitempty"`
}

func NewExpoPushSender(accessToken string) *ExpoPushSender {
	return &ExpoPushSender{
		AccessToken: accessToken,
		Client:      &http.Client{Timeout: 10 * time.Second},
	}
}

// truncateToken safely truncates a token for logging, avoiding panics on short tokens.
func truncateToken(token string, maxLen int) string {
	if len(token) <= maxLen {
		return token
	}
	return token[:maxLen] + "..."
}

func (e *ExpoPushSender) Send(n Notification) error {
	// Title and body provide a readable English default. Clients with a
	// Notification Service Extension (iOS) or background handler (Android)
	// can override these with localized text using the data fields.
	// mutableContent:true tells iOS to invoke the NSE before display.
	msg := expoMessage{
		To:             n.Token,
		Title:          n.Title,
		Body:           n.Body,
		Data:           n.Data,
		Sound:          "default",
		MutableContent: true,
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", "https://exp.host/--/api/v2/push/send", bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	if e.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+e.AccessToken)
	}

	resp, err := e.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("expo push API returned %d", resp.StatusCode)
	}

	log.Printf("[push/expo] sent to %s: %s", truncateToken(n.Token, 20), n.Data["reason"])
	return nil
}

// MultiSender routes to the correct sender based on platform and token format.
type MultiSender struct {
	Expo *ExpoPushSender
	APNs *APNsSender // nil if not configured
	FCM  *FCMSender  // nil if not configured
}

func NewMultiSender(expoToken string) *MultiSender {
	return &MultiSender{
		Expo: NewExpoPushSender(expoToken),
	}
}

// isExpoToken returns true if the token looks like an Expo Push Token.
func isExpoToken(token string) bool {
	return strings.HasPrefix(token, "ExponentPushToken[")
}

func (m *MultiSender) Send(n Notification) error {
	switch n.Platform {
	case "ios":
		if isExpoToken(n.Token) {
			log.Printf("[push] routing iOS to Expo")
			return m.Expo.Send(n)
		}
		if m.APNs != nil {
			log.Printf("[push] routing iOS to APNs")
			return m.APNs.Send(n)
		}
		// No APNs configured, can't send native token via Expo
		log.Printf("[push] skipping iOS native token (APNs not configured)")
		return nil
	case "android":
		if isExpoToken(n.Token) {
			log.Printf("[push] routing Android to Expo")
			return m.Expo.Send(n)
		}
		if m.FCM != nil {
			log.Printf("[push] routing Android to FCM")
			return m.FCM.Send(n)
		}
		log.Printf("[push] skipping Android native token (FCM not configured)")
		return nil
	case "web":
		log.Printf("[push] web push not yet supported, token: %s", truncateToken(n.Token, 20))
		return fmt.Errorf("web push not yet supported")
	default:
		log.Printf("[push] unsupported platform %q, token: %s", n.Platform, truncateToken(n.Token, 20))
		return fmt.Errorf("unsupported platform: %s", n.Platform)
	}
}
