# spotctl

An agent-friendly Spotify CLI with machine-readable JSON output.

Responses keep Spotify's own envelope — `items`, `limit`, `offset`, `total`, `next`,
`previous`, `href` — so anything written against the Web API keeps working. Only the objects
inside change: trimmed to what an agent needs by default, and exactly as Spotify sent them
with `--full`. The trimmed form is roughly a tenth of the tokens.

`playlist list`, `playlist get` and `playlist items` answer from a local SQLite cache without
contacting Spotify, so they are instant; `--refresh` fetches and updates it.

## Requirements

- Go 1.24+
- Spotify Premium for playback and queue operations
- A Spotify developer application using Authorization Code with PKCE

Register `http://127.0.0.1:8989/callback` as a redirect URI in the Spotify developer dashboard. New Spotify applications run in restricted Development Mode; the account authorizing `spotctl` must be allowed to use the application.

## Build

```sh
go build ./...
```

## Authentication

No client secret is needed.

```sh
spotctl auth login --client-id YOUR_CLIENT_ID
spotctl auth status
spotctl auth logout
```

`SPOTIFY_CLIENT_ID` can be used instead of `--client-id`. Credentials are stored with user-only permissions under the operating system's user config directory.

There is no need to check `auth status` before running anything. A command that needs authentication and does not have it — never logged in, grant revoked, token rejected — fails with the command that fixes it:

```json
{"error": "not authenticated", "fix": "spotctl auth login --client-id YOUR_SPOTIFY_CLIENT_ID", "details": "..."}
```

## Commands

Search Spotify:

```sh
spotctl search "teardrop massive attack"
spotctl search --type album --limit 5 "mezzanine"
```

Resolve several track names to IDs in one command, for instance to turn a list of recommendations into something queueable:

```sh
spotctl resolve "funk tribu phonky tribu" "the blaze territory"
spotctl resolve --limit 3 "teardrop massive attack"
```

Each query is searched concurrently and the results keep the order of the arguments, so they can be zipped back against the original list. A query that fails carries its `error` instead of aborting the batch, and one that matches nothing carries an empty `tracks` array.

Spotify's search is fuzzy and almost always returns something, so a match is not a confirmation: check that the artist and title are the ones you asked for before acting on the ID. Use `--limit` when you want to choose among candidates rather than take the top hit.

Inspect your top tracks or artists over Spotify's short-term (approximately 4 weeks), medium-term (approximately 6 months), or long-term (several years) windows:

```sh
spotctl top tracks --time-range short_term --limit 50
spotctl top artists --time-range long_term
```

Inspect recently played tracks and paginate with the millisecond timestamps returned under `cursors`:

```sh
spotctl history recent --limit 50
spotctl history recent --before 1735689600000
```

These commands require `user-top-read` and `user-read-recently-played`. Existing users must run `spotctl auth login` again to grant the new scopes.

Start playback on the active device or a specific Spotify Connect device:

```sh
spotctl device list
spotctl play album spotify:album:7kr9rQrjG28viFlKwH2QGq
spotctl play track --device DEVICE_ID spotify:track:0F7FA14euOIX8KcbEturGH
spotctl play playlist PLAYLIST_ID
```

Inspect or append to the playback queue:

```sh
spotctl queue get
spotctl queue add spotify:track:0F7FA14euOIX8KcbEturGH
spotctl queue add --device DEVICE_ID https://open.spotify.com/track/0F7FA14euOIX8KcbEturGH
```

Manage playlists:

```sh
spotctl playlist list
spotctl playlist list --full
spotctl playlist list --refresh
spotctl playlist list --limit 50 --offset 50
spotctl playlist get PLAYLIST_ID
spotctl playlist items PLAYLIST_ID
spotctl playlist create --name "Late night" --description "Created by my agent"
spotctl playlist update PLAYLIST_ID --name "Later night" --public=true
spotctl playlist add PLAYLIST_ID TRACK_ID spotify:track:TRACK_ID
spotctl playlist remove PLAYLIST_ID TRACK_ID
spotctl playlist delete PLAYLIST_ID
```

Cache every owned or followed playlist and all of its tracks in SQLite, then check one or more tracks against the entire cache using bare IDs, Spotify URIs, or Spotify URLs:

```sh
spotctl playlist cache
spotctl playlist contains TRACK_ID
spotctl playlist contains TRACK_ID spotify:track:OTHER_TRACK_ID
```

The result reports whether each track occurs in any cached playlist and lists every matching playlist. Input order is preserved, making bulk checks suitable for filtering recommendation candidates before queueing them.

The default database is `$XDG_CACHE_HOME/spotctl/playlists.db` (or the platform user cache directory). Pass `--db PATH` to either command to use another database. Refreshes replace the cache atomically, and `playlist contains` does not require authentication or network access.

Playlist deletion means unfollowing the playlist, matching Spotify's API semantics.

## Extended streaming history

Spotify does not expose extended or lifetime streaming history through its Web API. Request the Extended Streaming History archive manually from Spotify's account privacy page, then download it using the link Spotify sends by email.

## Queue limitation

Spotify exposes queue inspection and append operations only. It does not provide API operations to remove, reorder, replace, or clear queued items.

## API compatibility

`spotctl` targets Spotify's post-February-2026 playlist endpoints, including `/me/playlists` and `/playlists/{id}/items`.
