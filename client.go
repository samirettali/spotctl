package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const apiBaseURL = "https://api.spotify.com/v1"

type APIError struct {
	Status  int
	Message string
	Details any
}

func (err *APIError) Error() string {
	return fmt.Sprintf("Spotify API returned %d: %s", err.Status, err.Message)
}

type spotifyClient struct {
	httpClient *http.Client
	creds      credentials
}

func newSpotifyClient() (*spotifyClient, error) {
	creds, err := loadCredentials()
	if err != nil {
		return nil, fmt.Errorf("load credentials (run 'spotctl auth login' first): %w", err)
	}
	client := &spotifyClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		creds:      creds,
	}
	if time.Now().Add(time.Minute).After(creds.ExpiresAt) {
		if err := client.refresh(); err != nil {
			return nil, err
		}
	}
	return client, nil
}

func (client *spotifyClient) refresh() error {
	if client.creds.RefreshToken == "" {
		return fmt.Errorf("refresh token is missing; run 'spotctl auth login' again")
	}
	token, err := exchangeToken(url.Values{
		"client_id":     {client.creds.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {client.creds.RefreshToken},
	})
	if err != nil {
		return fmt.Errorf("refresh access token: %w", err)
	}
	client.creds.AccessToken = token.AccessToken
	client.creds.TokenType = token.TokenType
	client.creds.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	if token.RefreshToken != "" {
		client.creds.RefreshToken = token.RefreshToken
	}
	if token.Scope != "" {
		client.creds.Scope = token.Scope
	}
	return saveCredentials(client.creds)
}

func (client *spotifyClient) request(method, path string, query url.Values, body any) (json.RawMessage, error) {
	var requestBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		requestBody = bytes.NewReader(data)
	}

	target := apiBaseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequest(method, target, requestBody)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+client.creds.AccessToken)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call Spotify API: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read Spotify response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, decodeAPIError(response.StatusCode, data)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return json.RawMessage(`{"ok":true}`), nil
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("Spotify returned invalid JSON")
	}
	return json.RawMessage(data), nil
}

func decodeAPIError(status int, data []byte) error {
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return &APIError{Status: status, Message: http.StatusText(status), Details: string(data)}
	}

	message := http.StatusText(status)
	if object, ok := payload.(map[string]any); ok {
		if text, ok := object["error_description"].(string); ok {
			message = text
		}
		if apiObject, ok := object["error"].(map[string]any); ok {
			if text, ok := apiObject["message"].(string); ok {
				message = text
			}
		}
		if text, ok := object["error"].(string); ok {
			message = text
		}
	}
	return &APIError{Status: status, Message: message, Details: payload}
}
