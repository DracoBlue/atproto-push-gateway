package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// FCMSender sends push notifications directly via Firebase Cloud Messaging v1 API.
type FCMSender struct {
	projectID   string
	tokenSource oauth2.TokenSource
	client      *http.Client
}

// NewFCMSender creates a sender from a service account JSON file path.
func NewFCMSender(serviceAccountPath string) (*FCMSender, error) {
	data, err := os.ReadFile(serviceAccountPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read FCM service account file: %w", err)
	}
	return newFCMSenderFromBytes(data)
}

// NewFCMSenderFromBytes creates a sender from service account JSON bytes.
func NewFCMSenderFromBytes(data []byte) (*FCMSender, error) {
	return newFCMSenderFromBytes(data)
}

func newFCMSenderFromBytes(data []byte) (*FCMSender, error) {
	// Extract project_id from the service account JSON
	var sa struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(data, &sa); err != nil {
		return nil, fmt.Errorf("failed to parse FCM service account JSON: %w", err)
	}
	if sa.ProjectID == "" {
		return nil, fmt.Errorf("FCM service account JSON missing project_id")
	}

	creds, err := google.CredentialsFromJSON(
		context.Background(),
		data,
		"https://www.googleapis.com/auth/firebase.messaging",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create FCM credentials: %w", err)
	}

	return &FCMSender{
		projectID:   sa.ProjectID,
		tokenSource: creds.TokenSource,
		client:      &http.Client{},
	}, nil
}

type fcmRequest struct {
	Message fcmMessage `json:"message"`
}

type fcmMessage struct {
	Token        string            `json:"token"`
	Notification *fcmNotification  `json:"notification,omitempty"`
	Data         map[string]string `json:"data,omitempty"`
	Android      *fcmAndroid       `json:"android,omitempty"`
}

type fcmNotification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type fcmAndroid struct {
	Priority     string           `json:"priority"`
	Notification *fcmAndroidNotif `json:"notification,omitempty"`
}

type fcmAndroidNotif struct {
	ChannelID string `json:"channel_id,omitempty"`
}

func (f *FCMSender) Send(n Notification) error {
	token, err := f.tokenSource.Token()
	if err != nil {
		return fmt.Errorf("failed to get FCM OAuth2 token: %w", err)
	}

	// Use reason as Android notification channel
	channelID := n.Data["reason"]
	if channelID == "" {
		channelID = "default"
	}

	msg := fcmRequest{
		Message: fcmMessage{
			Token: n.Token,
			Notification: &fcmNotification{
				Title: n.Title,
				Body:  n.Body,
			},
			Data: n.Data,
			Android: &fcmAndroid{
				Priority: "high",
				Notification: &fcmAndroidNotif{
					ChannelID: channelID,
				},
			},
		},
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", f.projectID)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("FCM request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp struct {
			Error struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("FCM returned %d: %s (%s)", resp.StatusCode, errResp.Error.Status, errResp.Error.Message)
	}

	log.Printf("[push/fcm] sent to %s: %s", truncateToken(n.Token, 20), n.Data["reason"])
	return nil
}
