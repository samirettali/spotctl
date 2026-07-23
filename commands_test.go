package main

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func stubResponse(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}
}

func testClient(transport roundTripFunc) *spotifyClient {
	return &spotifyClient{
		httpClient: &http.Client{Transport: transport},
		creds:      credentials{AccessToken: "token", ExpiresAt: time.Now().Add(time.Hour)},
	}
}

func TestSpotifyURI(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		itemType  string
		expected  string
		shouldErr bool
	}{
		{name: "bare ID", input: "abc123", itemType: "track", expected: "spotify:track:abc123"},
		{name: "URI", input: "spotify:episode:abc123", itemType: "track", expected: "spotify:episode:abc123"},
		{name: "URL", input: "https://open.spotify.com/track/abc123?si=value", itemType: "track", expected: "spotify:track:abc123"},
		{name: "playlist URL", input: "https://open.spotify.com/playlist/xyz789", itemType: "playlist", expected: "spotify:playlist:xyz789"},
		{name: "empty", input: "", itemType: "track", shouldErr: true},
		{name: "malformed", input: "not/a/spotify/id", itemType: "track", shouldErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := spotifyURI(test.input, test.itemType)
			if test.shouldErr {
				if err == nil {
					t.Fatalf("spotifyURI(%q) did not return an error", test.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("spotifyURI(%q): %v", test.input, err)
			}
			if actual != test.expected {
				t.Fatalf("spotifyURI(%q) = %q, want %q", test.input, actual, test.expected)
			}
		})
	}
}

func TestSearchValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing query", args: []string{"--limit", "5"}},
		{name: "invalid type", args: []string{"--type", "episode", "query"}},
		{name: "limit too low", args: []string{"--limit", "0", "query"}},
		{name: "limit too high", args: []string{"--limit", "51", "query"}},
		{name: "negative offset", args: []string{"--offset", "-1", "query"}},
		{name: "offset too high", args: []string{"--offset", "1001", "query"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := runSearch(test.args); err == nil {
				t.Fatalf("runSearch(%q) did not return an error", test.args)
			}
		})
	}
}

func TestTopValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing type", args: nil},
		{name: "invalid type", args: []string{"albums"}},
		{name: "invalid time range", args: []string{"tracks", "--time-range", "weekly"}},
		{name: "limit too low", args: []string{"artists", "--limit", "0"}},
		{name: "limit too high", args: []string{"artists", "--limit", "51"}},
		{name: "negative offset", args: []string{"tracks", "--offset", "-1"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := runTop(test.args); err == nil {
				t.Fatalf("runTop(%q) did not return an error", test.args)
			}
		})
	}
}

func TestRecentHistoryValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing subcommand", args: nil},
		{name: "invalid subcommand", args: []string{"all"}},
		{name: "limit too low", args: []string{"recent", "--limit", "0"}},
		{name: "limit too high", args: []string{"recent", "--limit", "51"}},
		{name: "negative before", args: []string{"recent", "--before", "-1"}},
		{name: "negative after", args: []string{"recent", "--after", "-1"}},
		{name: "both cursors", args: []string{"recent", "--before", "1", "--after", "2"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := runHistory(test.args); err == nil {
				t.Fatalf("runHistory(%q) did not return an error", test.args)
			}
		})
	}
}

func TestPlaylistPaginationValidation(t *testing.T) {
	if err := playlistList(nil, []string{"--offset", "-1"}); err == nil {
		t.Fatal("playlistList accepted a negative offset")
	}

	tests := []struct {
		name string
		args []string
	}{
		{name: "missing playlist", args: nil},
		{name: "limit too low", args: []string{"playlist", "--limit", "0"}},
		{name: "limit too high", args: []string{"playlist", "--limit", "101"}},
		{name: "negative offset", args: []string{"playlist", "--offset", "-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := playlistGetItems(nil, test.args); err == nil {
				t.Fatalf("playlistGetItems(%q) did not return an error", test.args)
			}
		})
	}
}

func testCachedPlaylists() []cachedPlaylist {
	shared := cachedTrack{ID: "track2", Name: "Shared Song", Artists: []cachedArtist{
		{ID: "artist1", Name: "Alpha"},
	}}
	return []cachedPlaylist{
		{ID: "playlist1", Name: "First", SnapshotID: "snapshot1", Tracks: []cachedTrack{
			{ID: "track1", Name: "Opener", Artists: []cachedArtist{
				{ID: "artist1", Name: "Alpha"},
				{ID: "artist2", Name: "Beta"},
			}},
			shared,
			{ID: "track1", Name: "Opener", Artists: []cachedArtist{{ID: "artist1", Name: "Alpha"}}},
		}},
		{ID: "playlist2", Name: "Second", SnapshotID: "snapshot2", Tracks: []cachedTrack{shared}},
	}
}

