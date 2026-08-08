package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func samplePlaylists() []cachedPlaylist {
	return []cachedPlaylist{
		{
			ID:         "p1",
			Name:       "Techno",
			SnapshotID: "s1",
			Payload:    json.RawMessage(`{"id":"p1","name":"Techno","public":false,"owner":{"display_name":"samir"},"tracks":{"total":2}}`),
			Tracks: []cachedTrack{
				{
					ID: "t1", Name: "One", AddedAt: "2026-01-01T00:00:00Z",
					Artists:     []cachedArtist{{ID: "a1", Name: "First"}},
					Payload:     json.RawMessage(`{"id":"t1","name":"One","artists":[{"name":"First"}],"album":{"name":"Album"}}`),
					ItemPayload: json.RawMessage(`{"added_at":"2026-01-01T00:00:00Z","added_by":{"id":"samir"},"is_local":false,"track":{"id":"t1","name":"One"}}`),
				},
				{
					ID: "t2", Name: "Two", AddedAt: "2026-01-02T00:00:00Z",
					Artists:     []cachedArtist{{ID: "a2", Name: "Second"}},
					Payload:     json.RawMessage(`{"id":"t2","name":"Two","artists":[{"name":"Second"}],"album":{"name":"Album"}}`),
					ItemPayload: json.RawMessage(`{"added_at":"2026-01-02T00:00:00Z","added_by":{"id":"samir"},"is_local":false,"track":{"id":"t2","name":"Two"}}`),
				},
			},
		},
		{
			ID:         "p2",
			Name:       "Rap",
			SnapshotID: "s2",
			Payload:    json.RawMessage(`{"id":"p2","name":"Rap","public":true,"owner":{"display_name":"samir"},"tracks":{"total":0}}`),
		},
	}
}

func TestCachedPlaylistReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	if err := replacePlaylistCache(path, samplePlaylists(), time.Now().UTC()); err != nil {
		t.Fatalf("replacePlaylistCache: %v", err)
	}

	envelope, cachedAt, err := queryCachedPlaylists(path, false, 0, 0)
	if err != nil {
		t.Fatalf("queryCachedPlaylists: %v", err)
	}
	if cachedAt == "" || envelope.Source != "cache" {
		t.Fatalf("unexpected provenance: %q %q", cachedAt, envelope.Source)
	}
	playlists := envelope.Items.([]minimalPlaylist)
	if envelope.Total != 2 || len(playlists) != 2 {
		t.Fatalf("got %d playlists, want 2", len(playlists))
	}
	// ordered by name: Rap before Techno
	if playlists[0].Name != "Rap" || playlists[1].Name != "Techno" {
		t.Fatalf("unexpected order: %+v", playlists)
	}
	// everything fits in one page, so there is genuinely no next
	if envelope.Next != nil || envelope.Previous != nil {
		t.Fatalf("unexpected paging links: %v %v", envelope.Next, envelope.Previous)
	}

	items, err := queryCachedPlaylistItems(path, "p1", false, 0, 0)
	if err != nil {
		t.Fatalf("queryCachedPlaylistItems: %v", err)
	}
	tracks := items.Items.([]minimalTrack)
	if len(tracks) != 2 || tracks[0].ID != "t1" || tracks[1].ID != "t2" {
		t.Fatalf("unexpected items: %+v", tracks)
	}

	// --full returns the item object Spotify sent, added_by included
	fullItems, err := queryCachedPlaylistItems(path, "p1", true, 0, 0)
	if err != nil {
		t.Fatalf("queryCachedPlaylistItems full: %v", err)
	}
	payloads := fullItems.Items.([]json.RawMessage)
	var item struct {
		AddedAt string `json:"added_at"`
		AddedBy struct {
			ID string `json:"id"`
		} `json:"added_by"`
		Track struct {
			ID string `json:"id"`
		} `json:"track"`
	}
	if err := json.Unmarshal(payloads[0], &item); err != nil {
		t.Fatalf("decode full item: %v", err)
	}
	if item.AddedAt != "2026-01-01T00:00:00Z" || item.Track.ID != "t1" {
		t.Fatalf("unexpected full item: %+v", item)
	}
	if item.AddedBy.ID != "samir" {
		t.Fatalf("--full lost added_by: %+v", item)
	}
}

