package audio

import "testing"

func TestBuildAudioFilter(t *testing.T) {
	cases := []struct {
		name     string
		loudnorm bool
		gainDB   int
		want     string
	}{
		{"none", false, 0, ""},
		{"loudnorm only", true, 0, "loudnorm=I=-16:LRA=11:TP=-1.5"},
		{"gain only positive", false, 6, "volume=+6dB"},
		{"gain only negative", false, -3, "volume=-3dB"},
		{"both with gain after loudnorm", true, 6, "loudnorm=I=-16:LRA=11:TP=-1.5,volume=+6dB"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildAudioFilter(c.loudnorm, c.gainDB)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
