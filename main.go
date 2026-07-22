package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			_ = json.NewEncoder(os.Stderr).Encode(map[string]any{
				"error":   apiErr.Message,
				"status":  apiErr.Status,
				"details": apiErr.Details,
			})
		} else {
			_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"error": err.Error()})
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	switch args[0] {
	case "auth":
		return runAuth(args[1:])
	case "search":
		return runSearch(args[1:])
	case "queue":
		return runQueue(args[1:])
	case "playlist":
		return runPlaylist(args[1:])
	case "version", "--version", "-v":
		return writeJSON(map[string]string{"version": version})
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `spotctl controls Spotify through its Web API.

Usage:
  spotctl auth login [--client-id ID] [--redirect-uri URI]
  spotctl auth status
  spotctl auth logout
  spotctl search [--type track|album|artist|playlist] [--limit N] QUERY
  spotctl queue get
  spotctl queue add [--device ID] ITEM
  spotctl playlist list [--limit N]
  spotctl playlist get PLAYLIST
  spotctl playlist create --name NAME [--description TEXT] [--public]
  spotctl playlist update PLAYLIST [--name NAME] [--description TEXT] [--public BOOL] [--collaborative BOOL]
  spotctl playlist add PLAYLIST ITEM...
  spotctl playlist remove PLAYLIST ITEM...
  spotctl playlist delete PLAYLIST

ITEM may be a Spotify URI, open.spotify.com URL, or bare track ID.
All command output is JSON.
`)
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
