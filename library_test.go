package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleSavedTracks() []savedTrack {
	return []savedTrack{
		{ID: "t1", AddedAt: "2026-08-01T18:53:25Z", Payload: json.RawMessage(
			`{"added_at":"2026-08-01T18:53:25Z","track":{"id":"t1","name":"Newest","artists":[{"name":"First"}],"album":{"name":"Album"},"available_markets":["IT"]}}`,
		)},
		{ID: "t2", AddedAt: "2026-07-01T00:00:00Z", Payload: json.RawMessage(
			`{"added_at":"2026-07-01T00:00:00Z","track":{"id":"t2","name":"Older","artists":[{"name":"Second"}],"album":{"name":"Album"}}}`,
		)},
	}
}

func TestLibraryTracksValidation(t *testing.T) {
	if err := runLibrary(nil); err == nil {
		t.Fatal("library accepted no subcommand")
	}
	if err := runLibrary([]string{"albums"}); err == nil {
		t.Fatal("library accepted an unknown subcommand")
	}
	if err := libraryTracks([]string{"--limit", "-1"}); err == nil {
		t.Fatal("library tracks accepted a negative limit")
	}
	if err := libraryTracks([]string{"--offset", "-1"}); err == nil {
		t.Fatal("library tracks accepted a negative offset")
	}
	if err := libraryTracks([]string{"extra"}); err == nil {
		t.Fatal("library tracks accepted a positional argument")
	}
}

func TestCachedSavedTracksReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	if _, err := queryCachedSavedTracks(path, false, 0, 0); !errors.Is(err, errLibraryEmpty) {
		t.Fatalf("got %v, want errLibraryEmpty so the caller can fall back to the API", err)
	}

	if err := replaceSavedTracks(path, sampleSavedTracks(), "https://api.spotify.com/v1/me/tracks", time.Now().UTC()); err != nil {
		t.Fatalf("replaceSavedTracks: %v", err)
	}

	envelope, err := queryCachedSavedTracks(path, false, 0, 0)
	if err != nil {
		t.Fatalf("queryCachedSavedTracks: %v", err)
	}
	if envelope.Source != "cache" || envelope.CachedAt == "" {
		t.Fatalf("unexpected provenance: %+v", envelope)
	}
	tracks := envelope.Items.([]minimalTrack)
	if envelope.Total != 2 || len(tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(tracks))
	}
	// Spotify's order, most recently saved first
	if tracks[0].ID != "t1" || tracks[1].ID != "t2" {
		t.Fatalf("unexpected order: %+v", tracks)
	}
	if tracks[0].Name != "Newest" || tracks[0].Artists[0] != "First" || tracks[0].Album != "Album" {
		t.Fatalf("unexpected trimmed track: %+v", tracks[0])
	}
	if envelope.Next != nil || envelope.Previous != nil {
		t.Fatalf("unexpected paging links: %v %v", envelope.Next, envelope.Previous)
	}

	// --full returns the item Spotify sent, added_at and all
	fullEnvelope, err := queryCachedSavedTracks(path, true, 0, 0)
	if err != nil {
		t.Fatalf("queryCachedSavedTracks full: %v", err)
	}
	payloads := fullEnvelope.Items.([]json.RawMessage)
	if len(payloads) != 2 || !strings.Contains(string(payloads[0]), `"available_markets":["IT"]`) {
		t.Fatalf("--full dropped a field Spotify sent: %s", payloads[0])
	}
	var item struct {
		AddedAt string `json:"added_at"`
	}
	if err := json.Unmarshal(payloads[0], &item); err != nil || item.AddedAt != "2026-08-01T18:53:25Z" {
		t.Fatalf("--full lost added_at: %s (%v)", payloads[0], err)
	}
}

func TestCachedSavedTracksPaging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	if err := replaceSavedTracks(path, sampleSavedTracks(), "https://api.spotify.com/v1/me/tracks", time.Now().UTC()); err != nil {
		t.Fatalf("replaceSavedTracks: %v", err)
	}

	first, err := queryCachedSavedTracks(path, false, 1, 0)
	if err != nil {
		t.Fatalf("queryCachedSavedTracks: %v", err)
	}
	if first.Total != 2 || first.Limit != 1 || first.Offset != 0 {
		t.Fatalf("unexpected paging: %+v", first)
	}
	if first.Next == nil || *first.Next != "https://api.spotify.com/v1/me/tracks?limit=1&offset=1" {
		t.Fatalf("unexpected next: %v", first.Next)
	}
	if first.Previous != nil {
		t.Fatalf("first page should have no previous: %v", *first.Previous)
	}

	second, err := queryCachedSavedTracks(path, false, 1, 1)
	if err != nil {
		t.Fatalf("queryCachedSavedTracks: %v", err)
	}
	if second.Next != nil {
		t.Fatalf("last page should have no next: %v", *second.Next)
	}
	if second.Previous == nil {
		t.Fatal("second page should have a previous")
	}
	if tracks := second.Items.([]minimalTrack); len(tracks) != 1 || tracks[0].ID != "t2" {
		t.Fatalf("unexpected page: %+v", second.Items)
	}
}

