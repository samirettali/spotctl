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

// The library is the user's Liked Songs, GET /me/tracks: a different set from
// the tracks inside playlists, which is all the playlist cache holds. It gets
// its own table and its own freshness marker, and never references `tracks`,
// because `playlist cache` clears that table wholesale and a cascade or a
// foreign key would make one refresh break the other.

// savedTrack is one Liked Songs entry as Spotify sent it: the item object
// (`{added_at, track}`) kept verbatim so --full costs no extra column.
type savedTrack struct {
	ID      string
	AddedAt string
	Payload json.RawMessage
}

type savedTrackEntry struct {
	AddedAt string `json:"added_at"`
	Track   *struct {
		ID string `json:"id"`
	} `json:"track"`
}

var errLibraryEmpty = errors.New("library cache is empty; run 'spotctl library tracks --refresh'")

func runLibrary(args []string) error {
	if len(args) == 0 || args[0] != "tracks" {
		return errors.New("usage: spotctl library tracks [--db PATH] [--full] [--refresh] [--limit N] [--offset N]")
	}
	return libraryTracks(args[1:])
}

// libraryTracks answers from the cache like `playlist list`, for the same
// reason: it sits behind pickers, where one stray round trip is the whole
// problem. --refresh fetches, and an unpopulated cache fetches on its own.
func libraryTracks(args []string) error {
	flags := flag.NewFlagSet("library tracks", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	databasePath := flags.String("db", "", "SQLite cache path")
	full := flags.Bool("full", false, "return Spotify's complete items instead of the trimmed ones")
	refresh := flags.Bool("refresh", false, "fetch from Spotify and update the cache before answering")
	limit := flags.Int("limit", 0, "page size (0 reads the whole library in one page)")
	offset := flags.Int("offset", 0, "result offset (0 or greater)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: spotctl library tracks [--db PATH] [--full] [--refresh] [--limit N] [--offset N]")
	}
	if *limit < 0 || *offset < 0 {
		return errors.New("library tracks limit and offset must be 0 or greater")
	}

	path, err := resolvePlaylistCachePath(*databasePath)
	if err != nil {
		return err
	}
	if !*refresh {
		envelope, err := queryCachedSavedTracks(path, *full, *limit, *offset)
		switch {
		case err == nil:
			return writeJSON(envelope)
		case errors.Is(err, errLibraryEmpty):
		default:
			return err
		}
	}

	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	tracks, collection, err := fetchSavedTracks(client)
	if err != nil {
		return err
	}
	if err := replaceSavedTracks(path, tracks, collection, time.Now().UTC()); err != nil {
		return err
	}
	envelope, err := queryCachedSavedTracks(path, *full, *limit, *offset)
	if err != nil {
		return err
	}
	envelope.Source = "api"
	return writeJSON(envelope)
}

