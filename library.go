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
	"strconv"
	"strings"
	"time"
)

var errSavedTracksEmpty = errors.New("saved tracks cache is uninitialized")

func runLibrary(args []string) error {
	if len(args) == 0 || args[0] != "tracks" {
		return errors.New("usage: spotctl library tracks [--db PATH] [--full] [--refresh] [--limit N] [--offset N]")
	}
	flags := flag.NewFlagSet("library tracks", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	databasePath := flags.String("db", "", "SQLite cache path")
	full := flags.Bool("full", false, "return Spotify's complete saved-track items")
	refresh := flags.Bool("refresh", false, "fetch from Spotify and update the cache before answering")
	limit := flags.Int("limit", 0, "page size (0 reads the whole cache in one page)")
	offset := flags.Int("offset", 0, "result offset (0 or greater)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *limit < 0 || *offset < 0 {
		return errors.New("library tracks limit and offset must be 0 or greater")
	}
	path, err := resolvePlaylistCachePath(*databasePath)
	if err != nil {
		return err
	}
	if !*refresh {
		page, err := querySavedTracks(path, *full, *limit, *offset)
		if err == nil {
			return writeJSON(page)
		}
		if !errors.Is(err, errSavedTracksEmpty) {
			return err
		}
	}
	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	items, collection, err := fetchSavedTracks(client)
	if err != nil {
		return err
	}
	if err := replaceSavedTracks(path, items, collection, time.Now().UTC()); err != nil {
		return err
	}
	page, err := querySavedTracks(path, *full, *limit, *offset)
	if err != nil {
		return err
	}
	page.Source = "api"
	return writeJSON(page)
}

func fetchSavedTracks(client *spotifyClient) ([]json.RawMessage, string, error) {
	items := []json.RawMessage{}
	collection := ""
	for offset := 0; ; {
		data, err := requestWithRetry(client, http.MethodGet, "/me/tracks", url.Values{
			"limit": {"50"}, "offset": {strconv.Itoa(offset)},
		}, nil)
		if err != nil {
			return nil, "", fmt.Errorf("fetch saved tracks: %w", err)
		}
		var page rawPaging
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, "", fmt.Errorf("decode saved tracks: %w", err)
		}
		if page.Items == nil {
			return nil, "", errors.New("decode saved tracks: missing items array")
		}
		if collection == "" {
			collection = strings.SplitN(page.Href, "?", 2)[0]
		}
		items = append(items, page.Items...)
		if page.Next == nil || *page.Next == "" {
			break
		}
		if len(page.Items) == 0 {
			return nil, "", errors.New("Spotify returned an empty saved tracks page with a next page")
		}
		offset += len(page.Items)
	}
	return items, collection, nil
}

// Saved items are independent of playlist tracks: replacing either collection
// must not delete the other or advance its freshness marker.
func replaceSavedTracks(path string, items []json.RawMessage, collection string, cachedAt time.Time) error {
	database, err := openPlaylistCache(path)
	if err != nil {
		return err
	}
	defer database.Close()
	tx, err := database.Begin()
	if err != nil {
		return fmt.Errorf("begin saved tracks update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM saved_tracks"); err != nil {
		return fmt.Errorf("clear saved tracks: %w", err)
	}
	for position, item := range items {
		if _, err := tx.Exec("INSERT INTO saved_tracks (position, payload) VALUES (?, ?)", position, string(item)); err != nil {
			return fmt.Errorf("cache saved track: %w", err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO saved_tracks_metadata (id, cached_at, href) VALUES (1, ?, ?)
 ON CONFLICT(id) DO UPDATE SET cached_at = excluded.cached_at, href = excluded.href`, cachedAt.Format(time.RFC3339), collection); err != nil {
		return fmt.Errorf("update saved tracks metadata: %w", err)
	}
	return tx.Commit()
}

func querySavedTracks(path string, full bool, limit, offset int) (pagingEnvelope, error) {
	database, err := openPlaylistCache(path)
	if err != nil {
		return pagingEnvelope{}, err
	}
	defer database.Close()
	var cachedAt, collection string
	err = database.QueryRow("SELECT cached_at, href FROM saved_tracks_metadata WHERE id = 1").Scan(&cachedAt, &collection)
	if errors.Is(err, sql.ErrNoRows) {
		return pagingEnvelope{}, errSavedTracksEmpty
	}
	if err != nil {
		return pagingEnvelope{}, fmt.Errorf("query saved tracks metadata: %w", err)
	}
	var total int
	if err := database.QueryRow("SELECT COUNT(*) FROM saved_tracks").Scan(&total); err != nil {
		return pagingEnvelope{}, fmt.Errorf("count saved tracks: %w", err)
	}
	if limit == 0 {
		limit = total
	}
	rows, err := database.Query("SELECT payload FROM saved_tracks ORDER BY position LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return pagingEnvelope{}, fmt.Errorf("query saved tracks: %w", err)
	}
	defer rows.Close()
	payloads := []json.RawMessage{}
	trimmed := []minimalTrack{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return pagingEnvelope{}, fmt.Errorf("scan saved track: %w", err)
		}
		if full {
			payloads = append(payloads, json.RawMessage(payload))
			continue
		}
		var item struct {
			Track spotifyTrackObject `json:"track"`
		}
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return pagingEnvelope{}, fmt.Errorf("decode saved track: %w", err)
		}
		trimmed = append(trimmed, trimTrack(item.Track))
	}
	if err := rows.Err(); err != nil {
		return pagingEnvelope{}, fmt.Errorf("iterate saved tracks: %w", err)
	}
	href, next, previous := pageLinks(collection, limit, offset, total)
	page := pagingEnvelope{Href: href, Next: next, Previous: previous, Limit: limit, Offset: offset, Total: total, Source: "cache", CachedAt: cachedAt}
	if full {
		page.Items = payloads
	} else {
		page.Items = trimmed
	}
	return page, nil
}
