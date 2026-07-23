package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type cachedArtist struct {
	ID   string
	Name string
}

type cachedTrack struct {
	ID      string
	Name    string
	Artists []cachedArtist
}

type cachedPlaylist struct {
	ID         string
	Name       string
	SnapshotID string
	Tracks     []cachedTrack
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
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Artists []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"artists"`
}

type playlistCacheResult struct {
	Refreshed bool   `json:"refreshed"`
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

type cachedArtistStats struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Tracks    int    `json:"tracks"`
	Playlists int    `json:"playlists"`
}

type playlistArtistsResults struct {
	Results []struct {
		Query   string              `json:"query"`
		Artists []cachedArtistStats `json:"artists"`
	} `json:"results"`
	Artists  []cachedArtistStats `json:"artists,omitempty"`
	CachedAt string              `json:"cached_at"`
}

type playlistStatsEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Tracks int    `json:"tracks"`
}

type playlistStatsResult struct {
	Playlists      []playlistStatsEntry `json:"playlists"`
	TotalTracks    int                  `json:"total_tracks"`
	DistinctTracks int                  `json:"distinct_tracks"`
	CachedAt       string               `json:"cached_at"`
}

type cachedTrackMatch struct {
	ID        string                    `json:"id"`
	Name      string                    `json:"name"`
	Artists   []string                  `json:"artists"`
	Playlists []cachedPlaylistReference `json:"playlists"`
}

type playlistSearchResults struct {
	Results []struct {
		Query  string             `json:"query"`
		Tracks []cachedTrackMatch `json:"tracks"`
	} `json:"results"`
	CachedAt string `json:"cached_at"`
}

type sampledTrack struct {
	ID       string                  `json:"id"`
	Name     string                  `json:"name"`
	Artists  []string                `json:"artists"`
	Playlist cachedPlaylistReference `json:"playlist"`
}

type playlistSampleResult struct {
	Tracks   []sampledTrack `json:"tracks"`
	CachedAt string         `json:"cached_at"`
}

func playlistCache(args []string) error {
	flags := flag.NewFlagSet("playlist cache", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	databasePath := flags.String("db", "", "SQLite cache path")
	maxAge := flags.Duration("max-age", 0, "skip the refresh when the cache is newer than this (e.g. 24h)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: spotctl playlist cache [--db PATH] [--max-age DURATION]")
	}
	if *maxAge < 0 {
		return errors.New("cache max age must be 0 or greater")
	}

	path, err := resolvePlaylistCachePath(*databasePath)
	if err != nil {
		return err
	}
	if *maxAge > 0 {
		status, err := readPlaylistCacheStatus(path)
		if err != nil {
			return err
		}
		if status.upToDate(*maxAge) {
			return writeJSON(playlistCacheResult{
				Refreshed: false,
				Playlists: status.playlists,
				Tracks:    status.tracks,
				Path:      path,
				CachedAt:  status.cachedAt.Format(time.RFC3339),
			})
		}
	}

	client, err := newSpotifyClient()
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
		trackCount += len(playlist.Tracks)
	}
	return writeJSON(playlistCacheResult{
		Refreshed: true,
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

func playlistArtists(args []string) error {
	flags := flag.NewFlagSet("playlist artists", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	databasePath := flags.String("db", "", "SQLite cache path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	path, err := resolvePlaylistCachePath(*databasePath)
	if err != nil {
		return err
	}
	result, err := queryPlaylistArtists(path, flags.Args())
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func playlistStats(args []string) error {
	flags := flag.NewFlagSet("playlist stats", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	databasePath := flags.String("db", "", "SQLite cache path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: spotctl playlist stats [--db PATH]")
	}
	path, err := resolvePlaylistCachePath(*databasePath)
	if err != nil {
		return err
	}
	result, err := queryPlaylistStats(path)
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func playlistSearch(args []string) error {
	flags := flag.NewFlagSet("playlist search", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	databasePath := flags.String("db", "", "SQLite cache path")
	limit := flags.Int("limit", 25, "matches per query (1-100)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return errors.New("usage: spotctl playlist search [--db PATH] [--limit N] QUERY...")
	}
	if *limit < 1 || *limit > 100 {
		return errors.New("search limit must be between 1 and 100")
	}
	path, err := resolvePlaylistCachePath(*databasePath)
	if err != nil {
		return err
	}
	result, err := queryPlaylistSearch(path, flags.Args(), *limit)
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func playlistSample(args []string) error {
	flags := flag.NewFlagSet("playlist sample", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	databasePath := flags.String("db", "", "SQLite cache path")
	limit := flags.Int("limit", 10, "number of tracks (1-100)")
	playlistFilter := flags.String("playlist", "", "only sample playlists whose name contains this")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: spotctl playlist sample [--db PATH] [--limit N] [--playlist NAME]")
	}
	if *limit < 1 || *limit > 100 {
		return errors.New("sample limit must be between 1 and 100")
	}
	path, err := resolvePlaylistCachePath(*databasePath)
	if err != nil {
		return err
	}
	result, err := queryPlaylistSample(path, *limit, *playlistFilter)
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
			playlist.Tracks, err = fetchAllPlaylistTracks(client, item.ID)
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

func fetchAllPlaylistTracks(client *spotifyClient, playlistID string) ([]cachedTrack, error) {
	var tracks []cachedTrack
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
			if item == nil || item.Type != "track" || item.ID == "" {
				continue
			}
			track := cachedTrack{ID: item.ID, Name: item.Name}
			for _, artist := range item.Artists {
				track.Artists = append(track.Artists, cachedArtist{ID: artist.ID, Name: artist.Name})
			}
			tracks = append(tracks, track)
		}
		if page.Next == nil || *page.Next == "" {
			break
		}
		offset += len(page.Items)
		if len(page.Items) == 0 {
			return nil, errors.New("Spotify returned an empty item page with a next page")
		}
	}
	return tracks, nil
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
		CREATE TABLE IF NOT EXISTS tracks (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS track_artists (
			track_id TEXT NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			artist_id TEXT NOT NULL,
			artist_name TEXT NOT NULL,
			PRIMARY KEY (track_id, position)
		);
		CREATE INDEX IF NOT EXISTS track_artists_track_lookup
			ON track_artists (track_id);
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
	if _, err := transaction.Exec("DELETE FROM tracks"); err != nil {
		return fmt.Errorf("clear track cache: %w", err)
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
		for position, track := range playlist.Tracks {
			if _, err := transaction.Exec(
				"INSERT INTO playlist_tracks (playlist_id, position, track_id) VALUES (?, ?, ?)",
				playlist.ID, position, track.ID,
			); err != nil {
				return fmt.Errorf("cache track in playlist %q: %w", playlist.Name, err)
			}
			if _, err := transaction.Exec(
				"INSERT OR IGNORE INTO tracks (id, name) VALUES (?, ?)",
				track.ID, track.Name,
			); err != nil {
				return fmt.Errorf("cache track %q: %w", track.Name, err)
			}
			for artistPosition, artist := range track.Artists {
				if _, err := transaction.Exec(
					"INSERT OR IGNORE INTO track_artists (track_id, position, artist_id, artist_name) VALUES (?, ?, ?, ?)",
					track.ID, artistPosition, artist.ID, artist.Name,
				); err != nil {
					return fmt.Errorf("cache artist %q: %w", artist.Name, err)
				}
			}
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit playlist cache update: %w", err)
	}
	return nil
}

type playlistCacheStatus struct {
	cachedAt    time.Time
	playlists   int
	tracks      int
	namedTracks int
}

// upToDate reports whether the cache can satisfy every query command: fresh
// enough, and not written by a version that predates track metadata.
func (status playlistCacheStatus) upToDate(maxAge time.Duration) bool {
	if status.cachedAt.IsZero() || time.Since(status.cachedAt) >= maxAge {
		return false
	}
	return status.tracks == 0 || status.namedTracks > 0
}

// readPlaylistCacheStatus reports a zero cachedAt for an uninitialized cache
// so callers can tell "never fetched" apart from "fetched and empty".
func readPlaylistCacheStatus(path string) (playlistCacheStatus, error) {
	database, err := openPlaylistCache(path)
	if err != nil {
		return playlistCacheStatus{}, err
	}
	defer database.Close()

	var status playlistCacheStatus
	var cachedAt string
	err = database.QueryRow("SELECT cached_at FROM playlist_cache_metadata WHERE id = 1").Scan(&cachedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return playlistCacheStatus{}, fmt.Errorf("query playlist cache metadata: %w", err)
	}
	status.cachedAt, err = time.Parse(time.RFC3339, cachedAt)
	if err != nil {
		return playlistCacheStatus{}, fmt.Errorf("parse playlist cache timestamp: %w", err)
	}
	if err := database.QueryRow(
		"SELECT (SELECT COUNT(*) FROM playlists), (SELECT COUNT(*) FROM playlist_tracks), (SELECT COUNT(*) FROM tracks)",
	).Scan(&status.playlists, &status.tracks, &status.namedTracks); err != nil {
		return playlistCacheStatus{}, fmt.Errorf("count playlist cache rows: %w", err)
	}
	return status, nil
}

// requireCachedAt returns the cache timestamp or the shared guidance error,
// and rejects caches written before track names and artists were stored.
func requireCachedAt(database *sql.DB, needTrackData bool) (string, error) {
	var cachedAt string
	if err := database.QueryRow(
		"SELECT cached_at FROM playlist_cache_metadata WHERE id = 1",
	).Scan(&cachedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("playlist cache is empty; run 'spotctl playlist cache'")
		}
		return "", fmt.Errorf("query playlist cache metadata: %w", err)
	}
	if needTrackData {
		var playlistTracks, namedTracks int
		if err := database.QueryRow(
			"SELECT (SELECT COUNT(*) FROM playlist_tracks), (SELECT COUNT(*) FROM tracks)",
		).Scan(&playlistTracks, &namedTracks); err != nil {
			return "", fmt.Errorf("count playlist cache rows: %w", err)
		}
		if playlistTracks > 0 && namedTracks == 0 {
			return "", errors.New("playlist cache predates track metadata; run 'spotctl playlist cache'")
		}
	}
	return cachedAt, nil
}

func queryPlaylistContains(path string, trackIDs []string) (playlistContainsResults, error) {
	database, err := openPlaylistCache(path)
	if err != nil {
		return playlistContainsResults{}, err
	}
	defer database.Close()

	result := playlistContainsResults{Results: make([]playlistContainsResult, 0, len(trackIDs))}
	if result.CachedAt, err = requireCachedAt(database, false); err != nil {
		return playlistContainsResults{}, err
	}

	for _, trackID := range trackIDs {
		trackResult := playlistContainsResult{
			TrackID:   trackID,
			Playlists: make([]cachedPlaylistReference, 0),
		}
		trackResult.Playlists, err = queryTrackPlaylists(database, trackID)
		if err != nil {
			return playlistContainsResults{}, err
		}
		trackResult.Contains = len(trackResult.Playlists) > 0
		result.Results = append(result.Results, trackResult)
	}
	return result, nil
}

func queryTrackPlaylists(database *sql.DB, trackID string) ([]cachedPlaylistReference, error) {
	rows, err := database.Query(`
		SELECT playlists.id, playlists.name
		FROM playlist_tracks
		JOIN playlists ON playlists.id = playlist_tracks.playlist_id
		WHERE playlist_tracks.track_id = ?
		GROUP BY playlists.id, playlists.name
		ORDER BY playlists.name, playlists.id
	`, trackID)
	if err != nil {
		return nil, fmt.Errorf("query playlist tracks: %w", err)
	}
	defer rows.Close()

	references := make([]cachedPlaylistReference, 0)
	for rows.Next() {
		var playlist cachedPlaylistReference
		if err := rows.Scan(&playlist.ID, &playlist.Name); err != nil {
			return nil, fmt.Errorf("scan playlist match: %w", err)
		}
		references = append(references, playlist)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate playlist matches: %w", err)
	}
	return references, nil
}

func queryPlaylistArtists(path string, queries []string) (playlistArtistsResults, error) {
	database, err := openPlaylistCache(path)
	if err != nil {
		return playlistArtistsResults{}, err
	}
	defer database.Close()

	result := playlistArtistsResults{}
	if result.CachedAt, err = requireCachedAt(database, true); err != nil {
		return playlistArtistsResults{}, err
	}

	if len(queries) == 0 {
		result.Artists, err = queryArtistStats(database, "")
		if err != nil {
			return playlistArtistsResults{}, err
		}
		result.Results = make([]struct {
			Query   string              `json:"query"`
			Artists []cachedArtistStats `json:"artists"`
		}, 0)
		return result, nil
	}
	for _, query := range queries {
		artists, err := queryArtistStats(database, query)
		if err != nil {
			return playlistArtistsResults{}, err
		}
		result.Results = append(result.Results, struct {
			Query   string              `json:"query"`
			Artists []cachedArtistStats `json:"artists"`
		}{Query: query, Artists: artists})
	}
	return result, nil
}

func queryArtistStats(database *sql.DB, filter string) ([]cachedArtistStats, error) {
	query := `
		SELECT track_artists.artist_id, track_artists.artist_name,
			COUNT(DISTINCT playlist_tracks.track_id),
			COUNT(DISTINCT playlist_tracks.playlist_id)
		FROM track_artists
		JOIN playlist_tracks ON playlist_tracks.track_id = track_artists.track_id
	`
	arguments := []any{}
	if filter != "" {
		query += " WHERE instr(lower(track_artists.artist_name), lower(?)) > 0"
		arguments = append(arguments, filter)
	}
	query += `
		GROUP BY track_artists.artist_id, track_artists.artist_name
		ORDER BY COUNT(DISTINCT playlist_tracks.track_id) DESC, track_artists.artist_name, track_artists.artist_id
	`
	rows, err := database.Query(query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query artist stats: %w", err)
	}
	defer rows.Close()

	artists := make([]cachedArtistStats, 0)
	for rows.Next() {
		var artist cachedArtistStats
		if err := rows.Scan(&artist.ID, &artist.Name, &artist.Tracks, &artist.Playlists); err != nil {
			return nil, fmt.Errorf("scan artist stats: %w", err)
		}
		artists = append(artists, artist)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artist stats: %w", err)
	}
	return artists, nil
}

func queryPlaylistStats(path string) (playlistStatsResult, error) {
	database, err := openPlaylistCache(path)
	if err != nil {
		return playlistStatsResult{}, err
	}
	defer database.Close()

	result := playlistStatsResult{Playlists: make([]playlistStatsEntry, 0)}
	if result.CachedAt, err = requireCachedAt(database, false); err != nil {
		return playlistStatsResult{}, err
	}

	rows, err := database.Query(`
		SELECT playlists.id, playlists.name, COUNT(playlist_tracks.track_id)
		FROM playlists
		LEFT JOIN playlist_tracks ON playlist_tracks.playlist_id = playlists.id
		GROUP BY playlists.id, playlists.name
		ORDER BY COUNT(playlist_tracks.track_id) DESC, playlists.name, playlists.id
	`)
	if err != nil {
		return playlistStatsResult{}, fmt.Errorf("query playlist stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var entry playlistStatsEntry
		if err := rows.Scan(&entry.ID, &entry.Name, &entry.Tracks); err != nil {
			return playlistStatsResult{}, fmt.Errorf("scan playlist stats: %w", err)
		}
		result.Playlists = append(result.Playlists, entry)
		result.TotalTracks += entry.Tracks
	}
	if err := rows.Err(); err != nil {
		return playlistStatsResult{}, fmt.Errorf("iterate playlist stats: %w", err)
	}

	if err := database.QueryRow(
		"SELECT COUNT(DISTINCT track_id) FROM playlist_tracks",
	).Scan(&result.DistinctTracks); err != nil {
		return playlistStatsResult{}, fmt.Errorf("count distinct tracks: %w", err)
	}
	return result, nil
}

func queryPlaylistSearch(path string, queries []string, limit int) (playlistSearchResults, error) {
	database, err := openPlaylistCache(path)
	if err != nil {
		return playlistSearchResults{}, err
	}
	defer database.Close()

	result := playlistSearchResults{}
	if result.CachedAt, err = requireCachedAt(database, true); err != nil {
		return playlistSearchResults{}, err
	}

	for _, query := range queries {
		trackIDs, err := searchTrackIDs(database, query, limit)
		if err != nil {
			return playlistSearchResults{}, err
		}
		matches := make([]cachedTrackMatch, 0, len(trackIDs))
		for _, trackID := range trackIDs {
			match, err := hydrateTrackMatch(database, trackID)
			if err != nil {
				return playlistSearchResults{}, err
			}
			matches = append(matches, match)
		}
		result.Results = append(result.Results, struct {
			Query  string             `json:"query"`
			Tracks []cachedTrackMatch `json:"tracks"`
		}{Query: query, Tracks: matches})
	}
	return result, nil
}

func searchTrackIDs(database *sql.DB, query string, limit int) ([]string, error) {
	rows, err := database.Query(`
		SELECT DISTINCT tracks.id, tracks.name
		FROM tracks
		LEFT JOIN track_artists ON track_artists.track_id = tracks.id
		WHERE instr(lower(tracks.name), lower(?)) > 0
			OR instr(lower(track_artists.artist_name), lower(?)) > 0
		ORDER BY tracks.name, tracks.id
		LIMIT ?
	`, query, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search cached tracks: %w", err)
	}
	defer rows.Close()

	var trackIDs []string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan track match: %w", err)
		}
		trackIDs = append(trackIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate track matches: %w", err)
	}
	return trackIDs, nil
}

func hydrateTrackMatch(database *sql.DB, trackID string) (cachedTrackMatch, error) {
	match := cachedTrackMatch{ID: trackID, Artists: make([]string, 0)}
	if err := database.QueryRow("SELECT name FROM tracks WHERE id = ?", trackID).Scan(&match.Name); err != nil {
		return cachedTrackMatch{}, fmt.Errorf("load track %q: %w", trackID, err)
	}
	rows, err := database.Query(
		"SELECT artist_name FROM track_artists WHERE track_id = ? ORDER BY position", trackID,
	)
	if err != nil {
		return cachedTrackMatch{}, fmt.Errorf("load track artists: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return cachedTrackMatch{}, fmt.Errorf("scan track artist: %w", err)
		}
		match.Artists = append(match.Artists, name)
	}
	if err := rows.Err(); err != nil {
		return cachedTrackMatch{}, fmt.Errorf("iterate track artists: %w", err)
	}
	if match.Playlists, err = queryTrackPlaylists(database, trackID); err != nil {
		return cachedTrackMatch{}, err
	}
	return match, nil
}

// queryPlaylistSample draws each pick by choosing a playlist uniformly at
// random first, then a track within it — so large playlists do not dominate
// the sample the way track-uniform sampling would.
func queryPlaylistSample(path string, limit int, playlistFilter string) (playlistSampleResult, error) {
	database, err := openPlaylistCache(path)
	if err != nil {
		return playlistSampleResult{}, err
	}
	defer database.Close()

	result := playlistSampleResult{Tracks: make([]sampledTrack, 0, limit)}
	if result.CachedAt, err = requireCachedAt(database, true); err != nil {
		return playlistSampleResult{}, err
	}

	query := `
		SELECT playlists.id, playlists.name, playlist_tracks.track_id
		FROM playlists
		JOIN playlist_tracks ON playlist_tracks.playlist_id = playlists.id
	`
	arguments := []any{}
	if playlistFilter != "" {
		query += " WHERE instr(lower(playlists.name), lower(?)) > 0"
		arguments = append(arguments, playlistFilter)
	}
	rows, err := database.Query(query, arguments...)
	if err != nil {
		return playlistSampleResult{}, fmt.Errorf("load playlist tracks: %w", err)
	}
	defer rows.Close()

	playlistNames := map[string]string{}
	playlistTracks := map[string][]string{}
	for rows.Next() {
		var playlistID, playlistName, trackID string
		if err := rows.Scan(&playlistID, &playlistName, &trackID); err != nil {
			return playlistSampleResult{}, fmt.Errorf("scan playlist track: %w", err)
		}
		playlistNames[playlistID] = playlistName
		playlistTracks[playlistID] = append(playlistTracks[playlistID], trackID)
	}
	if err := rows.Err(); err != nil {
		return playlistSampleResult{}, fmt.Errorf("iterate playlist tracks: %w", err)
	}
	if len(playlistTracks) == 0 {
		if playlistFilter != "" {
			return playlistSampleResult{}, fmt.Errorf("no cached playlists match %q", playlistFilter)
		}
		return playlistSampleResult{}, errors.New("no cached playlist tracks to sample")
	}

	playlistIDs := make([]string, 0, len(playlistTracks))
	for id := range playlistTracks {
		playlistIDs = append(playlistIDs, id)
	}

	picked := map[string]bool{}
	for attempts := 0; len(result.Tracks) < limit && attempts < limit*20; attempts++ {
		playlistID := playlistIDs[rand.IntN(len(playlistIDs))]
		trackIDs := playlistTracks[playlistID]
		trackID := trackIDs[rand.IntN(len(trackIDs))]
		if picked[trackID] {
			continue
		}
		picked[trackID] = true
		match, err := hydrateTrackMatch(database, trackID)
		if err != nil {
			return playlistSampleResult{}, err
		}
		result.Tracks = append(result.Tracks, sampledTrack{
			ID:      trackID,
			Name:    match.Name,
			Artists: match.Artists,
			Playlist: cachedPlaylistReference{
				ID:   playlistID,
				Name: playlistNames[playlistID],
			},
		})
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