// fetchSavedTracks pages /me/tracks to the end. Spotify caps the page at 50.
func fetchSavedTracks(client *spotifyClient) ([]savedTrack, string, error) {
	tracks := []savedTrack{}
	collection := ""
	offset := 0
	for {
		data, err := client.request(http.MethodGet, "/me/tracks", url.Values{
			"limit":  {"50"},
			"offset": {strconv.Itoa(offset)},
		}, nil)
		if err != nil {
			return nil, "", fmt.Errorf("fetch saved tracks: %w", err)
		}
		var page struct {
			Href  string            `json:"href"`
			Items []json.RawMessage `json:"items"`
			Next  *string           `json:"next"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, "", fmt.Errorf("decode saved tracks: %w", err)
		}
		if collection == "" && page.Href != "" {
			collection = strings.SplitN(page.Href, "?", 2)[0]
		}
		for _, raw := range page.Items {
			var entry savedTrackEntry
			if err := json.Unmarshal(raw, &entry); err != nil {
				return nil, "", fmt.Errorf("decode saved track: %w", err)
			}
			if entry.Track == nil || entry.Track.ID == "" {
				continue
			}
			tracks = append(tracks, savedTrack{ID: entry.Track.ID, AddedAt: entry.AddedAt, Payload: raw})
		}
		if page.Next == nil || *page.Next == "" {
			break
		}
		if len(page.Items) == 0 {
			return nil, "", errors.New("Spotify returned an empty saved tracks page with a next page")
		}
		offset += len(page.Items)
	}
	return tracks, collection, nil
}

// replaceSavedTracks swaps the whole library in one transaction, in the order
// Spotify returned it (most recently saved first).
func replaceSavedTracks(path string, tracks []savedTrack, collection string, cachedAt time.Time) error {
	database, err := openPlaylistCache(path)
	if err != nil {
		return err
	}
	defer database.Close()

	transaction, err := database.Begin()
	if err != nil {
		return fmt.Errorf("begin library update: %w", err)
	}
	defer transaction.Rollback()

	if _, err := transaction.Exec("DELETE FROM saved_tracks"); err != nil {
		return fmt.Errorf("clear library cache: %w", err)
	}
	for position, track := range tracks {
		if _, err := transaction.Exec(
			"INSERT INTO saved_tracks (position, track_id, added_at, payload) VALUES (?, ?, ?, ?)",
			position, track.ID, track.AddedAt, string(track.Payload),
		); err != nil {
			return fmt.Errorf("cache saved track: %w", err)
		}
	}
	if _, err := transaction.Exec(
		"INSERT INTO saved_tracks_metadata (id, cached_at, href) VALUES (1, ?, ?) ON CONFLICT(id) DO UPDATE SET cached_at = excluded.cached_at, href = COALESCE(NULLIF(excluded.href, ''), saved_tracks_metadata.href)",
		cachedAt.Format(time.RFC3339), collection,
	); err != nil {
		return fmt.Errorf("update library metadata: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit library update: %w", err)
	}
	return nil
}

// queryCachedSavedTracks serves `library tracks`. limit <= 0 means the whole
// library in one page, as with the playlist reads.
func queryCachedSavedTracks(path string, full bool, limit, offset int) (pagingEnvelope, error) {
	database, err := openPlaylistCache(path)
	if err != nil {
		return pagingEnvelope{}, err
	}
	defer database.Close()

	var cachedAt string
	var href sql.NullString
	err = database.QueryRow("SELECT cached_at, href FROM saved_tracks_metadata WHERE id = 1").Scan(&cachedAt, &href)
	if errors.Is(err, sql.ErrNoRows) {
		return pagingEnvelope{}, errLibraryEmpty
	}
	if err != nil {
		return pagingEnvelope{}, fmt.Errorf("query library metadata: %w", err)
	}

	var total int
	if err := database.QueryRow("SELECT COUNT(*) FROM saved_tracks").Scan(&total); err != nil {
		return pagingEnvelope{}, fmt.Errorf("count cached saved tracks: %w", err)
	}
	if limit <= 0 {
		limit = total
	}

	rows, err := database.Query(
		"SELECT payload FROM saved_tracks ORDER BY position LIMIT ? OFFSET ?", limit, offset,
	)
	if err != nil {
		return pagingEnvelope{}, fmt.Errorf("query cached saved tracks: %w", err)
	}
	defer rows.Close()

	payloads := []json.RawMessage{}
	trimmed := []minimalTrack{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return pagingEnvelope{}, fmt.Errorf("scan cached saved track: %w", err)
		}
		if full {
			payloads = append(payloads, json.RawMessage(payload))
			continue
		}
		var item struct {
			Track spotifyTrackObject `json:"track"`
		}
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return pagingEnvelope{}, fmt.Errorf("decode cached saved track: %w", err)
		}
		trimmed = append(trimmed, trimTrack(item.Track))
	}
	if err := rows.Err(); err != nil {
		return pagingEnvelope{}, fmt.Errorf("iterate cached saved tracks: %w", err)
	}

	collection := href.String
	if collection == "" {
		collection = spotifyAPIBase + "/me/tracks"
	}
	pageHref, next, previous := pageLinks(collection, limit, offset, total)
	envelope := pagingEnvelope{
		Href: pageHref, Limit: limit, Next: next, Offset: offset, Previous: previous,
		Total: total, Source: "cache", CachedAt: cachedAt,
	}
	if full {
		envelope.Items = payloads
	} else {
		envelope.Items = trimmed
	}
	return envelope, nil
}
