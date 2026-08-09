# AGENTS.md

`spotctl` is an agent-friendly Spotify search, playback, queue, and playlist CLI written in Go.

## Commands

- `go test ./...` — run tests.
- `go vet ./...` — run static checks.
- `go fmt ./...` — format Go files.

## Releasing

Bump `version` in `main.go` in a `chore: release vX.Y.Z` commit, then push a signed annotated
tag. `.github/workflows/release.yml` does the rest: it publishes the GitHub release and starts
the updater in `samirettali/nur` scoped to this package.

- **The release matters, not the tag.** NUR's `pkgs/spotctl/update.sh` reads `releases/latest`
  from the GitHub API, so a pushed tag with no release is invisible to it and the daily sweep
  keeps reporting the previous version in silence. Publishing the release is the whole point of
  the workflow; the dispatch only makes the bump immediate instead of same-day.
- **A tag whose tree predates the workflow will not run it.** Actions evaluates workflows at the
  ref that triggered them, so tagging an older commit is a silent no-op — no run, no release.
  The tag has to sit on a commit that contains `.github/workflows/release.yml`.
- **The dispatch uses `NUR_DISPATCH_TOKEN`**, a fine-grained PAT with Actions read/write on
  `samirettali/nur` and nothing else, declared in the `infra` repo (`github/secrets.tf`). Actions
  write rather than Contents write on purpose: enough to start a run, not enough to push a
  derivation into the package channel. With the secret missing the step is skipped and the daily
  sweep still picks the release up, so it degrades into a delay rather than a failure.
- **NUR's updater rewrites `version` and `hash` but not `vendorHash`.** Adding a Go dependency
  therefore breaks the automatic bump, which surfaces as an issue opened by NUR's workflow, not
  as a silent wrong package. Keeping to the standard library avoids it.

## Conventions

- Keep stdout machine-readable JSON; diagnostics belong on stderr.
- **The envelope is always Spotify's; only the objects inside `items` change.** Default gives
  trimmed objects (an id, a name, and whatever disambiguates two entries with the same name),
  `--full` gives them verbatim — the raw payloads cost roughly ten times the tokens (50
  playlists are 84 KB raw against 8 KB trimmed, most of it `images` and `owner`). Keeping the
  wrapper identical means anything written against the Spotify API keeps working, so the
  paging keys stay `items`/`limit`/`offset`/`total`/`next`/`previous`/`href`, search stays
  nested per type (`{"tracks": {...}}`), `playlist get` returns the object itself rather than a
  wrapper, and `device list`/`queue get` keep their non-paging shapes. `source` and `cached_at`
  are added on cache reads; a Spotify client ignores keys it does not know.
- **Paging fields on network reads are copied off Spotify's own response, never rebuilt**, so
  they cannot drift. Cache reads rebuild them from the collection URL captured during the last
  refresh — Spotify rewrites `/me/playlists` to `/users/{id}/playlists` in `href`, so it is
  stored rather than guessed. `next`/`previous` are then honest: present only when there really
  is another page. Reading from cache defaults to one page holding everything, since there is
  no API cap to respect on disk and the common caller wants the whole library at once.
- **`playlist list|get|items` answer from the SQLite cache and never check freshness over the
  network**, because they sit behind an interactive picker where a stray HTTP round trip is the
  whole problem. `--refresh` fetches and updates; an uninitialized cache falls back to the API
  on its own so the command always works. `--refresh` is rejected on commands with no cache
  rather than ignored.
- **The cache stores each playlist and track payload verbatim in a `payload` column**, which is
  what lets `--full` be served from disk without the schema growing a column every time Spotify
  adds a field. `playlist items --full` rebuilds the item shape from `tracks.payload` plus the
  `added_at` on the join row, so a track in ten playlists is stored once.
- **`playlist list --refresh` is a playlists-only refresh and must upsert, never replace.**
  `playlist_tracks` cascades on delete, so replacing playlist rows would silently empty the
  track cache. It also keeps its own freshness marker (`playlist_list_metadata`) apart from
  the track cache's (`playlist_cache_metadata`), so a light refresh cannot make
  `stats|search|artists|sample` believe they are current.
