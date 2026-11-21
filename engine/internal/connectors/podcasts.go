package connectors

import (
	"context"
	"time"
)

// CapPodcastSearch is the capability of a connector that searches podcast
// episodes by what they say about themselves: titles, show notes and the
// people tagged in them.
const CapPodcastSearch = "podcasts.search"

// PodcastQuery is one podcast search.
type PodcastQuery struct {
	// Term is what to look for, as said (a brand name, a website).
	Term string
	// Max is how many episodes to return at most.
	Max int
}

// PodcastEpisode is one episode a search found.
type PodcastEpisode struct {
	// ID is the directory's id for the episode.
	ID          string
	Title       string
	Description string
	// Link is the episode's web page, or empty.
	Link string
	// AudioURL is the episode's recording.
	AudioURL string
	Seconds  int
	// Published is when the episode came out.
	Published *time.Time
	// The show it is part of.
	ShowID    string
	ShowTitle string
	ShowLink  string
	ShowImage string
	// Author is who the show says makes it.
	Author string
	// TranscriptURL is a transcript the show publishes in its feed
	// (podcast:transcript), and TranscriptType its media type
	// (text/vtt, application/x-subrip, application/json, text/html,
	// text/plain). Empty when it has none.
	TranscriptURL  string
	TranscriptType string
}

// PodcastSearcher searches podcast episodes.
type PodcastSearcher interface {
	Connector
	SearchPodcastEpisodes(ctx context.Context, c *Client, creds Creds, q PodcastQuery) ([]PodcastEpisode, error)
}
