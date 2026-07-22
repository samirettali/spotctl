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

type playlistContainsResult struct {
	PlaylistID   string `json:"playlist_id"`
	PlaylistName string `json:"playlist_name"`
	TrackID      string `json:"track_id"`
	Contains     bool   `json:"contains"`
	CachedAt     string `json:"cached_at"`
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
	if flags.NArg() != 2 {
		return errors.New("usage: spotctl playlist contains [--db PATH] PLAYLIST TRACK")
	}

	playlistID, err := exactSpotifyID(flags.Arg(0), "playlist")
	if err != nil {
		return err
	}
	trackID, err := exactSpotifyID(flags.Arg(1), "track")
	if err != nil {
		return err
	}
	path, err := resolvePlaylistCachePath(*databasePath)
	if err != nil {
		return err
	}
	result, err := queryPlaylistContains(path, playlistID, trackID)
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

func queryPlaylistContains(path, playlistID, trackID string) (playlistContainsResult, error) {
	database, err := openPlaylistCache(path)
	if err != nil {
		return playlistContainsResult{}, err
	}
	defer database.Close()

	result := playlistContainsResult{PlaylistID: playlistID, TrackID: trackID}
	if err := database.QueryRow(
		"SELECT name, cached_at FROM playlists WHERE id = ?", playlistID,
	).Scan(&result.PlaylistName, &result.CachedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return playlistContainsResult{}, fmt.Errorf("playlist %q is not in the cache; run 'spotctl playlist cache'", playlistID)
		}
		return playlistContainsResult{}, fmt.Errorf("query playlist cache: %w", err)
	}
	if err := database.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM playlist_tracks WHERE playlist_id = ? AND track_id = ?)",
		playlistID, trackID,
	).Scan(&result.Contains); err != nil {
		return playlistContainsResult{}, fmt.Errorf("query playlist tracks: %w", err)
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