// A full playlist refresh clears the tracks table. The library must not go
// with it, and the other way round: the two are different sets with their own
// freshness markers.
func TestSavedTracksSurvivePlaylistCacheReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	if err := replaceSavedTracks(path, sampleSavedTracks(), "", time.Now().UTC()); err != nil {
		t.Fatalf("replaceSavedTracks: %v", err)
	}
	if err := replacePlaylistCache(path, samplePlaylists(), time.Now().UTC()); err != nil {
		t.Fatalf("replacePlaylistCache: %v", err)
	}
	envelope, err := queryCachedSavedTracks(path, false, 0, 0)
	if err != nil {
		t.Fatalf("queryCachedSavedTracks after playlist cache: %v", err)
	}
	if envelope.Total != 2 {
		t.Fatalf("playlist cache replacement dropped saved tracks: %+v", envelope)
	}

	if err := replaceSavedTracks(path, nil, "", time.Now().UTC()); err != nil {
		t.Fatalf("replaceSavedTracks: %v", err)
	}
	items, err := queryCachedPlaylistItems(path, "p1", false, 0, 0)
	if err != nil {
		t.Fatalf("queryCachedPlaylistItems after library refresh: %v", err)
	}
	if len(items.Items.([]minimalTrack)) != 2 {
		t.Fatalf("library refresh dropped playlist tracks: %+v", items.Items)
	}
	envelope, err = queryCachedSavedTracks(path, false, 0, 0)
	if err != nil {
		t.Fatalf("queryCachedSavedTracks after empty refresh: %v", err)
	}
	if envelope.Total != 0 || len(envelope.Items.([]minimalTrack)) != 0 {
		t.Fatalf("an emptied library should read as cached and empty, not uninitialized: %+v", envelope)
	}
}

func TestFetchSavedTracksFollowsPages(t *testing.T) {
	client := testClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/me/tracks" {
			t.Errorf("path = %q, want /v1/me/tracks", request.URL.Path)
		}
		if request.URL.Query().Get("limit") != "50" {
			t.Errorf("limit = %q, want 50", request.URL.Query().Get("limit"))
		}
		switch request.URL.Query().Get("offset") {
		case "0":
			return stubResponse(http.StatusOK, `{
				"href":"https://api.spotify.com/v1/me/tracks?offset=0&limit=50",
				"next":"https://api.spotify.com/v1/me/tracks?offset=50&limit=50",
				"total":51,
				"items":[
					{"added_at":"2026-08-01T00:00:00Z","track":{"id":"t1","name":"One"}},
					{"added_at":"2026-07-01T00:00:00Z","track":null}
				]
			}`, nil), nil
		case "2":
			return stubResponse(http.StatusOK, `{
				"href":"https://api.spotify.com/v1/me/tracks?offset=2&limit=50",
				"next":null,
				"total":51,
				"items":[{"added_at":"2026-06-01T00:00:00Z","track":{"id":"t3","name":"Three"}}]
			}`, nil), nil
		default:
			t.Errorf("unexpected offset %q", request.URL.Query().Get("offset"))
			return stubResponse(http.StatusBadRequest, `{}`, nil), nil
		}
	})

	tracks, collection, err := fetchSavedTracks(client)
	if err != nil {
		t.Fatalf("fetchSavedTracks: %v", err)
	}
	if collection != "https://api.spotify.com/v1/me/tracks" {
		t.Fatalf("collection = %q", collection)
	}
	// the null track is skipped, the offset still advances by the page size
	if len(tracks) != 2 || tracks[0].ID != "t1" || tracks[1].ID != "t3" {
		t.Fatalf("unexpected tracks: %+v", tracks)
	}
	if tracks[0].AddedAt != "2026-08-01T00:00:00Z" {
		t.Fatalf("unexpected added_at: %q", tracks[0].AddedAt)
	}
}