// Paging from the cache has to produce honest links: a next only when there is
// more, a previous only when there is something behind.
func TestCachedPlaylistPaging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	if err := replacePlaylistCache(path, samplePlaylists(), time.Now().UTC()); err != nil {
		t.Fatalf("replacePlaylistCache: %v", err)
	}
	if err := upsertPlaylistSummaries(
		path, samplePlaylists(), "https://api.spotify.com/v1/users/samir/playlists", time.Now().UTC(),
	); err != nil {
		t.Fatalf("upsertPlaylistSummaries: %v", err)
	}

	first, _, err := queryCachedPlaylists(path, false, 1, 0)
	if err != nil {
		t.Fatalf("queryCachedPlaylists: %v", err)
	}
	if first.Total != 2 || first.Limit != 1 || first.Offset != 0 {
		t.Fatalf("unexpected paging: %+v", first)
	}
	if first.Next == nil || *first.Next != "https://api.spotify.com/v1/users/samir/playlists?limit=1&offset=1" {
		t.Fatalf("unexpected next: %v", first.Next)
	}
	if first.Previous != nil {
		t.Fatalf("first page should have no previous: %v", *first.Previous)
	}

	second, _, err := queryCachedPlaylists(path, false, 1, 1)
	if err != nil {
		t.Fatalf("queryCachedPlaylists: %v", err)
	}
	if second.Next != nil {
		t.Fatalf("last page should have no next: %v", *second.Next)
	}
	if second.Previous == nil {
		t.Fatal("second page should have a previous")
	}
	if len(second.Items.([]minimalPlaylist)) != 1 {
		t.Fatalf("unexpected page size: %+v", second.Items)
	}
}

// The light refresh rewrites playlist rows. Because playlist_tracks cascades on
// delete, a refresh that replaced rows instead of upserting them would silently
// empty the track cache and leave the offline query commands answering nothing.
func TestLightRefreshKeepsCachedTracks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	// distinct timestamps: RFC3339 has second granularity, so two time.Now()
	// calls in the same test would format identically
	seeded := time.Now().UTC().Add(-time.Hour)
	if err := replacePlaylistCache(path, samplePlaylists(), seeded); err != nil {
		t.Fatalf("replacePlaylistCache: %v", err)
	}

	renamed := []cachedPlaylist{{
		ID:         "p1",
		Name:       "Techno renamed",
		SnapshotID: "s3",
		Payload:    json.RawMessage(`{"id":"p1","name":"Techno renamed","public":false,"owner":{"display_name":"samir"},"tracks":{"total":2}}`),
	}}
	if err := upsertPlaylistSummaries(path, renamed, "", time.Now().UTC()); err != nil {
		t.Fatalf("upsertPlaylistSummaries: %v", err)
	}

	items, err := queryCachedPlaylistItems(path, "p1", false, 0, 0)
	if err != nil {
		t.Fatalf("queryCachedPlaylistItems after refresh: %v", err)
	}
	if len(items.Items.([]minimalTrack)) != 2 {
		t.Fatalf("light refresh dropped cached tracks: got %+v", items.Items)
	}

	envelope, _, err := queryCachedPlaylists(path, false, 0, 0)
	if err != nil {
		t.Fatalf("queryCachedPlaylists: %v", err)
	}
	playlists := envelope.Items.([]minimalPlaylist)
	if len(playlists) != 1 || playlists[0].Name != "Techno renamed" {
		t.Fatalf("playlist that disappeared upstream was kept: %+v", playlists)
	}

	// the track cache's own freshness marker must not be touched by a light
	// refresh, or stats/search/artists would claim to be current
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()
	var trackCachedAt, listCachedAt string
	if err := database.QueryRow("SELECT cached_at FROM playlist_cache_metadata WHERE id = 1").Scan(&trackCachedAt); err != nil {
		t.Fatalf("read track cache metadata: %v", err)
	}
	if err := database.QueryRow("SELECT cached_at FROM playlist_list_metadata WHERE id = 1").Scan(&listCachedAt); err != nil {
		t.Fatalf("read list metadata: %v", err)
	}
	if trackCachedAt != seeded.Format(time.RFC3339) {
		t.Fatalf("light refresh moved the track cache timestamp to %q", trackCachedAt)
	}
	if listCachedAt == trackCachedAt {
		t.Fatal("light refresh did not advance the playlist list timestamp")
	}
}

func TestEmptyCacheIsDistinguishable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	if _, _, err := queryCachedPlaylists(path, false, 0, 0); !errors.Is(err, errCacheEmpty) {
		t.Fatalf("got %v, want errCacheEmpty so the caller can fall back to the API", err)
	}
}

// A cache written before the payload columns existed must be migrated and then
// reported as unusable for these reads, not silently returned as empty.
func TestCachePredatingPayloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := database.Exec(`
		CREATE TABLE playlists (id TEXT PRIMARY KEY, name TEXT NOT NULL, snapshot_id TEXT NOT NULL, cached_at TEXT NOT NULL);
		CREATE TABLE playlist_list_metadata (id INTEGER PRIMARY KEY CHECK (id = 1), cached_at TEXT NOT NULL);
		INSERT INTO playlists VALUES ('p1', 'Old', 's1', '2026-01-01T00:00:00Z');
		INSERT INTO playlist_list_metadata VALUES (1, '2026-01-01T00:00:00Z');
	`); err != nil {
		t.Fatalf("seed old schema: %v", err)
	}
	database.Close()

	if _, _, err := queryCachedPlaylists(path, false, 0, 0); !errors.Is(err, errCacheNoPayloads) {
		t.Fatalf("got %v, want errCacheNoPayloads", err)
	}
}
