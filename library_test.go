package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSavedTracksCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	if _, err := querySavedTracks(path, false, 0, 0); !errors.Is(err, errSavedTracksEmpty) {
		t.Fatalf("uninitialized: %v", err)
	}
	items := []json.RawMessage{
		json.RawMessage(`{"added_at":"2026-01-01","track":{"id":"t2","name":"Two","available_markets":["IT"]},"future_field":true}`),
		json.RawMessage(`{"added_at":"2026-01-02","track":{"id":"t1","name":"One"}}`),
	}
	now := time.Now().UTC().Truncate(time.Second)
	collection := spotifyAPIBase + "/me/tracks"
	if err := replaceSavedTracks(path, items, collection, now); err != nil {
		t.Fatal(err)
	}
	// A full playlist replacement must leave the saved library intact.
	if err := replacePlaylistCache(path, samplePlaylists(), now); err != nil {
		t.Fatal(err)
	}
	page, err := querySavedTracks(path, false, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || page.Source != "cache" || page.CachedAt != now.Format(time.RFC3339) || page.Items.([]minimalTrack)[0].ID != "t2" || page.Next == nil || page.Previous != nil {
		t.Fatalf("unexpected page: %+v", page)
	}
	if *page.Next != collection+"?limit=1&offset=1" {
		t.Fatalf("next: %s", *page.Next)
	}
	page, err = querySavedTracks(path, true, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if page.Next != nil || page.Previous == nil || string(page.Items.([]json.RawMessage)[0]) != string(items[1]) {
		t.Fatalf("unexpected full page: %+v", page)
	}
	page, err = querySavedTracks(path, true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Limit != 2 || page.Next != nil || string(page.Items.([]json.RawMessage)[0]) != string(items[0]) {
		t.Fatalf("full payload lost: %+v", page)
	}
	page, err = querySavedTracks(path, false, 10, 10)
	if err != nil || len(page.Items.([]minimalTrack)) != 0 {
		t.Fatalf("beyond end: %+v, %v", page, err)
	}
	if err := replaceSavedTracks(path, nil, collection, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	page, err = querySavedTracks(path, false, 0, 0)
	if err != nil || page.Total != 0 || page.Items == nil || page.Next != nil {
		t.Fatalf("initialized empty: %+v, %v", page, err)
	}
	playlists, _, err := queryCachedPlaylists(path, false, 0, 0)
	if err != nil || playlists.Total != 2 {
		t.Fatalf("saved refresh damaged playlists: %+v, %v", playlists, err)
	}
	tracks, err := queryCachedPlaylistItems(path, "p1", false, 0, 0)
	if err != nil || tracks.Total != 2 {
		t.Fatalf("saved refresh damaged tracks: %+v, %v", tracks, err)
	}
}

func TestFetchSavedTracks(t *testing.T) {
	calls := 0
	client := testClient(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/me/tracks" || r.URL.Query().Get("limit") != "50" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		calls++
		if calls == 1 {
			if r.URL.Query().Get("offset") != "0" {
				t.Fatal(r.URL)
			}
			return stubResponse(200, `{"href":"https://api.spotify.com/v1/me/tracks?limit=50&offset=0","items":[{"track":{"id":"one"}}],"next":"next"}`, nil), nil
		}
		if r.URL.Query().Get("offset") != "1" {
			t.Fatal(r.URL)
		}
		return stubResponse(200, `{"items":[{"track":{"id":"two"}}],"next":null}`, nil), nil
	})
	items, href, err := fetchSavedTracks(client)
	if err != nil || len(items) != 2 || calls != 2 || href != spotifyAPIBase+"/me/tracks" {
		t.Fatalf("fetch: %v %s %v", items, href, err)
	}
}

func TestFetchSavedTracksErrors(t *testing.T) {
	for _, body := range []string{`not json`, `{"items":[],"next":"next"}`} {
		client := testClient(func(r *http.Request) (*http.Response, error) { return stubResponse(200, body, nil), nil })
		if _, _, err := fetchSavedTracks(client); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	client := testClient(func(r *http.Request) (*http.Response, error) {
		return stubResponse(403, `{"error":{"message":"forbidden"}}`, nil), nil
	})
	if _, _, err := fetchSavedTracks(client); err == nil {
		t.Fatal("accepted failed request")
	}
}

func TestLibraryReadsOffline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "cache.db")
	if err := replaceSavedTracks(path, nil, spotifyAPIBase+"/me/tracks", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	original := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = original }()
	if err := runLibrary([]string{"tracks", "--db", path}); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var page struct {
		Source string            `json:"source"`
		Items  []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(output).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if page.Source != "cache" || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("unexpected output: %+v", page)
	}
}

func TestSavedTracksRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	now := time.Now().UTC().Truncate(time.Second)
	if err := replaceSavedTracks(path, []json.RawMessage{json.RawMessage(`{"track":{"id":"old"}}`)}, spotifyAPIBase+"/me/tracks", now); err != nil {
		t.Fatal(err)
	}
	db, err := openPlaylistCache(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TRIGGER fail_saved_insert BEFORE INSERT ON saved_tracks BEGIN SELECT RAISE(ABORT, 'test failure'); END`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceSavedTracks(path, []json.RawMessage{json.RawMessage(`{"track":{"id":"new"}}`)}, "changed", now.Add(time.Hour)); err == nil {
		t.Fatal("expected insert failure")
	}
	page, err := querySavedTracks(path, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Items.([]minimalTrack)[0].ID != "old" || page.CachedAt != now.Format(time.RFC3339) {
		t.Fatalf("failed refresh changed cache: %+v", page)
	}
}

func TestLibraryValidation(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"tracks", "extra"}, {"tracks", "--limit", "-1"}, {"tracks", "--offset", "-1"}} {
		if err := runLibrary(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
