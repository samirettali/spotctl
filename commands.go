package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func runSearch(args []string) error {
	flags := flag.NewFlagSet("search", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	itemType := flags.String("type", "track", "track, album, artist, or playlist")
	limit := flags.Int("limit", 20, "number of results (1-50, Spotify's maximum)")
	offset := flags.Int("offset", 0, "result offset (0-1000, Spotify's maximum)")
	full := flags.Bool("full", false, "return Spotify's complete payload instead of the trimmed shape")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return errors.New("usage: spotctl search [--type TYPE] [--limit N] [--offset N] QUERY")
	}
	validTypes := map[string]bool{"track": true, "album": true, "artist": true, "playlist": true}
	if !validTypes[*itemType] {
		return fmt.Errorf("unsupported search type %q", *itemType)
	}
	if *limit < 1 || *limit > 50 {
		return errors.New("search limit must be between 1 and 50 (Spotify's maximum)")
	}
	if *offset < 0 || *offset > 1000 {
		return errors.New("search offset must be between 0 and 1000 (Spotify's maximum)")
	}

	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	return outputTrimmed(client, http.MethodGet, "/search", url.Values{
		"q":      {strings.Join(flags.Args(), " ")},
		"type":   {*itemType},
		"limit":  {strconv.Itoa(*limit)},
		"offset": {strconv.Itoa(*offset)},
	}, nil, *full, func(data []byte) (any, error) {
		return trimSearch(data, *itemType)
	})
}

// resolveConcurrency bounds the fan-out. Spotify has no batch search endpoint,
// so one query is one request; firing every query at once only earns 429s that
// requestWithRetry then has to sleep off.
const resolveConcurrency = 5

type resolveMatch struct {
	Query  string `json:"query"`
	Tracks any    `json:"tracks"`
	Error  string `json:"error,omitempty"`
}

type resolveResult struct {
	Results []resolveMatch `json:"results"`
}

