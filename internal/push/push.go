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

	log.Printf("[push/expo] sent to %s: %s", n.Token[:20]+"...", n.Title)
	return nil
}

// MultiSender routes to the correct sender based on token format
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
	// Expo tokens start with "ExponentPushToken["
	if len(n.Token) > 18 && n.Token[:18] == "ExponentPushToken[" {
		return m.Expo.Send(n)
	}

	// TODO: route to FCM or APNs based on platform
	log.Printf("[push] unsupported token format for platform %s: %s", n.Platform, n.Token[:20]+"...")
	return fmt.Errorf("unsupported token format, only Expo Push Tokens supported currently")
}
