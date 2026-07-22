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
