package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultRedirectURI = "http://127.0.0.1:8989/callback"
	authorizeURL       = "https://accounts.spotify.com/authorize"
	tokenURL           = "https://accounts.spotify.com/api/token"
)

var oauthScopes = []string{
	"playlist-read-private",
	"playlist-read-collaborative",
	"playlist-modify-private",
	"playlist-modify-public",
	"user-read-playback-state",
	"user-modify-playback-state",
}

type credentials struct {
	ClientID     string    `json:"client_id"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        string    `json:"scope,omitempty"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func runAuth(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: spotctl auth login|status|logout")
	}

	switch args[0] {
	case "login":
		return authLogin(args[1:])
	case "status":
		creds, err := loadCredentials()
		if errors.Is(err, os.ErrNotExist) {
			return writeJSON(map[string]bool{"authenticated": false})
		}
		if err != nil {
			return err
		}
		return writeJSON(map[string]any{
			"authenticated": true,
			"client_id":     creds.ClientID,
			"expires_at":    creds.ExpiresAt,
			"scopes":        strings.Fields(creds.Scope),
		})
	case "logout":
		path, err := credentialsPath()
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return writeJSON(map[string]bool{"authenticated": false})
	default:
		return fmt.Errorf("unknown auth command %q", args[0])
	}
}

func authLogin(args []string) error {
	flags := flag.NewFlagSet("auth login", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	clientID := flags.String("client-id", os.Getenv("SPOTIFY_CLIENT_ID"), "Spotify application client ID")
	redirectURI := flags.String("redirect-uri", defaultRedirectURI, "registered loopback redirect URI")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *clientID == "" {
		return errors.New("client ID is required; pass --client-id or set SPOTIFY_CLIENT_ID")
	}

	callbackURL, err := url.Parse(*redirectURI)
	if err != nil {
		return fmt.Errorf("parse redirect URI: %w", err)
	}
	if callbackURL.Scheme != "http" || callbackURL.Hostname() != "127.0.0.1" {
		return errors.New("redirect URI must use http://127.0.0.1 with a registered callback path")
	}

	verifier, err := randomURLSafe(64)
	if err != nil {
		return err
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return err
	}
	challengeHash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeHash[:])

	listener, err := net.Listen("tcp", callbackURL.Host)
	if err != nil {
		return fmt.Errorf("listen on OAuth callback %s: %w", callbackURL.Host, err)
	}
	defer listener.Close()

	result := make(chan callbackResult, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc(callbackURL.Path, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("state") != state {
			http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
			result <- callbackResult{err: errors.New("OAuth state mismatch")}
			return
		}
		if oauthError := request.URL.Query().Get("error"); oauthError != "" {
			http.Error(w, "Spotify authorization failed", http.StatusBadRequest)
			result <- callbackResult{err: fmt.Errorf("Spotify authorization failed: %s", oauthError)}
			return
		}
		code := request.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			result <- callbackResult{err: errors.New("missing authorization code")}
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "spotctl is authenticated. You can close this tab.")
		result <- callbackResult{code: code}
	})
	server.Handler = mux
	go func() { _ = server.Serve(listener) }()

	authURL, _ := url.Parse(authorizeURL)
	query := authURL.Query()
	query.Set("client_id", *clientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", *redirectURI)
	query.Set("scope", strings.Join(oauthScopes, " "))
	query.Set("state", state)
	query.Set("code_challenge_method", "S256")
	query.Set("code_challenge", challenge)
	authURL.RawQuery = query.Encode()

	fmt.Fprintln(os.Stderr, "Opening Spotify authorization in your browser...")
	if err := openBrowser(authURL.String()); err != nil {
		fmt.Fprintf(os.Stderr, "Open this URL manually:\n%s\n", authURL.String())
	}

	var callback callbackResult
	select {
	case callback = <-result:
	case <-time.After(5 * time.Minute):
		return errors.New("timed out waiting for Spotify authorization")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	if callback.err != nil {
		return callback.err
	}

	token, err := exchangeToken(url.Values{
		"client_id":     {*clientID},
		"grant_type":    {"authorization_code"},
		"code":          {callback.code},
		"redirect_uri":  {*redirectURI},
		"code_verifier": {verifier},
	})
	if err != nil {
		return err
	}

	creds := credentials{
		ClientID:     *clientID,
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
		ExpiresAt:    time.Now().Add(time.Duration(token.ExpiresIn) * time.Second),
		Scope:        token.Scope,
	}
	if err := saveCredentials(creds); err != nil {
		return err
	}
	return writeJSON(map[string]any{
		"authenticated": true,
		"expires_at":    creds.ExpiresAt,
		"scopes":        strings.Fields(creds.Scope),
	})
}

type callbackResult struct {
	code string
	err  error
}

func randomURLSafe(bytes int) (string, error) {
	data := make([]byte, bytes)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}

func exchangeToken(values url.Values) (tokenResponse, error) {
	request, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("exchange OAuth token: %w", err)
	}
	defer response.Body.Close()

	var token tokenResponse
	if err := json.NewDecoder(response.Body).Decode(&token); err != nil {
		return tokenResponse{}, fmt.Errorf("decode OAuth token: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := token.Error
		if token.Description != "" {
			message += ": " + token.Description
		}
		return tokenResponse{}, fmt.Errorf("OAuth token request failed: %s", message)
	}
	return token, nil
}

func credentialsPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(configDir, "spotctl", "credentials.json"), nil
}

func loadCredentials() (credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return credentials{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return credentials{}, err
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return credentials{}, fmt.Errorf("decode credentials: %w", err)
	}
	return creds, nil
}

func saveCredentials(creds credentials) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace credentials: %w", err)
	}
	return nil
}
