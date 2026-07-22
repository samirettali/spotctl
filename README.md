# spotctl

An agent-friendly Spotify CLI with machine-readable JSON output.

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

## Commands

Search Spotify:

```sh
spotctl search "teardrop massive attack"
spotctl search --type album --limit 5 "mezzanine"
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
spotctl playlist get PLAYLIST_ID
spotctl playlist create --name "Late night" --description "Created by my agent"
spotctl playlist update PLAYLIST_ID --name "Later night" --public=true
spotctl playlist add PLAYLIST_ID TRACK_ID spotify:track:TRACK_ID
spotctl playlist remove PLAYLIST_ID TRACK_ID
spotctl playlist delete PLAYLIST_ID
```

Playlist deletion means unfollowing the playlist, matching Spotify's API semantics.

## Queue limitation

Spotify exposes queue inspection and append operations only. It does not provide API operations to remove, reorder, replace, or clear queued items.

## API compatibility

`spotctl` targets Spotify's post-February-2026 playlist endpoints, including `/me/playlists` and `/playlists/{id}/items`.
