package web

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// feedContentType is the media type podcast apps expect for an RSS document.
const feedContentType = "application/rss+xml; charset=utf-8"

// handleFeed serves one source's podcast RSS feed, so a podcast app can
// subscribe to the channel and receive every archived video as an episode.
//
// The path is never taken from the request: the numeric id is formatted into
// the filename the feed writer uses, so a crafted URL cannot reach anything
// outside the feed directory. A feed that does not exist yet — the writer
// creates it after the source's first download — is a plain 404 with a hint,
// not an error.
func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}

	path := filepath.Join(s.deps.FeedDir, fmt.Sprintf("%d.xml", id))
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		http.Error(w, "no feed for this source yet — it appears after the first download",
			http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", feedContentType)
	http.ServeFile(w, r, path)
}
