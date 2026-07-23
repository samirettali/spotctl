package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type cachedPlaylist struct {
	ID         string
	Name       string
	SnapshotID string
	TrackIDs   []string
}

type playlistPage struct {
	Items []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		SnapshotID string `json:"snapshot_id"`
	} `json:"items"`
	Next *string `json:"next"`
}

type playlistItemPage struct {
	Items []struct {
		Item  *spotifyPlaylistItem `json:"item"`
		Track *spotifyPlaylistItem `json:"track"`
	} `json:"items"`
	Next *string `json:"next"`
}

type spotifyPlaylistItem struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type playlistCacheResult struct {
	Playlists int    `json:"playlists"`
	Tracks    int    `json:"tracks"`
	Path      string `json:"path"`
	CachedAt  string `json:"cached_at"`
}

type cachedPlaylistReference struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type playlistContainsResult struct {
	TrackID   string                    `json:"track_id"`
	Contains  bool                      `json:"contains"`
	Playlists []cachedPlaylistReference `json:"playlists"`
}

type playlistContainsResults struct {
	Results  []playlistContainsResult `json:"results"`
	CachedAt string                   `json:"cached_at"`
}

func playlistCache(client *spotifyClient, args []string) error {
	flags := flag.NewFlagSet("playlist cache", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	databasePath := flags.String("db", "", "SQLite cache path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: spotctl playlist cache [--db PATH]")
	}

	path, err := resolvePlaylistCachePath(*databasePath)
	if err != nil {
		return err
	}
	playlists, err := fetchAllPlaylists(client)
	if err != nil {
		return err
	}
	cachedAt := time.Now().UTC()
	if err := replacePlaylistCache(path, playlists, cachedAt); err != nil {
		return err
	}

	trackCount := 0
	for _, playlist := range playlists {
		trackCount += len(playlist.TrackIDs)
	}
	return writeJSON(playlistCacheResult{
		Playlists: len(playlists),
		Tracks:    trackCount,
		Path:      path,
		CachedAt:  cachedAt.Format(time.RFC3339),
	})
}

func playlistContains(args []string) error {
	flags := flag.NewFlagSet("playlist contains", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	databasePath := flags.String("db", "", "SQLite cache path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return errors.New("usage: spotctl playlist contains [--db PATH] TRACK...")
	}

	trackIDs := make([]string, 0, flags.NArg())
	for _, value := range flags.Args() {
		trackID, err := exactSpotifyID(value, "track")
		if err != nil {
			return err
		}
		trackIDs = append(trackIDs, trackID)
	}
	path, err := resolvePlaylistCachePath(*databasePath)
	if err != nil {
		return err
	}
	result, err := queryPlaylistContains(path, trackIDs)
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func fetchAllPlaylists(client *spotifyClient) ([]cachedPlaylist, error) {
	var playlists []cachedPlaylist
	offset := 0
	for {
		data, err := client.request(http.MethodGet, "/me/playlists", url.Values{
			"limit":  {"50"},
			"offset": {strconv.Itoa(offset)},
		}, nil)
		if err != nil {
			return nil, fmt.Errorf("fetch playlists: %w", err)
		}
		var page playlistPage
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("decode playlists: %w", err)
		}
		for _, item := range page.Items {
			playlist := cachedPlaylist{ID: item.ID, Name: item.Name, SnapshotID: item.SnapshotID}
			playlist.TrackIDs, err = fetchAllPlaylistTracks(client, item.ID)
			if err != nil {
				return nil, fmt.Errorf("fetch playlist %q: %w", item.Name, err)
			}
			playlists = append(playlists, playlist)
		}
		if page.Next == nil || *page.Next == "" {
			break
		}
		offset += len(page.Items)
		if len(page.Items) == 0 {
			return nil, errors.New("Spotify returned an empty playlist page with a next page")
		}
	}
	return playlists, nil
}

func fetchAllPlaylistTracks(client *spotifyClient, playlistID string) ([]string, error) {
	var trackIDs []string
	offset := 0
	for {
		data, err := client.request(http.MethodGet, "/playlists/"+playlistID+"/items", url.Values{
			"limit":  {"100"},
			"offset": {strconv.Itoa(offset)},
		}, nil)
		if err != nil {
			return nil, err
		}
		var page playlistItemPage
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("decode playlist items: %w", err)
		}
		for _, entry := range page.Items {
			item := entry.Item
			if item == nil {
				item = entry.Track
			}
			if item != nil && item.Type == "track" && item.ID != "" {
				trackIDs = append(trackIDs, item.ID)
			}
		}
		if page.Next == nil || *page.Next == "" {
			break
		}
		offset += len(page.Items)
		if len(page.Items) == 0 {
			return nil, errors.New("Spotify returned an empty item page with a next page")
		}
	}
	return trackIDs, nil
}

func resolvePlaylistCachePath(override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find user cache directory: %w", err)
	}
	return filepath.Join(cacheDir, "spotctl", "playlists.db"), nil
}