- Use only the Go standard library unless a dependency provides clear value.
- Target Spotify's post-February-2026 Web API paths (`/playlists/{id}/items`, not `/tracks`).
- OAuth uses Authorization Code with PKCE; never require or store a client secret.
- **Authentication failures carry their own remedy (`authError`, rendered as `{"error", "fix",
  "details"}`), so no caller has to probe `auth status` first.** Agents were running a status
  check before every command, which is a network round trip to learn something the next command
  would have said anyway. Missing credentials, a missing or dead refresh token, and a 401 from
  the API all collapse into the same shape. `authError` wraps its cause, so `errors.As` still
  reaches the `APIError` underneath a 401 and the 429 retry logic is unaffected.
- Spotify does not expose Extended Streaming History through its Web API; users must request and download that archive manually through Spotify's account privacy page.
- Queue mutation is append-only because Spotify does not expose remove, reorder, or clear
  operations. **Advancing is the exception**: `POST /me/player/next` and `/previous` exist and
  need `user-modify-playback-state`, which is already requested. `next --count N` is therefore
  the nearest thing to dropping the next N queued items — measured: two skips consumed both
  queued copies of a track and `previous` did not bring them back.
- **`previous` walks the play history, not the queue in reverse.** Measured on a live session:
  it returned to a track played earlier and restored the playlist it had been played from, so
  it is not an undo for `next`. Nor does it behave like the UI button, which restarts the
  current track a few seconds in. Landing in a one-track playlist also makes `queue get` answer
  with the same track repeated, which is the context being that short, not a bug.
- `skipTracks` stops at the first failure instead of continuing, because every later step would
  then move past the wrong track.
- **A 2xx body that is not JSON is a success, not a failure.** `POST /me/player/queue` answers
  200 with a 27-byte opaque token and no `Content-Type`, so rejecting non-JSON in `request`
  reported every track as failed while the queue actually filled up — `{"queued": 0, "failed":
  [...]}` next to a `queue get` showing all of them. Not a 204 with an empty body, which was
  always handled. Reads that come back non-JSON now fail at the caller's decode step instead,
  which at least names what it was decoding.
- `queue add` accepts multiple items and queues them sequentially (the API takes one URI per request); all URIs are validated up front so a malformed one aborts before any request, while per-item runtime failures are collected into a `failed` array instead of aborting. 429 responses retry with exponential backoff (`requestWithRetry`, honoring `Retry-After`); `retrySleep` is an overridable package var so tests exercise the backoff without waiting.
- **`resolve` is a spotctl command, not a Spotify one, which is why it may be bulk.** Spotify's
  `/v1/search` takes one query per request, so a variadic `search` would have to wrap Spotify's
  own envelope in something else and break the compatibility the trimmed shapes are careful to
  keep — `next`/`href` would no longer have a single referent. `resolve` instead answers with
  spotctl's own `results[]` shape, echoing each query, exactly like `playlist artists`, and fans
  out over `requestWithRetry` the way `queue add` does. It exists because resolving N
  recommendations to N IDs was N agent turns, which dominated the wall clock of the whole
  recommendation flow (the CLI itself costs under two seconds end to end).
- Sharing one `spotifyClient` across `resolve`'s goroutines is only safe because
  `newSpotifyClient` refreshes the token before any of them start; nothing mutates the
  credentials afterwards. A retry-on-401 inside `request` would have to take a lock first.
- `resolve` bounds concurrency at `resolveConcurrency` rather than firing every query at once,
  since the only thing unbounded fan-out buys is 429s that the backoff then sleeps off.
- Playlist caching uses a pure-Go SQLite driver, defaults to the OS user cache directory, and replaces the full cache atomically. Cache refresh metadata is stored separately so a successfully cached empty playlist library can be distinguished from an uninitialized cache.
- The cache stores track names and artists (`tracks`/`track_artists` tables) to power the offline query commands (`playlist artists|stats|search|sample`). A cache written before that schema errors with "predates track metadata" on those commands (`contains` still works), and `cache --max-age` treats such a cache as stale even when fresh by time, so agents never loop between "skip refresh" and "needs refresh".
- `playlist sample` picks a playlist uniformly at random and then a track within it, so heavily curated playlists do not dominate the sample.
