package audio

import "testing"

func TestBuildAudioFilter(t *testing.T) {
	cases := []struct {
		name       string
		replaygain bool
		loudnorm   bool
		gainDB     int
		want       string
	}{
		{"none", false, false, 0, ""},
		{"loudnorm only", false, true, 0, "loudnorm=I=-16:LRA=11:TP=-1.5"},
		{"replaygain only", true, false, 0, "volume=replaygain=track"},
		{"gain only positive", false, false, 6, "volume=+6dB"},
		{"gain only negative", false, false, -3, "volume=-3dB"},
		{"both with gain after loudnorm", false, true, 6, "loudnorm=I=-16:LRA=11:TP=-1.5,volume=+6dB"},
		{"replaygain before loudnorm", true, true, 0, "volume=replaygain=track,loudnorm=I=-16:LRA=11:TP=-1.5"},
		{"full chain", true, true, 2, "volume=replaygain=track,loudnorm=I=-16:LRA=11:TP=-1.5,volume=+2dB"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildAudioFilter(c.replaygain, c.loudnorm, c.gainDB)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