func runResolve(args []string) error {
	flags := flag.NewFlagSet("resolve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	limit := flags.Int("limit", 1, "matches to return per query (1-50, Spotify's maximum)")
	full := flags.Bool("full", false, "return Spotify's complete payload instead of the trimmed shape")
	if err := flags.Parse(args); err != nil {
		return err
	}
	queries := flags.Args()
	if len(queries) == 0 {
		return errors.New("usage: spotctl resolve [--limit N] [--full] QUERY...")
	}
	if *limit < 1 || *limit > 50 {
		return errors.New("resolve limit must be between 1 and 50 (Spotify's maximum)")
	}
	for _, query := range queries {
		if strings.TrimSpace(query) == "" {
			return errors.New("resolve queries must not be empty")
		}
	}

	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	return writeJSON(resolveTracks(client, queries, *limit, *full))
}

// resolveTracks searches every query concurrently and keeps the results in the
// order they were given, so a caller can zip them back against its own list.
// A query that fails carries its error instead of aborting the batch, matching
// queue add.
//
// Sharing one client across goroutines is safe because newSpotifyClient has
// already refreshed the token: nothing mutates the credentials from here on.
func resolveTracks(client *spotifyClient, queries []string, limit int, full bool) resolveResult {
	matches := make([]resolveMatch, len(queries))
	slots := make(chan struct{}, resolveConcurrency)
	var wait sync.WaitGroup
	for index, query := range queries {
		wait.Add(1)
		go func() {
			defer wait.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			matches[index] = resolveOne(client, query, limit, full)
		}()
	}
	wait.Wait()
	return resolveResult{Results: matches}
}

func resolveOne(client *spotifyClient, query string, limit int, full bool) resolveMatch {
	match := resolveMatch{Query: query, Tracks: []minimalTrack{}}
	data, err := requestWithRetry(client, http.MethodGet, "/search", url.Values{
		"q":     {query},
		"type":  {"track"},
		"limit": {strconv.Itoa(limit)},
	}, nil)
	if err != nil {
		match.Error = err.Error()
		return match
	}
	tracks, err := resolveItems(data, full)
	if err != nil {
		match.Error = err.Error()
		return match
	}
	match.Tracks = tracks
	return match
}

func resolveItems(data []byte, full bool) (any, error) {
	var body struct {
		Tracks struct {
			Items []json.RawMessage `json:"items"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("decode search results: %w", err)
	}
	if full {
		if body.Tracks.Items == nil {
			return []json.RawMessage{}, nil
		}
		return body.Tracks.Items, nil
	}
	items := make([]minimalTrack, 0, len(body.Tracks.Items))
	for _, raw := range body.Tracks.Items {
		var track spotifyTrackObject
		if err := json.Unmarshal(raw, &track); err != nil {
			return nil, fmt.Errorf("decode track: %w", err)
		}
		items = append(items, trimTrack(track))
	}
	return items, nil
}

func runTop(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: spotctl top tracks|artists [--time-range RANGE] [--limit N] [--offset N]")
	}

	itemType := args[0]
	if itemType != "tracks" && itemType != "artists" {
		return fmt.Errorf("unsupported top item type %q", itemType)
	}

	flags := flag.NewFlagSet("top "+itemType, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	timeRange := flags.String("time-range", "medium_term", "short_term, medium_term, or long_term")
	limit := flags.Int("limit", 20, "number of items (1-50)")
	offset := flags.Int("offset", 0, "result offset (0 or greater)")
	full := flags.Bool("full", false, "return Spotify's complete payload instead of the trimmed shape")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	validTimeRanges := map[string]bool{"short_term": true, "medium_term": true, "long_term": true}
	if !validTimeRanges[*timeRange] {
		return fmt.Errorf("unsupported time range %q", *timeRange)
	}
	if *limit < 1 || *limit > 50 {
		return errors.New("top items limit must be between 1 and 50")
	}
	if *offset < 0 {
		return errors.New("top items offset must be 0 or greater")
	}

	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	return outputTrimmed(client, http.MethodGet, "/me/top/"+itemType, url.Values{
		"time_range": {*timeRange},
		"limit":      {strconv.Itoa(*limit)},
		"offset":     {strconv.Itoa(*offset)},
	}, nil, *full, func(data []byte) (any, error) {
		return trimTop(data, itemType)
	})
}

func runHistory(args []string) error {
	if len(args) == 0 || args[0] != "recent" {
		return errors.New("usage: spotctl history recent [--limit N] [--before UNIX_MS | --after UNIX_MS]")
	}

	flags := flag.NewFlagSet("history recent", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	limit := flags.Int("limit", 20, "number of tracks (1-50)")
	before := flags.Int64("before", 0, "return tracks played before this Unix timestamp in milliseconds")
	after := flags.Int64("after", 0, "return tracks played after this Unix timestamp in milliseconds")
	full := flags.Bool("full", false, "return Spotify's complete payload instead of the trimmed shape")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *limit < 1 || *limit > 50 {
		return errors.New("recent history limit must be between 1 and 50")
	}
	if *before < 0 || *after < 0 {
		return errors.New("history timestamps must be 0 or greater")
	}
	if *before != 0 && *after != 0 {
		return errors.New("--before and --after cannot be used together")
	}

	query := url.Values{"limit": {strconv.Itoa(*limit)}}
	if *before != 0 {
		query.Set("before", strconv.FormatInt(*before, 10))
	}
	if *after != 0 {
		query.Set("after", strconv.FormatInt(*after, 10))
	}

	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	return outputTrimmed(client, http.MethodGet, "/me/player/recently-played", query, nil, *full, trimHistoryResponse)
}

func runDevice(args []string) error {
	if len(args) == 0 || args[0] != "list" {
		return errors.New("usage: spotctl device list [--full]")
	}
	flags := flag.NewFlagSet("device list", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	full := flags.Bool("full", false, "return Spotify's complete payload instead of the trimmed shape")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: spotctl device list [--full]")
	}
	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	return outputTrimmed(client, http.MethodGet, "/me/player/devices", nil, nil, *full, trimDevicesResponse)
}

func runPlay(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: spotctl play track|episode|album|artist|playlist [--device ID] ITEM")
	}

	itemType := args[0]
	validTypes := map[string]bool{
		"track": true, "episode": true, "album": true, "artist": true, "playlist": true,
	}
	if !validTypes[itemType] {
		return fmt.Errorf("unsupported playback type %q", itemType)
	}

	flags := flag.NewFlagSet("play", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	device := flags.String("device", "", "Spotify Connect device ID")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: spotctl play TYPE [--device ID] ITEM")
	}

	uri, err := spotifyURI(flags.Arg(0), itemType)
	if err != nil {
		return err
	}
	query := url.Values{}
	if *device != "" {
		query.Set("device_id", *device)
	}

	body := map[string]any{"context_uri": uri}
	if itemType == "track" || itemType == "episode" {
		body = map[string]any{"uris": []string{uri}}
	}

	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	return outputRequest(client, http.MethodPut, "/me/player/play", query, body)
}

func runQueue(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: spotctl queue get|add")
	}
	client, err := newSpotifyClient()
	if err != nil {
		return err
	}

	switch args[0] {
	case "get":
		flags := flag.NewFlagSet("queue get", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		full := flags.Bool("full", false, "return Spotify's complete payload instead of the trimmed shape")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: spotctl queue get [--full]")
		}
		return outputTrimmed(client, http.MethodGet, "/me/player/queue", nil, nil, *full, trimQueueResponse)
	case "add":
		flags := flag.NewFlagSet("queue add", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		device := flags.String("device", "", "Spotify Connect device ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() == 0 {
			return errors.New("usage: spotctl queue add [--device ID] ITEM...")
		}
		uris := make([]string, 0, flags.NArg())
		for _, item := range flags.Args() {
			uri, err := spotifyURI(item, "track")
			if err != nil {
				return err
			}
			uris = append(uris, uri)
		}
		return writeJSON(queueAddItems(client, uris, *device))
	default:
		return fmt.Errorf("unknown queue command %q", args[0])
	}
}

type queueAddFailure struct {
	URI   string `json:"uri"`
	Error string `json:"error"`
}

type queueAddResult struct {
	Queued int               `json:"queued"`
	Failed []queueAddFailure `json:"failed"`
}

// queueAddItems queues each URI in order with per-item 429 retry. Spotify's
// queue endpoint accepts one URI per request, so this is a sequential loop;
// items that still fail after all retries are collected in Failed rather than
// aborting the batch.
func queueAddItems(client *spotifyClient, uris []string, device string) queueAddResult {
	result := queueAddResult{Failed: []queueAddFailure{}}
	query := url.Values{}
	if device != "" {
		query.Set("device_id", device)
	}
	for _, uri := range uris {
		query.Set("uri", uri)
		if _, err := requestWithRetry(client, http.MethodPost, "/me/player/queue", query, nil); err != nil {
			result.Failed = append(result.Failed, queueAddFailure{URI: uri, Error: err.Error()})
			continue
		}
		result.Queued++
	}
	return result
}

func runPlaylist(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: spotctl playlist list|get|items|create|update|add|remove|delete|cache|contains|artists|stats|search|sample")
	}
	switch args[0] {
	case "contains":
		return playlistContains(args[1:])
	case "cache":
		return playlistCache(args[1:])
	case "artists":
		return playlistArtists(args[1:])
	case "stats":
		return playlistStats(args[1:])
	case "search":
		return playlistSearch(args[1:])
	case "sample":
		return playlistSample(args[1:])
	case "list":
		return playlistList(args[1:])
	case "get":
		return playlistGet(args[1:])
	case "items":
		return playlistGetItems(args[1:])
	}

	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	switch args[0] {
	case "create":
		return playlistCreate(client, args[1:])
	case "update":
		return playlistUpdate(client, args[1:])
	case "add":
		return playlistItems(client, http.MethodPost, args[1:])
	case "remove":
		return playlistItems(client, http.MethodDelete, args[1:])
	case "delete":
		if len(args) != 2 {
			return errors.New("usage: spotctl playlist delete PLAYLIST")
		}
		return outputRequest(client, http.MethodDelete, "/playlists/"+spotifyID(args[1], "playlist")+"/followers", nil, nil)
	default:
		return fmt.Errorf("unknown playlist command %q", args[0])
	}
}

// playlistList answers from the cache unless told otherwise, because the point
// of it is to be instant. There is deliberately no age check: a freshness test
// that reaches the network would put back the latency the cache exists to
// remove. New playlists appear after a --refresh or a `playlist cache`.
func playlistList(args []string) error {
	flags := flag.NewFlagSet("playlist list", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	databasePath := flags.String("db", "", "SQLite cache path")
	full := flags.Bool("full", false, "return Spotify's complete objects instead of the trimmed ones")
	refresh := flags.Bool("refresh", false, "fetch from Spotify and update the cache before answering")
	limit := flags.Int("limit", 0, "page size (0 reads the whole cache in one page)")
	offset := flags.Int("offset", 0, "result offset (0 or greater)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: spotctl playlist list [--db PATH] [--full] [--refresh] [--limit N] [--offset N]")
	}
	if *limit < 0 || *offset < 0 {
		return errors.New("playlist list limit and offset must be 0 or greater")
	}

	path, err := resolvePlaylistCachePath(*databasePath)
	if err != nil {
		return err
	}

	if !*refresh {
		envelope, _, err := queryCachedPlaylists(path, *full, *limit, *offset)
		switch {
		case err == nil:
			return writeJSON(envelope)
		case errors.Is(err, errCacheEmpty):
			// never populated: fall through and fetch, so the command always
			// works rather than telling the caller to go run something else
		default:
			return err
		}
	}

	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	playlists, collection, err := fetchPlaylistSummaries(client)
	if err != nil {
		return err
	}
	if err := upsertPlaylistSummaries(path, playlists, collection, time.Now().UTC()); err != nil {
		return err
	}
	envelope, _, err := queryCachedPlaylists(path, *full, *limit, *offset)
	if err != nil {
		return err
	}
	envelope.Source = "api"
	return writeJSON(envelope)
}

// playlistGet returns the playlist object itself, not a paging envelope, which
// is the shape Spotify uses for this endpoint.
func playlistGet(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: spotctl playlist get PLAYLIST [--db PATH] [--full] [--refresh]")
	}
	playlistID := spotifyID(args[0], "playlist")
	flags := flag.NewFlagSet("playlist get", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	databasePath := flags.String("db", "", "SQLite cache path")
	full := flags.Bool("full", false, "return Spotify's complete object instead of the trimmed one")
	refresh := flags.Bool("refresh", false, "fetch from Spotify before answering")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: spotctl playlist get PLAYLIST [--db PATH] [--full] [--refresh]")
	}

	path, err := resolvePlaylistCachePath(*databasePath)
	if err != nil {
		return err
	}
	if !*refresh {
		playlist, _, err := queryCachedPlaylist(path, playlistID, *full)
		switch {
		case err == nil:
			return writeJSON(playlist)
		case errors.Is(err, errCacheEmpty):
		default:
			return err
		}
	}

	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	data, err := client.request(http.MethodGet, "/playlists/"+playlistID, nil, nil)
	if err != nil {
		return err
	}
	if *full {
		return writeJSON(data)
	}
	var object spotifyPlaylistObject
	if err := json.Unmarshal(data, &object); err != nil {
		return fmt.Errorf("decode playlist: %w", err)
	}
	return writeJSON(trimPlaylist(object, true))
}

func playlistGetItems(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: spotctl playlist items PLAYLIST [--db PATH] [--full] [--refresh] [--limit N] [--offset N]")
	}
	playlistID := spotifyID(args[0], "playlist")
	flags := flag.NewFlagSet("playlist items", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	databasePath := flags.String("db", "", "SQLite cache path")
	full := flags.Bool("full", false, "return Spotify's complete items instead of the trimmed ones")
	refresh := flags.Bool("refresh", false, "fetch from Spotify and update the cache before answering")
	limit := flags.Int("limit", 0, "page size (0 reads the whole playlist in one page)")
	offset := flags.Int("offset", 0, "result offset (0 or greater)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: spotctl playlist items PLAYLIST [--db PATH] [--full] [--refresh] [--limit N] [--offset N]")
	}
	if *limit < 0 || *offset < 0 {
		return errors.New("playlist items limit and offset must be 0 or greater")
	}

	path, err := resolvePlaylistCachePath(*databasePath)
	if err != nil {
		return err
	}
	if !*refresh {
		envelope, err := queryCachedPlaylistItems(path, playlistID, *full, *limit, *offset)
		switch {
		case err == nil:
			return writeJSON(envelope)
		case errors.Is(err, errCacheEmpty):
		default:
			return err
		}
	}

	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	tracks, err := fetchAllPlaylistTracks(client, playlistID)
	if err != nil {
		return err
	}
	if err := replacePlaylistTracks(path, playlistID, tracks); err != nil {
		return err
	}
	envelope, err := queryCachedPlaylistItems(path, playlistID, *full, *limit, *offset)
	if err != nil {
		return err
	}
	envelope.Source = "api"
	return writeJSON(envelope)
}

func playlistCreate(client *spotifyClient, args []string) error {
	flags := flag.NewFlagSet("playlist create", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	name := flags.String("name", "", "playlist name")
	description := flags.String("description", "", "playlist description")
	public := flags.Bool("public", false, "make the playlist public")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *name == "" || flags.NArg() != 0 {
		return errors.New("usage: spotctl playlist create --name NAME [--description TEXT] [--public]")
	}
	body := map[string]any{"name": *name, "description": *description, "public": *public}
	return outputRequest(client, http.MethodPost, "/me/playlists", nil, body)
}

func playlistUpdate(client *spotifyClient, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: spotctl playlist update PLAYLIST [options]")
	}
	playlistID := spotifyID(args[0], "playlist")
	flags := flag.NewFlagSet("playlist update", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	name := flags.String("name", "", "playlist name")
	description := flags.String("description", "", "playlist description")
	var public optionalBool
	var collaborative optionalBool
	flags.Var(&public, "public", "true or false")
	flags.Var(&collaborative, "collaborative", "true or false")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}

	set := map[string]bool{}
	flags.Visit(func(current *flag.Flag) { set[current.Name] = true })
	body := map[string]any{}
	if set["name"] {
		body["name"] = *name
	}
	if set["description"] {
		body["description"] = *description
	}
	if public.set {
		body["public"] = public.value
	}
	if collaborative.set {
		body["collaborative"] = collaborative.value
	}
	if len(body) == 0 {
		return errors.New("at least one playlist field must be provided")
	}
	return outputRequest(client, http.MethodPut, "/playlists/"+playlistID, nil, body)
}

func playlistItems(client *spotifyClient, method string, args []string) error {
	if len(args) < 2 {
		return errors.New("playlist add/remove requires a playlist and at least one item")
	}
	if len(args)-1 > 100 {
		return errors.New("Spotify accepts at most 100 playlist items per request")
	}
	playlistID := spotifyID(args[0], "playlist")
	uris := make([]string, 0, len(args)-1)
	for _, item := range args[1:] {
		uri, err := spotifyURI(item, "track")
		if err != nil {
			return err
		}
		uris = append(uris, uri)
	}

	var body any
	if method == http.MethodDelete {
		items := make([]map[string]string, 0, len(uris))
		for _, uri := range uris {
			items = append(items, map[string]string{"uri": uri})
		}
		body = map[string]any{"items": items}
	} else {
		body = map[string]any{"uris": uris}
	}
	return outputRequest(client, method, "/playlists/"+playlistID+"/items", nil, body)
}

func outputRequest(client *spotifyClient, method, path string, query url.Values, body any) error {
	result, err := client.request(method, path, query, body)
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func spotifyID(value, expectedType string) string {
	uri, err := spotifyURI(value, expectedType)
	if err != nil {
		return value
	}
	parts := strings.Split(uri, ":")
	return parts[len(parts)-1]
}

func spotifyURI(value, defaultType string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("Spotify item cannot be empty")
	}
	if strings.HasPrefix(value, "spotify:") {
		parts := strings.Split(value, ":")
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
			return "", fmt.Errorf("invalid Spotify URI %q", value)
		}
		return value, nil
	}
	if parsed, err := url.Parse(value); err == nil && (parsed.Host == "open.spotify.com" || parsed.Host == "www.open.spotify.com") {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) < 2 {
			return "", fmt.Errorf("invalid Spotify URL %q", value)
		}
		return "spotify:" + parts[0] + ":" + parts[1], nil
	}
	if strings.ContainsAny(value, ":/?#") {
		return "", fmt.Errorf("invalid Spotify item %q", value)
	}
	return "spotify:" + defaultType + ":" + value, nil
}

type optionalBool struct {
	set   bool
	value bool
}

func (value *optionalBool) String() string {
	if !value.set {
		return ""
	}
	return strconv.FormatBool(value.value)
}

func (value *optionalBool) Set(raw string) error {
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return err
	}
	value.set = true
	value.value = parsed
	return nil
}