func TestPlaylistCacheContains(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	cachedAt := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	if err := replacePlaylistCache(path, testCachedPlaylists(), cachedAt); err != nil {
		t.Fatal(err)
	}

	result, err := queryPlaylistContains(path, []string{"track2", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if result.CachedAt != cachedAt.Format(time.RFC3339) || len(result.Results) != 2 {
		t.Fatalf("query result = %+v", result)
	}
	if !result.Results[0].Contains || len(result.Results[0].Playlists) != 2 {
		t.Fatalf("track2 result = %+v", result.Results[0])
	}
	if result.Results[0].Playlists[0].ID != "playlist1" || result.Results[0].Playlists[1].ID != "playlist2" {
		t.Fatalf("track2 playlists = %+v", result.Results[0].Playlists)
	}
	if result.Results[1].Contains || len(result.Results[1].Playlists) != 0 {
		t.Fatalf("missing track result = %+v", result.Results[1])
	}
}

func TestPlaylistCacheContainsRequiresRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	if _, err := queryPlaylistContains(path, []string{"track"}); err == nil {
		t.Fatal("uninitialized cache did not return an error")
	}
}

func TestPlaylistCacheReplacementRemovesOldData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	old := []cachedPlaylist{{ID: "old", Name: "Old", Tracks: []cachedTrack{
		{ID: "track", Name: "Gone", Artists: []cachedArtist{{ID: "artist", Name: "Ghost"}}},
	}}}
	if err := replacePlaylistCache(path, old, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := replacePlaylistCache(path, []cachedPlaylist{{ID: "new", Name: "New"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err := queryPlaylistContains(path, []string{"track"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Results[0].Contains {
		t.Fatalf("replaced playlist remained in cache: %+v", result.Results[0])
	}
	artists, err := queryPlaylistArtists(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(artists.Artists) != 0 {
		t.Fatalf("replaced artists remained in cache: %+v", artists.Artists)
	}
}

func TestPlaylistCacheArtists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	if err := replacePlaylistCache(path, testCachedPlaylists(), time.Now()); err != nil {
		t.Fatal(err)
	}

	all, err := queryPlaylistArtists(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Artists) != 2 {
		t.Fatalf("artists = %+v", all.Artists)
	}
	alpha := all.Artists[0]
	if alpha.Name != "Alpha" || alpha.Tracks != 2 || alpha.Playlists != 2 {
		t.Fatalf("alpha stats = %+v", alpha)
	}
	if all.Artists[1].Name != "Beta" || all.Artists[1].Tracks != 1 || all.Artists[1].Playlists != 1 {
		t.Fatalf("beta stats = %+v", all.Artists[1])
	}

	filtered, err := queryPlaylistArtists(path, []string{"alp", "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Results) != 2 {
		t.Fatalf("filtered results = %+v", filtered.Results)
	}
	if len(filtered.Results[0].Artists) != 1 || filtered.Results[0].Artists[0].Name != "Alpha" {
		t.Fatalf("alpha query = %+v", filtered.Results[0])
	}
	if len(filtered.Results[1].Artists) != 0 {
		t.Fatalf("unknown query matched = %+v", filtered.Results[1])
	}
}

func TestPlaylistCacheStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	if err := replacePlaylistCache(path, testCachedPlaylists(), time.Now()); err != nil {
		t.Fatal(err)
	}
	stats, err := queryPlaylistStats(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Playlists) != 2 || stats.Playlists[0].Name != "First" || stats.Playlists[0].Tracks != 3 {
		t.Fatalf("playlist stats = %+v", stats.Playlists)
	}
	if stats.TotalTracks != 4 || stats.DistinctTracks != 2 {
		t.Fatalf("totals = %+v", stats)
	}
}

func TestPlaylistCacheSearch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	if err := replacePlaylistCache(path, testCachedPlaylists(), time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err := queryPlaylistSearch(path, []string{"beta", "missing"}, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("search results = %+v", result.Results)
	}
	byArtist := result.Results[0].Tracks
	if len(byArtist) != 1 || byArtist[0].Name != "Opener" || len(byArtist[0].Playlists) != 1 {
		t.Fatalf("beta search = %+v", byArtist)
	}
	if len(result.Results[1].Tracks) != 0 {
		t.Fatalf("missing search matched = %+v", result.Results[1])
	}
}

func TestPlaylistCacheSample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	if err := replacePlaylistCache(path, testCachedPlaylists(), time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err := queryPlaylistSample(path, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tracks) != 2 {
		t.Fatalf("sample should exhaust the two distinct tracks, got %+v", result.Tracks)
	}
	seen := map[string]bool{}
	for _, track := range result.Tracks {
		if seen[track.ID] {
			t.Fatalf("sample repeated track %q", track.ID)
		}
		seen[track.ID] = true
	}

	filtered, err := queryPlaylistSample(path, 5, "second")
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Tracks) != 1 || filtered.Tracks[0].ID != "track2" || filtered.Tracks[0].Playlist.Name != "Second" {
		t.Fatalf("filtered sample = %+v", filtered.Tracks)
	}
	if _, err := queryPlaylistSample(path, 5, "nope"); err == nil {
		t.Fatal("sample with unmatched filter did not return an error")
	}
}

func TestPlaylistCachePredatesTrackMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	if err := replacePlaylistCache(path, testCachedPlaylists(), time.Now()); err != nil {
		t.Fatal(err)
	}
	database, err := openPlaylistCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("DELETE FROM tracks"); err != nil {
		t.Fatal(err)
	}
	database.Close()
	if _, err := queryPlaylistArtists(path, nil); err == nil {
		t.Fatal("stale cache did not return an error")
	}
	if _, err := queryPlaylistContains(path, []string{"track2"}); err != nil {
		t.Fatalf("contains should still work on a stale cache: %v", err)
	}
}

func TestPlaylistCacheStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.db")
	status, err := readPlaylistCacheStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !status.cachedAt.IsZero() {
		t.Fatalf("uninitialized cache reported a timestamp: %+v", status)
	}

	cachedAt := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	if err := replacePlaylistCache(path, testCachedPlaylists(), cachedAt); err != nil {
		t.Fatal(err)
	}
	status, err = readPlaylistCacheStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !status.cachedAt.Equal(cachedAt) || status.playlists != 2 || status.tracks != 4 {
		t.Fatalf("cache status = %+v", status)
	}
	if status.upToDate(time.Nanosecond) {
		t.Fatal("expired cache reported up to date")
	}

	if err := replacePlaylistCache(path, testCachedPlaylists(), time.Now()); err != nil {
		t.Fatal(err)
	}
	status, err = readPlaylistCacheStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !status.upToDate(24 * time.Hour) {
		t.Fatalf("fresh cache reported stale: %+v", status)
	}

	database, err := openPlaylistCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("DELETE FROM tracks"); err != nil {
		t.Fatal(err)
	}
	database.Close()
	status, err = readPlaylistCacheStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if status.upToDate(24 * time.Hour) {
		t.Fatal("cache without track metadata reported up to date")
	}
}

func TestQueueAddItems(t *testing.T) {
	original := retrySleep
	var slept []time.Duration
	retrySleep = func(wait time.Duration) { slept = append(slept, wait) }
	t.Cleanup(func() { retrySleep = original })

	rateLimited := `{"error":{"status":429,"message":"rate limited"}}`
	calls := map[string]int{}
	client := testClient(func(request *http.Request) (*http.Response, error) {
		uri := request.URL.Query().Get("uri")
		calls[uri]++
		switch uri {
		case "spotify:track:ok":
			return stubResponse(http.StatusNoContent, "", nil), nil
		case "spotify:track:slow": // 429 twice, then succeeds
			if calls[uri] <= 2 {
				return stubResponse(http.StatusTooManyRequests, rateLimited, http.Header{"Retry-After": {"0"}}), nil
			}
			return stubResponse(http.StatusNoContent, "", nil), nil
		default: // always rate limited
			return stubResponse(http.StatusTooManyRequests, rateLimited, http.Header{"Retry-After": {"0"}}), nil
		}
	})

	result := queueAddItems(client, []string{"spotify:track:ok", "spotify:track:slow", "spotify:track:nope"}, "device1")
	if result.Queued != 2 {
		t.Fatalf("queued = %d, want 2", result.Queued)
	}
	if len(result.Failed) != 1 || result.Failed[0].URI != "spotify:track:nope" {
		t.Fatalf("failed = %+v", result.Failed)
	}
	if calls["spotify:track:slow"] != 3 {
		t.Fatalf("slow track attempts = %d, want 3", calls["spotify:track:slow"])
	}
	if calls["spotify:track:nope"] != maxRateLimitRetries+1 {
		t.Fatalf("nope track attempts = %d, want %d", calls["spotify:track:nope"], maxRateLimitRetries+1)
	}
	// two retries for slow, five for nope
	if len(slept) != 2+maxRateLimitRetries {
		t.Fatalf("sleeps = %d, want %d", len(slept), 2+maxRateLimitRetries)
	}
}

func TestRequestWithRetryStopsOnNon429(t *testing.T) {
	original := retrySleep
	retrySleep = func(time.Duration) {}
	t.Cleanup(func() { retrySleep = original })

	calls := 0
	client := testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return stubResponse(http.StatusNotFound, `{"error":{"status":404,"message":"no device"}}`, nil), nil
	})
	if _, err := requestWithRetry(client, http.MethodPost, "/me/player/queue", nil, nil); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry on 404)", calls)
	}
}

func TestExactSpotifyID(t *testing.T) {
	id, err := exactSpotifyID("https://open.spotify.com/track/abc123?si=value", "track")
	if err != nil || id != "abc123" {
		t.Fatalf("exactSpotifyID() = %q, %v", id, err)
	}
	if _, err := exactSpotifyID("spotify:album:abc123", "track"); err == nil {
		t.Fatal("exactSpotifyID accepted the wrong item type")
	}
}

func TestOptionalBool(t *testing.T) {
	var value optionalBool
	if value.set {
		t.Fatal("optional bool starts set")
	}
	if err := value.Set("true"); err != nil {
		t.Fatal(err)
	}
	if !value.set || !value.value {
		t.Fatalf("optional bool = %+v, want set true", value)
	}
	if err := value.Set("invalid"); err == nil {
		t.Fatal("invalid boolean did not return an error")
	}
}