func openPlaylistCache(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create cache directory: %w", err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open playlist cache: %w", err)
	}
	database.SetMaxOpenConns(1)
	if _, err := database.Exec(`
		PRAGMA foreign_keys = ON;
		PRAGMA busy_timeout = 5000;
		CREATE TABLE IF NOT EXISTS playlists (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			snapshot_id TEXT NOT NULL,
			cached_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS playlist_tracks (
			playlist_id TEXT NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			track_id TEXT NOT NULL,
			PRIMARY KEY (playlist_id, position)
		);
		CREATE INDEX IF NOT EXISTS playlist_tracks_lookup
			ON playlist_tracks (playlist_id, track_id);
		CREATE INDEX IF NOT EXISTS playlist_tracks_track_lookup
			ON playlist_tracks (track_id);
		CREATE TABLE IF NOT EXISTS playlist_cache_metadata (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			cached_at TEXT NOT NULL
		);
	`); err != nil {
		database.Close()
		return nil, fmt.Errorf("initialize playlist cache: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		database.Close()
		return nil, fmt.Errorf("secure playlist cache: %w", err)
	}
	return database, nil
}

func replacePlaylistCache(path string, playlists []cachedPlaylist, cachedAt time.Time) error {
	database, err := openPlaylistCache(path)
	if err != nil {
		return err
	}
	defer database.Close()

	transaction, err := database.Begin()
	if err != nil {
		return fmt.Errorf("begin playlist cache update: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.Exec("DELETE FROM playlists"); err != nil {
		return fmt.Errorf("clear playlist cache: %w", err)
	}
	if _, err := transaction.Exec(
		"INSERT INTO playlist_cache_metadata (id, cached_at) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET cached_at = excluded.cached_at",
		cachedAt.Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("update playlist cache metadata: %w", err)
	}
	for _, playlist := range playlists {
		if _, err := transaction.Exec(
			"INSERT INTO playlists (id, name, snapshot_id, cached_at) VALUES (?, ?, ?, ?)",
			playlist.ID, playlist.Name, playlist.SnapshotID, cachedAt.Format(time.RFC3339),
		); err != nil {
			return fmt.Errorf("cache playlist %q: %w", playlist.Name, err)
		}
		for position, trackID := range playlist.TrackIDs {
			if _, err := transaction.Exec(
				"INSERT INTO playlist_tracks (playlist_id, position, track_id) VALUES (?, ?, ?)",
				playlist.ID, position, trackID,
			); err != nil {
				return fmt.Errorf("cache track in playlist %q: %w", playlist.Name, err)
			}
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit playlist cache update: %w", err)
	}
	return nil
}

func queryPlaylistContains(path string, trackIDs []string) (playlistContainsResults, error) {
	database, err := openPlaylistCache(path)
	if err != nil {
		return playlistContainsResults{}, err
	}
	defer database.Close()

	result := playlistContainsResults{Results: make([]playlistContainsResult, 0, len(trackIDs))}
	if err := database.QueryRow(
		"SELECT cached_at FROM playlist_cache_metadata WHERE id = 1",
	).Scan(&result.CachedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return playlistContainsResults{}, errors.New("playlist cache is empty; run 'spotctl playlist cache'")
		}
		return playlistContainsResults{}, fmt.Errorf("query playlist cache metadata: %w", err)
	}

	for _, trackID := range trackIDs {
		trackResult := playlistContainsResult{
			TrackID:   trackID,
			Playlists: make([]cachedPlaylistReference, 0),
		}
		rows, err := database.Query(`
			SELECT playlists.id, playlists.name
			FROM playlist_tracks
			JOIN playlists ON playlists.id = playlist_tracks.playlist_id
			WHERE playlist_tracks.track_id = ?
			GROUP BY playlists.id, playlists.name
			ORDER BY playlists.name, playlists.id
		`, trackID)
		if err != nil {
			return playlistContainsResults{}, fmt.Errorf("query playlist tracks: %w", err)
		}
		for rows.Next() {
			var playlist cachedPlaylistReference
			if err := rows.Scan(&playlist.ID, &playlist.Name); err != nil {
				rows.Close()
				return playlistContainsResults{}, fmt.Errorf("scan playlist match: %w", err)
			}
			trackResult.Playlists = append(trackResult.Playlists, playlist)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return playlistContainsResults{}, fmt.Errorf("iterate playlist matches: %w", err)
		}
		if err := rows.Close(); err != nil {
			return playlistContainsResults{}, fmt.Errorf("close playlist matches: %w", err)
		}
		trackResult.Contains = len(trackResult.Playlists) > 0
		result.Results = append(result.Results, trackResult)
	}
	return result, nil
}

func exactSpotifyID(value, expectedType string) (string, error) {
	uri, err := spotifyURI(value, expectedType)
	if err != nil {
		return "", err
	}
	parts := strings.Split(uri, ":")
	if len(parts) != 3 || parts[1] != expectedType {
		return "", fmt.Errorf("expected a Spotify %s, got %q", expectedType, value)
	}
	return parts[2], nil
}
