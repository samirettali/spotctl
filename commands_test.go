package main

import "testing"

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
