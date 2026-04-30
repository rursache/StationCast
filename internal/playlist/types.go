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

func (t Track) DisplayTitle() string {
	if t.Title == "" {
		return ""
	}
	if t.Artist == "" {
		return t.Title
	}
	return t.Artist + " - " + t.Title
}
