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
- Playlist caching uses a pure-Go SQLite driver, defaults to the OS user cache directory, and replaces the full cache atomically.
