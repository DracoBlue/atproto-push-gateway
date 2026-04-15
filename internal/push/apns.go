package push

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// APNsSender sends push notifications directly via Apple Push Notification service.
type APNsSender struct {
	keyID  string
	teamID string
	key    *ecdsa.PrivateKey
	client *http.Client
	topic  string // Bundle ID

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// NewAPNsSender creates a sender from a .p8 key file path.
func NewAPNsSender(keyPath, keyID, teamID, topic string) (*APNsSender, error) {
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read APNs key file: %w", err)
	}
	return newAPNsSenderFromBytes(keyData, keyID, teamID, topic)
}

// NewAPNsSenderFromBase64 creates a sender from PEM-encoded key bytes.
func NewAPNsSenderFromBytes(keyData []byte, keyID, teamID, topic string) (*APNsSender, error) {
	return newAPNsSenderFromBytes(keyData, keyID, teamID, topic)
}

func newAPNsSenderFromBytes(keyData []byte, keyID, teamID, topic string) (*APNsSender, error) {
	block, _ := pem.Decode(keyData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from APNs key")
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse APNs private key: %w", err)
	}

	ecKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("APNs key is not an ECDSA key")
	}

	return &APNsSender{
		keyID:  keyID,
		teamID: teamID,
		key:    ecKey,
		topic:  topic,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}, nil
}

// getToken returns a valid APNs JWT, refreshing if needed (tokens are valid for 1 hour).
func (a *APNsSender) getToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Reuse token if still valid (refresh 5 minutes before expiry)
	if a.token != "" && time.Now().Before(a.tokenExp.Add(-5*time.Minute)) {
		return a.token, nil
	}

	now := time.Now()
	claims := jwt.RegisteredClaims{
		IssuedAt: jwt.NewNumericDate(now),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = a.keyID
	token.Header["iss"] = a.teamID

	signedToken, err := token.SignedString(a.key)
	if err != nil {
		return "", fmt.Errorf("failed to sign APNs JWT: %w", err)
	}

	a.token = signedToken
	a.tokenExp = now.Add(1 * time.Hour)

	return a.token, nil
}

type apnsPayload struct {
	APS  apnsAPS           `json:"aps"`
	Data map[string]string  `json:"data,omitempty"`
}

type apnsAPS struct {
	Alert          apnsAlert `json:"alert"`
	Sound          string    `json:"sound,omitempty"`
	MutableContent int       `json:"mutable-content,omitempty"`
}

type apnsAlert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (a *APNsSender) Send(n Notification) error {
	token, err := a.getToken()
	if err != nil {
		return err
	}

	payload := apnsPayload{
		APS: apnsAPS{
			Alert: apnsAlert{
				Title: n.Title,
				Body:  n.Body,
			},
			Sound:          "default",
			MutableContent: 1,
		},
		Data: n.Data,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("https://api.push.apple.com/3/device/%s", n.Token)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("apns-topic", a.topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("APNs request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp struct {
			Reason string `json:"reason"`
		}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("APNs returned %d: %s", resp.StatusCode, errResp.Reason)
	}

	log.Printf("[push/apns] sent to %s: %s", truncateToken(n.Token, 20), n.Data["reason"])
	return nil
}
