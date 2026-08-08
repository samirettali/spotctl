package main

import "encoding/json"

// Every read command answers with a trimmed shape by default and the untouched
// Spotify payload under --full. The trimmed shapes carry what an agent needs to
// pick an item and act on it — an id, a name, and whatever disambiguates two
// entries with the same name — and nothing else, because the full payloads cost
// roughly ten times the tokens.

type spotifyArtistRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type spotifyTrackObject struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Type    string             `json:"type"`
	Artists []spotifyArtistRef `json:"artists"`
	Album   struct {
		Name string `json:"name"`
	} `json:"album"`
}

type spotifyArtistObject struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Genres []string `json:"genres"`
}

type spotifyAlbumObject struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Artists []spotifyArtistRef `json:"artists"`
}

type spotifyPlaylistObject struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SnapshotID  string `json:"snapshot_id"`
	Public      *bool  `json:"public"`
	Owner       struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"owner"`
	Tracks struct {
		Total int `json:"total"`
	} `json:"tracks"`
}

type spotifyDeviceObject struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	IsActive      bool   `json:"is_active"`
	VolumePercent *int   `json:"volume_percent"`
}

type minimalTrack struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Artists []string `json:"artists"`
	Album   string   `json:"album,omitempty"`
}

type minimalArtist struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Genres []string `json:"genres"`
}

type minimalAlbum struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Artists []string `json:"artists"`
}

type minimalPlaylist struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Tracks      int    `json:"tracks"`
	Owner       string `json:"owner,omitempty"`
	Public      *bool  `json:"public"`
	Description string `json:"description,omitempty"`
}

type minimalPlayed struct {
	PlayedAt string       `json:"played_at"`
	Track    minimalTrack `json:"track"`
}

type minimalDevice struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	IsActive      bool   `json:"is_active"`
	VolumePercent *int   `json:"volume_percent"`
}

// artistNames keeps the trimmed shapes to plain names, matching what the cache
// query commands already emit.
func artistNames(artists []spotifyArtistRef) []string {
	names := make([]string, 0, len(artists))
	for _, artist := range artists {
		names = append(names, artist.Name)
	}
	return names
}

func trimTrack(track spotifyTrackObject) minimalTrack {
	return minimalTrack{
		ID:      track.ID,
		Name:    track.Name,
		Artists: artistNames(track.Artists),
		Album:   track.Album.Name,
	}
}

func trimArtist(artist spotifyArtistObject) minimalArtist {
	if artist.Genres == nil {
		artist.Genres = []string{}
	}
	return minimalArtist{ID: artist.ID, Name: artist.Name, Genres: artist.Genres}
}

func trimAlbum(album spotifyAlbumObject) minimalAlbum {
	return minimalAlbum{ID: album.ID, Name: album.Name, Artists: artistNames(album.Artists)}
}

func trimPlaylist(playlist spotifyPlaylistObject, withDescription bool) minimalPlaylist {
	trimmed := minimalPlaylist{
		ID:     playlist.ID,
		Name:   playlist.Name,
		Tracks: playlist.Tracks.Total,
		Owner:  playlist.Owner.DisplayName,
		Public: playlist.Public,
	}
	if withDescription {
		trimmed.Description = playlist.Description
	}
	return trimmed
}

func trimDevice(device spotifyDeviceObject) minimalDevice {
	return minimalDevice{
		ID:            device.ID,
		Name:          device.Name,
		Type:          device.Type,
		IsActive:      device.IsActive,
		VolumePercent: device.VolumePercent,
	}
}

// decodeInto unmarshals a payload that has already been produced by Spotify, so
// a failure means the shape changed rather than that the input is untrusted.
func decodeInto[T any](data []byte, target *T) error {
	return json.Unmarshal(data, target)
}
