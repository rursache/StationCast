package playlist

type Track struct {
	ID         int64
	Path       string
	Size       int64
	MTime      int64
	Title      string
	Artist     string
	Album      string
	DurationMS int64
	HasArt     bool
	AddedAt    int64
}

// DisplayArtist returns the file's artist tag, or the supplied fallback
// (typically the station name) when the tag is empty
func (t Track) DisplayArtist(fallback string) string {
	if t.Artist != "" {
		return t.Artist
	}
	return fallback
}

// DisplayLine renders "Artist - Title" for the public stream metadata, with
// the supplied fallback substituted whenever the artist tag is missing.
// Title is always non-empty after a scan (the library falls back to the
// filename without extension when no tag is present)
func (t Track) DisplayLine(fallbackArtist string) string {
	if t.Title == "" {
		return ""
	}
	artist := t.DisplayArtist(fallbackArtist)
	if artist == "" {
		return t.Title
	}
	return artist + " - " + t.Title
}
