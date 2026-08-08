package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

// The envelope is always Spotify's. Only the objects inside `items` change:
// trimmed by default, verbatim under --full. That way a caller that already
// knows Spotify's API keeps working, and the token cost of the full payloads is
// opt-in rather than mandatory.
//
// For network reads the paging fields are copied straight off Spotify's own
// response, so href/next/previous are not reconstructed and cannot drift. Cache
// reads rebuild them from the stored collection URL, which is what Spotify does
// too: they are a function of (endpoint, limit, offset, total).

type rawPaging struct {
	Href     string            `json:"href"`
	Items    []json.RawMessage `json:"items"`
	Limit    int               `json:"limit"`
	Next     *string           `json:"next"`
	Offset   int               `json:"offset"`
	Previous *string           `json:"previous"`
	Total    int               `json:"total"`
}

type pagingEnvelope struct {
	Href     string  `json:"href,omitempty"`
	Items    any     `json:"items"`
	Limit    int     `json:"limit"`
	Next     *string `json:"next"`
	Offset   int     `json:"offset"`
	Previous *string `json:"previous"`
	Total    int     `json:"total"`
	Source   string  `json:"source,omitempty"`
	CachedAt string  `json:"cached_at,omitempty"`
}

func (page rawPaging) envelope(items any) pagingEnvelope {
	return pagingEnvelope{
		Href:     page.Href,
		Items:    items,
		Limit:    page.Limit,
		Next:     page.Next,
		Offset:   page.Offset,
		Previous: page.Previous,
		Total:    page.Total,
	}
}

// pageLinks rebuilds the paging URLs for a cache read. collection is the
// endpoint Spotify itself reported for this list, so the links point where the
// live data is; cached_at says how old the answer is.
func pageLinks(collection string, limit, offset, total int) (string, *string, *string) {
	if collection == "" {
		return "", nil, nil
	}
	at := func(offset, limit int) string {
		return collection + "?" + url.Values{
			"offset": {strconv.Itoa(offset)},
			"limit":  {strconv.Itoa(limit)},
		}.Encode()
	}
	var next, previous *string
	if offset+limit < total {
		link := at(offset+limit, limit)
		next = &link
	}
	if offset > 0 {
		start := offset - limit
		if start < 0 {
			start = 0
		}
		link := at(start, limit)
		previous = &link
	}
	return at(offset, limit), next, previous
}

// trimItems decodes each item and projects it, keeping Spotify's envelope.
func trimItems[T any, R any](page rawPaging, project func(T) R) (pagingEnvelope, error) {
	items := make([]R, 0, len(page.Items))
	for _, raw := range page.Items {
		var decoded T
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return pagingEnvelope{}, fmt.Errorf("decode item: %w", err)
		}
		items = append(items, project(decoded))
	}
	return page.envelope(items), nil
}

func trimSearch(data []byte, itemType string) (any, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("decode search results: %w", err)
	}
	// Spotify nests one paging object per requested type
	key := itemType + "s"
	raw, ok := body[key]
	if !ok {
		return nil, fmt.Errorf("search response has no %q results", key)
	}
	var page rawPaging
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, fmt.Errorf("decode search results: %w", err)
	}

	var envelope pagingEnvelope
	var err error
	switch itemType {
	case "track":
		envelope, err = trimItems(page, trimTrack)
	case "artist":
		envelope, err = trimItems(page, trimArtist)
	case "album":
		envelope, err = trimItems(page, trimAlbum)
	case "playlist":
		// Spotify pads playlist search pages with nulls
		items := make([]minimalPlaylist, 0, len(page.Items))
		for _, item := range page.Items {
			var playlist *spotifyPlaylistObject
			if err := json.Unmarshal(item, &playlist); err != nil {
				return nil, fmt.Errorf("decode playlist: %w", err)
			}
			if playlist == nil {
				continue
			}
			items = append(items, trimPlaylist(*playlist, false))
		}
		envelope = page.envelope(items)
	default:
		return nil, fmt.Errorf("unsupported search type %q", itemType)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{key: envelope}, nil
}

func trimTop(data []byte, itemType string) (any, error) {
	var page rawPaging
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, fmt.Errorf("decode top items: %w", err)
	}
	if itemType == "artists" {
		return trimItems(page, trimArtist)
	}
	return trimItems(page, trimTrack)
}

type playedItem struct {
	PlayedAt string             `json:"played_at"`
	Track    spotifyTrackObject `json:"track"`
}

type minimalPlayedItem struct {
	PlayedAt string       `json:"played_at"`
	Track    minimalTrack `json:"track"`
}

// recently-played is a cursor-paged object: it has cursors instead of offset,
// so its envelope is passed through rather than reshaped.
type cursorEnvelope struct {
	Href    string          `json:"href,omitempty"`
	Items   any             `json:"items"`
	Limit   int             `json:"limit"`
	Next    *string         `json:"next"`
	Cursors json.RawMessage `json:"cursors,omitempty"`
	Total   int             `json:"total,omitempty"`
}

func trimHistoryResponse(data []byte) (any, error) {
	var body struct {
		Href    string            `json:"href"`
		Items   []json.RawMessage `json:"items"`
		Limit   int               `json:"limit"`
		Next    *string           `json:"next"`
		Cursors json.RawMessage   `json:"cursors"`
		Total   int               `json:"total"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("decode recent history: %w", err)
	}
	items := make([]minimalPlayedItem, 0, len(body.Items))
	for _, raw := range body.Items {
		var played playedItem
		if err := json.Unmarshal(raw, &played); err != nil {
			return nil, fmt.Errorf("decode played item: %w", err)
		}
		items = append(items, minimalPlayedItem{PlayedAt: played.PlayedAt, Track: trimTrack(played.Track)})
	}
	return cursorEnvelope{
		Href:    body.Href,
		Items:   items,
		Limit:   body.Limit,
		Next:    body.Next,
		Cursors: body.Cursors,
		Total:   body.Total,
	}, nil
}

func trimDevicesResponse(data []byte) (any, error) {
	var body struct {
		Devices []spotifyDeviceObject `json:"devices"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("decode devices: %w", err)
	}
	devices := make([]minimalDevice, 0, len(body.Devices))
	for _, device := range body.Devices {
		devices = append(devices, trimDevice(device))
	}
	return map[string]any{"devices": devices}, nil
}

func trimQueueResponse(data []byte) (any, error) {
	var body struct {
		CurrentlyPlaying *spotifyTrackObject  `json:"currently_playing"`
		Queue            []spotifyTrackObject `json:"queue"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("decode queue: %w", err)
	}
	queue := make([]minimalTrack, 0, len(body.Queue))
	for _, item := range body.Queue {
		queue = append(queue, trimTrack(item))
	}
	result := map[string]any{"currently_playing": nil, "queue": queue}
	if body.CurrentlyPlaying != nil {
		result["currently_playing"] = trimTrack(*body.CurrentlyPlaying)
	}
	return result, nil
}

func outputTrimmed(
	client *spotifyClient,
	method, path string,
	query url.Values,
	body any,
	full bool,
	trim func([]byte) (any, error),
) error {
	data, err := client.request(method, path, query, body)
	if err != nil {
		return err
	}
	if full {
		return writeJSON(data)
	}
	trimmed, err := trim(data)
	if err != nil {
		return err
	}
	return writeJSON(trimmed)
}
