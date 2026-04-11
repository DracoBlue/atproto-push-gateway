package push

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	To    string            `json:"to"`
	Title string            `json:"title"`
	Body  string            `json:"body"`
	Data  map[string]string `json:"data,omitempty"`
	Sound string            `json:"sound,omitempty"`
}

func NewExpoPushSender(accessToken string) *ExpoPushSender {
	return &ExpoPushSender{
		AccessToken: accessToken,
		Client:      &http.Client{},
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
	msg := expoMessage{
		To:    n.Token,
		Title: n.Title,
		Body:  n.Body,
		Data:  n.Data,
		Sound: "default",
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

	log.Printf("[push/expo] sent to %s: %s", truncateToken(n.Token, 20), n.Title)
	return nil
}

// MultiSender routes to the correct sender based on platform
type MultiSender struct {
	Expo *ExpoPushSender
	// FCM  *FCMSender  // TODO
	// APNs *APNsSender // TODO
}

func NewMultiSender(expoToken string) *MultiSender {
	return &MultiSender{
		Expo: NewExpoPushSender(expoToken),
	}
}

func (m *MultiSender) Send(n Notification) error {
	switch n.Platform {
	case "ios", "android":
		// Currently all mobile push is routed through Expo
		return m.Expo.Send(n)
	case "web":
		// TODO: implement web push (Web Push API / VAPID)
		log.Printf("[push] web push not yet supported, token: %s", truncateToken(n.Token, 20))
		return fmt.Errorf("web push not yet supported")
	default:
		log.Printf("[push] unsupported platform %q, token: %s", n.Platform, truncateToken(n.Token, 20))
		return fmt.Errorf("unsupported platform: %s", n.Platform)
	}
}
