---
name: spotify
description: Search and play Spotify music, resolve track names to IDs in bulk, inspect listening history and top items, manage the playback queue, and create, edit, or query playlists with spotctl. Use when the user asks to find or play music; inspect their top tracks, top artists, or recent listening history; manage a playlist; check whether a playlist contains a track; add music to the queue; or inspect what is queued.
compatibility: Requires spotctl. Spotify API operations require OAuth authentication; queue operations also require Spotify Premium. Cached playlist membership checks work offline.
---

# Spotify

`spotctl` handles Spotify search, listening statistics, queue and playlists.
stdout is JSON; errors are JSON on stderr.

## Output shapes

**The envelope is Spotify's own** — `items`, `limit`, `offset`, `total`, `next`, `previous`, `href`, search nested per type, `playlist get` returning the playlist object itself. Only the objects inside change:

- **default** — trimmed: an id, a name, and whatever disambiguates two entries with the same name.
- **`--full`** — the objects exactly as Spotify sends them.

Prefer the default. Full payloads cost roughly ten times the tokens and the trimmed objects already carry what is needed to act. Reach for `--full` only for something the trimmed object omits: album art, follower counts, ISRCs, `added_by`.

Trimmed objects:

```
track     {id, name, artists[], album}
artist    {id, name, genres[]}
album     {id, name, artists[]}
playlist  {id, name, tracks, owner, public}   (+ description on playlist get)
device    {id, name, type, is_active, volume_percent}
```

Cache reads add `source` ("cache" or "api") and `cached_at`. Empty collections are `[]`, never `null`.

## Reading playlists

`playlist list`, `playlist get` and `playlist items` answer from the local SQLite cache without contacting Spotify, so they are instant. They never check staleness: a freshness check over the network would defeat the point.

```sh
spotctl playlist list                      # from cache, every playlist in one page
spotctl playlist list --refresh            # fetch, update the cache, then answer
spotctl playlist list --limit 50 --offset 50
spotctl playlist items PLAYLIST_ID
spotctl playlist items PLAYLIST_ID --refresh
```

Cache reads return everything in one page, so `next` is `null`; `--limit` pages, and then `next`/`previous` are real URLs to follow.

`source` says whether the answer came from `cache` or the `api`, `cached_at` when it was written. If the user just changed a playlist elsewhere, or the response contradicts what they describe, re-run with `--refresh` instead of telling them the data is stale. An empty cache falls back to the API on its own, so these always work.

`--refresh` on `playlist list` re-reads playlist names only, not their tracks. Use `spotctl playlist cache` for that.

## Authentication

**Never check authentication before running a command.** Anything that needs it fails with the fix already in the error, so the check only ever costs a round trip:

```json
{"error": "not authenticated", "fix": "spotctl auth login --client-id YOUR_SPOTIFY_CLIENT_ID", "details": "..."}
```

On a `fix`, relay that command and ask the user to run it — it completes an OAuth flow in the browser and cannot be run for them. Same shape for a revoked or expired grant.

`spotctl auth status` is for when the user asks about their authentication state, not a precondition. Its `scopes` array is worth reading when a `403` looks like a missing permission: top items need `user-top-read`, recent history needs `user-read-recently-played`, both granted by the same `auth login`.

Never request a client secret. `spotctl` uses OAuth Authorization Code with PKCE.

## Resolve items safely

Search before mutating whenever the user gave a title rather than an exact Spotify URI, URL or ID:

```sh
spotctl search --type track "track and artist"
spotctl search --type album --limit 10 "album and artist"
spotctl search --type artist --limit 5 "artist"
spotctl search --type playlist --limit 50 --offset 50 "playlist"
```

Search always contacts Spotify; `--refresh` there is rejected, not ignored. Limit 1-50 (default 20), offset 0-1000 — Spotify's caps, enforced locally before any request.

Use IDs or URIs from the JSON response. Match both title and artist; never take the first result blindly when several plausible matches exist. Ask the user to choose if intent stays ambiguous.

