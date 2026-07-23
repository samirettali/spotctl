package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

func runSearch(args []string) error {
	flags := flag.NewFlagSet("search", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	itemType := flags.String("type", "track", "track, album, artist, or playlist")
	limit := flags.Int("limit", 10, "number of results (1-10)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return errors.New("usage: spotctl search [--type TYPE] [--limit N] QUERY")
	}
	validTypes := map[string]bool{"track": true, "album": true, "artist": true, "playlist": true}
	if !validTypes[*itemType] {
		return fmt.Errorf("unsupported search type %q", *itemType)
	}
	if *limit < 1 || *limit > 10 {
		return errors.New("search limit must be between 1 and 10")
	}

	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	result, err := client.request(http.MethodGet, "/search", url.Values{
		"q":     {strings.Join(flags.Args(), " ")},
		"type":  {*itemType},
		"limit": {strconv.Itoa(*limit)},
	}, nil)
	if err != nil {
		return err
	}
	return writeJSON(result)
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
	return outputRequest(client, http.MethodGet, "/me/top/"+itemType, url.Values{
		"time_range": {*timeRange},
		"limit":      {strconv.Itoa(*limit)},
		"offset":     {strconv.Itoa(*offset)},
	}, nil)
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
	return outputRequest(client, http.MethodGet, "/me/player/recently-played", query, nil)
}

func runDevice(args []string) error {
	if len(args) != 1 || args[0] != "list" {
		return errors.New("usage: spotctl device list")
	}
	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	return outputRequest(client, http.MethodGet, "/me/player/devices", nil, nil)
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
		if len(args) != 1 {
			return errors.New("usage: spotctl queue get")
		}
		result, err := client.request(http.MethodGet, "/me/player/queue", nil, nil)
		if err != nil {
			return err
		}
		return writeJSON(result)
	case "add":
		flags := flag.NewFlagSet("queue add", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		device := flags.String("device", "", "Spotify Connect device ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("usage: spotctl queue add [--device ID] ITEM")
		}
		uri, err := spotifyURI(flags.Arg(0), "track")
		if err != nil {
			return err
		}
		query := url.Values{"uri": {uri}}
		if *device != "" {
			query.Set("device_id", *device)
		}
		result, err := client.request(http.MethodPost, "/me/player/queue", query, nil)
		if err != nil {
			return err
		}
		return writeJSON(result)
	default:
		return fmt.Errorf("unknown queue command %q", args[0])
	}
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
	}

	client, err := newSpotifyClient()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		return playlistList(client, args[1:])
	case "get":
		if len(args) != 2 {
			return errors.New("usage: spotctl playlist get PLAYLIST")
		}
		return outputRequest(client, http.MethodGet, "/playlists/"+spotifyID(args[1], "playlist"), nil, nil)
	case "items":
		return playlistGetItems(client, args[1:])
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

func playlistList(client *spotifyClient, args []string) error {
	flags := flag.NewFlagSet("playlist list", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	limit := flags.Int("limit", 50, "number of playlists (1-50)")
	offset := flags.Int("offset", 0, "result offset (0 or greater)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *limit < 1 || *limit > 50 || *offset < 0 {
		return errors.New("usage: spotctl playlist list [--limit N] [--offset N], where N is 1-50 and offset is non-negative")
	}
	return outputRequest(client, http.MethodGet, "/me/playlists", url.Values{
		"limit":  {strconv.Itoa(*limit)},
		"offset": {strconv.Itoa(*offset)},
	}, nil)
}

func playlistGetItems(client *spotifyClient, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: spotctl playlist items PLAYLIST [--limit N] [--offset N]")
	}
	playlistID := spotifyID(args[0], "playlist")
	flags := flag.NewFlagSet("playlist items", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	limit := flags.Int("limit", 100, "number of items (1-100)")
	offset := flags.Int("offset", 0, "result offset (0 or greater)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *limit < 1 || *limit > 100 || *offset < 0 {
		return errors.New("usage: spotctl playlist items PLAYLIST [--limit N] [--offset N], where N is 1-100 and offset is non-negative")
	}
	return outputRequest(client, http.MethodGet, "/playlists/"+playlistID+"/items", url.Values{
		"limit":  {strconv.Itoa(*limit)},
		"offset": {strconv.Itoa(*offset)},
	}, nil)
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
