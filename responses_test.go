package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTrimSearchTracks(t *testing.T) {
	payload := []byte(`{"tracks":{"href":"https://api.spotify.com/v1/search?q=x","next":"https://api.spotify.com/v1/search?offset=20","limit":10,"total":2,"offset":10,"items":[
		{"id":"t1","name":"One","album":{"name":"Album"},"artists":[{"id":"a1","name":"First"},{"id":"a2","name":"Second"}]},
		{"id":"t2","name":"Two","album":{"name":"Other"},"artists":[]}
	]}}`)

	result, err := trimSearch(payload, "track")
	if err != nil {
		t.Fatalf("trimSearch: %v", err)
	}
	// Spotify nests one paging object per type, and so do we
	nested := result.(map[string]any)["tracks"].(pagingEnvelope)
	if nested.Total != 2 || nested.Offset != 10 || nested.Limit != 10 {
		t.Fatalf("unexpected paging: %+v", nested)
	}
	// the paging links are Spotify's own, copied rather than rebuilt
	if nested.Href != "https://api.spotify.com/v1/search?q=x" || nested.Next == nil {
		t.Fatalf("paging links were not passed through: %+v", nested)
	}
	tracks := nested.Items.([]minimalTrack)
	if len(tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(tracks))
	}
	first := tracks[0]
	if first.ID != "t1" || first.Name != "One" || first.Album != "Album" {
		t.Fatalf("unexpected first track: %+v", first)
	}
	if len(first.Artists) != 2 || first.Artists[0] != "First" || first.Artists[1] != "Second" {
		t.Fatalf("unexpected artists: %+v", first.Artists)
	}
}

// Spotify pads playlist search pages with nulls, which decodes to a nil element
// and would panic anything that dereferences it blindly.
func TestTrimSearchPlaylistsSkipsNulls(t *testing.T) {
	payload := []byte(`{"playlists":{"total":2,"offset":0,"items":[
		null,
		{"id":"p1","name":"Mine","public":true,"owner":{"display_name":"samir"},"tracks":{"total":7}}
	]}}`)

	result, err := trimSearch(payload, "playlist")
	if err != nil {
		t.Fatalf("trimSearch: %v", err)
	}
	nested := result.(map[string]any)["playlists"].(pagingEnvelope)
	playlists := nested.Items.([]minimalPlaylist)
	if len(playlists) != 1 {
		t.Fatalf("got %d playlists, want 1", len(playlists))
	}
	only := playlists[0]
	if only.ID != "p1" || only.Tracks != 7 || only.Owner != "samir" {
		t.Fatalf("unexpected playlist: %+v", only)
	}
	if only.Description != "" {
		t.Fatal("search results should not carry a description")
	}
}

func TestTrimTopArtists(t *testing.T) {
	payload := []byte(`{"total":1,"offset":0,"items":[{"id":"a1","name":"Caparezza","genres":["italian hip hop"]}]}`)
	result, err := trimTop(payload, "artists")
	if err != nil {
		t.Fatalf("trimTop: %v", err)
	}
	artists := result.(pagingEnvelope).Items.([]minimalArtist)
	if len(artists) != 1 || artists[0].Name != "Caparezza" {
		t.Fatalf("unexpected artists: %+v", artists)
	}
	if len(artists[0].Genres) != 1 {
		t.Fatalf("unexpected genres: %+v", artists[0].Genres)
	}
}

func TestTrimQueueWithoutCurrentTrack(t *testing.T) {
	payload := []byte(`{"currently_playing":null,"queue":[{"id":"t1","name":"One","artists":[{"name":"First"}]}]}`)
	result, err := trimQueueResponse(payload)
	if err != nil {
		t.Fatalf("trimQueueResponse: %v", err)
	}
	queue := result.(map[string]any)
	if queue["currently_playing"] != nil {
		t.Fatal("currently_playing should stay nil")
	}
	tracks := queue["queue"].([]minimalTrack)
	if len(tracks) != 1 || tracks[0].Artists[0] != "First" {
		t.Fatalf("unexpected queue: %+v", tracks)
	}
}

// Empty results must encode as [] rather than null: an agent that indexes into
// the array should not have to special-case a missing one.
func TestTrimmedEmptyCollectionsEncodeAsArrays(t *testing.T) {
	result, err := trimHistoryResponse([]byte(`{"items":[]}`))
	if err != nil {
		t.Fatalf("trimHistoryResponse: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"items":[]`) {
		t.Fatalf("got %s, want an empty items array", encoded)
	}

	devices, err := trimDevicesResponse([]byte(`{"devices":[]}`))
	if err != nil {
		t.Fatalf("trimDevicesResponse: %v", err)
	}
	encoded, err = json.Marshal(devices)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != `{"devices":[]}` {
		t.Fatalf("got %s, want an empty array", encoded)
	}
}

func TestTrimDeviceKeepsVolume(t *testing.T) {
	result, err := trimDevicesResponse([]byte(
		`{"devices":[{"id":"d1","name":"Mac","type":"Computer","is_active":true,"volume_percent":40}]}`,
	))
	if err != nil {
		t.Fatalf("trimDevicesResponse: %v", err)
	}
	devices := result.(map[string]any)["devices"].([]minimalDevice)
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}
	device := devices[0]
	if device.VolumePercent == nil || *device.VolumePercent != 40 {
		t.Fatalf("unexpected volume: %+v", device.VolumePercent)
	}
	if !device.IsActive || device.Type != "Computer" {
		t.Fatalf("unexpected device: %+v", device)
	}
}