Items may be a Spotify URI, an `open.spotify.com` URL, or a bare track ID.

## Resolve many names at once

Turning several titles into IDs — a list of recommendations, an album's worth of tracks — is `resolve`, not one `search` per title. It searches every query concurrently in one command:

```sh
spotctl resolve "funk tribu phonky tribu" "the blaze territory" "montee ascension"
spotctl resolve --limit 3 "teardrop massive attack"
```

```json
{"results": [{"query": "...", "tracks": [{"id": "...", "name": "...", "artists": [], "album": "..."}]}]}
```

Results keep argument order. A query that matched nothing has an empty `tracks` array; one whose request failed carries an `error` and does not abort the batch. `--limit` is per query (1-50, default 1), `--full` returns Spotify's own track objects.

**A match is not a confirmation.** Spotify's search is fuzzy and nearly always returns something: a misspelled artist, a track that does not exist, outright nonsense — all come back looking plausible. Check artist and title against what was asked before acting on the ID, and pass `--limit 3` for a likely ambiguous query so the right candidate can be picked in the same call. Use `search` when the user wants to browse rather than resolve known titles.

## Parameter limits

Defaults in parentheses, hard caps are Spotify's. Out-of-range values fail locally with a descriptive error — use these instead of probing:

| Command | Limits |
|---|---|
| `search` | `--limit` 1-50 (20), `--offset` 0-1000 |
| `resolve` | `--limit` 1-50 (1), per query; queries are unlimited |
| `top tracks\|artists` | `--limit` 1-50 (20), `--offset` >= 0 |
| `history recent` | `--limit` 1-50 (20); Spotify retains only the last ~50 plays |
| `playlist add\|remove` | at most 100 items per request |
| `playlist search` | `--limit` 1-100 (25) |
| `playlist sample` | `--limit` 1-100 (10) |
| `next\|previous` | `--count` >= 1 (1) |

## Listening statistics

```sh
spotctl top tracks --time-range short_term --limit 50
spotctl top artists --time-range medium_term --limit 50
spotctl top tracks --time-range long_term --limit 50 --offset 50
```

Time ranges: `short_term` (~4 weeks), `medium_term` (~6 months), `long_term` (several years). Limit 1-50, `--offset` pages.

```sh
spotctl history recent --limit 50
spotctl history recent --before UNIX_TIMESTAMP_MS
spotctl history recent --after UNIX_TIMESTAMP_MS
```

Recent history returns at most 50 tracks per request. Page with the millisecond values in the response's `cursors` object; `--before` and `--after` cannot be combined.

Spotify exposes no extended or lifetime streaming history through the Web API. For complete statistics, direct the user to request Extended Streaming History from Spotify's account privacy page and download the archive from the emailed link.

## Playback

Play a track, episode, album, artist or playlist on the active device:

```sh
spotctl play track spotify:track:TRACK_ID
spotctl play episode spotify:episode:EPISODE_ID
spotctl play album spotify:album:ALBUM_ID
spotctl play artist spotify:artist:ARTIST_ID
spotctl play playlist spotify:playlist:PLAYLIST_ID
```

If Spotify reports no active device, list the devices and retry with the intended ID:

```sh
spotctl device list
spotctl play album --device DEVICE_ID spotify:album:ALBUM_ID
```

With several devices and no evident intent, ask which one. Never pick an integrated or third-party player merely because it comes first.

## Queue

```sh
spotctl queue get
spotctl queue add spotify:track:TRACK_ID
spotctl queue add --device DEVICE_ID spotify:track:ONE spotify:track:TWO spotify:track:THREE
```

Items are appended in the order given. Spotify queues one item per request, so spotctl sends them sequentially: a malformed URI fails the whole command before anything is queued, while a runtime failure on one item does not abort the rest. The response is `{"queued": N, "failed": [...]}`, where `failed` holds only items still failing after automatic retries (each with the item `uri` and the `error`) and is empty on full success. Report failed items whenever the array is non-empty. Rate limits (429) retry with exponential backoff, so no manual pacing is needed.

