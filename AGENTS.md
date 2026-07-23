# AGENTS.md

`spotctl` is an agent-friendly Spotify search, playback, queue, and playlist CLI written in Go.

## Commands

- `go test ./...` — run tests.
- `go vet ./...` — run static checks.
- `go fmt ./...` — format Go files.

## Conventions

- Keep stdout machine-readable JSON; diagnostics belong on stderr.
- Use only the Go standard library unless a dependency provides clear value.
- Target Spotify's post-February-2026 Web API paths (`/playlists/{id}/items`, not `/tracks`).
- OAuth uses Authorization Code with PKCE; never require or store a client secret.
- Spotify does not expose Extended Streaming History through its Web API; users must request and download that archive manually through Spotify's account privacy page.
- Queue mutation is append-only because Spotify does not expose remove, reorder, or clear operations.
- `queue add` accepts multiple items and queues them sequentially (the API takes one URI per request); all URIs are validated up front so a malformed one aborts before any request, while per-item runtime failures are collected into a `failed` array instead of aborting. 429 responses retry with exponential backoff (`requestWithRetry`, honoring `Retry-After`); `retrySleep` is an overridable package var so tests exercise the backoff without waiting.
- Playlist caching uses a pure-Go SQLite driver, defaults to the OS user cache directory, and replaces the full cache atomically. Cache refresh metadata is stored separately so a successfully cached empty playlist library can be distinguished from an uninitialized cache.
- The cache stores track names and artists (`tracks`/`track_artists` tables) to power the offline query commands (`playlist artists|stats|search|sample`). A cache written before that schema errors with "predates track metadata" on those commands (`contains` still works), and `cache --max-age` treats such a cache as stale even when fresh by time, so agents never loop between "skip refresh" and "needs refresh".
- `playlist sample` picks a playlist uniformly at random and then a track within it, so heavily curated playlists do not dominate the sample.
