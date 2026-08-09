package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const apiBaseURL = "https://api.spotify.com/v1"

type APIError struct {
	Status  int
	Message string
	Details any
	// RetryAfter is the server's requested wait on a 429, when present.
	RetryAfter time.Duration
}

func (err *APIError) Error() string {
	return fmt.Sprintf("Spotify API returned %d: %s", err.Status, err.Message)
}

// authLoginFix is the one command that resolves every authentication failure,
// carried on the error itself so a caller never has to run `auth status` up
// front to discover it.
const authLoginFix = "spotctl auth login --client-id YOUR_SPOTIFY_CLIENT_ID"

// authError marks a failure the user can only fix by authenticating again. It
// wraps its cause, so errors.As still finds an APIError underneath a 401.
type authError struct {
	Message string
	Fix     string
	Cause   error
}

func (err *authError) Error() string {
	if err.Cause == nil {
		return fmt.Sprintf("%s: %s", err.Message, err.Fix)
	}
	return fmt.Sprintf("%s: %s (%v)", err.Message, err.Fix, err.Cause)
}

func (err *authError) Unwrap() error { return err.Cause }

func notAuthenticated(message string, cause error) error {
	return &authError{Message: message, Fix: authLoginFix, Cause: cause}
}

type spotifyClient struct {
	httpClient *http.Client
	creds      credentials
}

func newSpotifyClient() (*spotifyClient, error) {
	creds, err := loadCredentials()
	if err != nil {
		return nil, notAuthenticated("not authenticated", err)
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
		return notAuthenticated("stored credentials have no refresh token", nil)
	}
	token, err := exchangeToken(url.Values{
		"client_id":     {client.creds.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {client.creds.RefreshToken},
	})
	if err != nil {
		// A refresh only fails for good when the grant is gone: revoked in the
		// Spotify account, or the application was deleted.
		return notAuthenticated("stored credentials are no longer valid", err)
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
		err := decodeAPIError(response.StatusCode, data)
		if response.StatusCode == http.StatusUnauthorized {
			return nil, notAuthenticated("Spotify rejected the access token", err)
		}
		if response.StatusCode == http.StatusTooManyRequests {
			if seconds, convErr := strconv.Atoi(strings.TrimSpace(response.Header.Get("Retry-After"))); convErr == nil && seconds >= 0 {
				var apiErr *APIError
				if errors.As(err, &apiErr) {
					apiErr.RetryAfter = time.Duration(seconds) * time.Second
				}
			}
		}
		return nil, err
	}
	// A successful write is not required to answer in JSON. POST
	// /me/player/queue returns 200 with a 27-byte opaque token and no
	// Content-Type at all, so demanding JSON here reported every queued track
	// as failed while the queue filled up. Reads that somehow come back
	// non-JSON still fail, but at the caller's own decode step, which says what
	// it was decoding.
	if !json.Valid(bytes.TrimSpace(data)) {
		return json.RawMessage(`{"ok":true}`), nil
	}
	return json.RawMessage(data), nil
}

// retrySleep is overridable so tests can exercise the backoff without waiting.
var retrySleep = time.Sleep

const maxRateLimitRetries = 5

// requestWithRetry retries only on HTTP 429, backing off exponentially from
// one second and honoring the server's Retry-After header as a floor. Every
// other error (and success) returns immediately.
func requestWithRetry(client *spotifyClient, method, path string, query url.Values, body any) (json.RawMessage, error) {
	backoff := time.Second
	for attempt := 0; ; attempt++ {
		result, err := client.request(method, path, query, body)
		if err == nil {
			return result, nil
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || attempt >= maxRateLimitRetries {
			return nil, err
		}
		wait := backoff
		if apiErr.RetryAfter > wait {
			wait = apiErr.RetryAfter
		}
		retrySleep(wait)
		backoff *= 2
	}
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