Adding requires Spotify Premium and an active playback device. Spotify cannot remove, clear, replace or reorder queue entries. State that limitation rather than attempting a workaround, unless the user asks for one.

Moving through the queue is the one exception:

```sh
spotctl next
spotctl next --count 3
spotctl previous
```

Both answer `{"skipped": N, "failed": [...]}` and stop at the first failure, since every later step would move past the wrong track. `next --count N` is the closest thing to dropping the next N queued items — **they are consumed, not removed, and do not come back**, so confirm before using it to clear a queue the user built. `previous` walks the play history rather than the queue in reverse, restoring the context the earlier track came from; it is not an undo for `next`.

## Playlists

List or inspect. Both read the cache; see "Reading playlists" above:

```sh
spotctl playlist list
spotctl playlist get PLAYLIST_ID
```

Create:

```sh
spotctl playlist create --name "NAME" --description "DESCRIPTION"
spotctl playlist create --name "NAME" --public
```

Update metadata. Booleans must be explicit:

```sh
spotctl playlist update PLAYLIST_ID --name "NEW NAME"
spotctl playlist update PLAYLIST_ID --description "NEW DESCRIPTION"
spotctl playlist update PLAYLIST_ID --public=true
spotctl playlist update PLAYLIST_ID --collaborative=false
```

Add or remove up to 100 tracks or episodes per request:

```sh
spotctl playlist add PLAYLIST_ID spotify:track:TRACK_ID spotify:track:OTHER_ID
spotctl playlist remove PLAYLIST_ID spotify:track:TRACK_ID
```

Delete, which Spotify implements as unfollowing:

```sh
spotctl playlist delete PLAYLIST_ID
```

Before deleting a playlist or removing items, confirm the target and summarize the destructive change — unless the user's request already identifies both unambiguously.

## Playlist cache

Cache every owned and followed playlist, including each track with its name and artists:

```sh
spotctl playlist cache --max-age 24h
```

`--max-age` skips the network refresh when the cache is newer than that; the response reports `refreshed` either way. Default to `--max-age 24h`. Force a full refresh (no `--max-age`) only when the user just changed playlists or explicitly wants current contents. A cache written by an older spotctl is refreshed automatically whatever its age.

Offline cache queries — none of these contact Spotify:

```sh
spotctl playlist contains TRACK_ID spotify:track:OTHER_TRACK_ID
spotctl playlist artists                        # every artist, with track and playlist counts
spotctl playlist artists "artist name" other    # bulk case-insensitive substring queries
spotctl playlist stats                          # per-playlist track counts and totals
spotctl playlist search --limit 25 QUERY...     # substring search over track and artist names
spotctl playlist sample --limit 10              # random tracks across playlists
spotctl playlist sample --playlist "hard techno" --limit 5
```

- `contains` takes exact track IDs, URIs or URLs and reports every playlist holding each one, preserving input order. Tracks only, not episodes.
- `artists` gauges how well the user knows an artist: `tracks` is how many of their songs are filed, `playlists` how widely. An empty result means the artist is absent from the library.
- `sample` picks a playlist uniformly at random, then a track inside it, so large playlists do not dominate the draw.
- All cache commands take `--db PATH`; otherwise the platform user cache directory.

## Operating rules

- Run only the mutations asked for. Never add related tracks on your own.
- Preserve the user's order when adding multiple items.
- Report what changed with names and artists, not only opaque IDs.
- Never query the cache's SQLite database directly; use the playlist commands.
- Never pass `--full` by default. It is for when the trimmed shape genuinely lacks a field the user asked for.
- On `403`, explain that Spotify application access, scopes, ownership or Premium requirements may be responsible.
- On `404`, re-check the item type and ID before retrying.
- Never expose access or refresh tokens in responses or command output.
