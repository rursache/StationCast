package playlist

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestVersuriSlug(t *testing.T) {
	cases := []struct {
		in             string
		dropConnectors bool
		want           string
	}{
		{"Florin Peste", true, "florin-peste"},
		{"Ce Blonda Tare", false, "ce-blonda-tare"},
		// the site omits connector words from the artist part: the form with
		// "si" does not resolve, the form without it does
		{"Nicolae Guta si Modjo", true, "nicolae-guta-modjo"},
		{"Kalif x Luis Gabriel", true, "kalif-luis-gabriel"},
		{"A feat. B", true, "a-b"},
		// diacritics fold to the ASCII the site uses in its URLs
		{"Ce Blondă Tare", false, "ce-blonda-tare"},
		{"Florin Pește", true, "florin-peste"},
		{"Țambal Și Vioară", false, "tambal-si-vioara"},
		// punctuation collapses rather than doubling up hyphens
		{"Fata Care M-a Dat Gata", false, "fata-care-m-a-dat-gata"},
		{"  Spaced   Out  ", false, "spaced-out"},
		{"!!!", false, ""},
		{"", false, ""},
		// connector words are kept in a title, where they are real words
		{"Cu Tine sau Fara Tine", false, "cu-tine-sau-fara-tine"},
	}
	for _, c := range cases {
		if got := versuriSlug(c.in, c.dropConnectors); got != c.want {
			t.Errorf("versuriSlug(%q, drop=%v) = %q, want %q", c.in, c.dropConnectors, got, c.want)
		}
	}
}

func TestFoldDiacritics(t *testing.T) {
	cases := map[string]string{
		"Blondă":  "Blonda",
		"Pește":   "Peste",
		"Ț":       "T",
		"ș":       "s",
		"ş":       "s",
		"ţ":       "t",
		"plain":   "plain",
		"Ünïcödé": "Unicode",
	}
	for in, want := range cases {
		if got := foldDiacritics(in); got != want {
			t.Errorf("foldDiacritics(%q) = %q, want %q", in, got, want)
		}
	}
}

// The lyric container holds nested divs for adverts. Taking the first closing
// tag truncates the lyrics to their opening verse, which looked like the site
// only storing a preview
func TestExtractVersuriLyricsHandlesNestedDivs(t *testing.T) {
	page := `<html><body>
<div id="textdiv">
Look at the stars,<br>
Look how they shine for you,<br>
<div class="ad"><script>googletag.cmd.push(function(){});</script></div>
I wrote a song for you,<br>
And all the things you do.<br>
</div>
<div id="footer">not lyrics</div>
</body></html>`
	got := extractVersuriLyrics(page)
	for _, want := range []string{"Look at the stars,", "I wrote a song for you,", "And all the things you do."} {
		if !strings.Contains(got, want) {
			t.Errorf("extracted text is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "googletag") {
		t.Errorf("advert script leaked into the lyrics:\n%s", got)
	}
	if strings.Contains(got, "not lyrics") {
		t.Errorf("content past the container leaked in:\n%s", got)
	}
}

func TestExtractVersuriLyricsEdgeCases(t *testing.T) {
	if got := extractVersuriLyrics("<html><body>no container here</body></html>"); got != "" {
		t.Errorf("a page with no lyric container returned %q", got)
	}
	if got := extractVersuriLyrics(""); got != "" {
		t.Errorf("an empty page returned %q", got)
	}
	if got := extractVersuriLyrics(`<div id="textdiv"></div>`); got != "" {
		t.Errorf("an empty container returned %q", got)
	}
	// entities are decoded, and a run of breaks collapses to one blank line
	got := extractVersuriLyrics(`<div id="textdiv">Rock &amp; Roll<br><br><br>Don&#39;t stop</div>`)
	if !strings.Contains(got, "Rock & Roll") || !strings.Contains(got, "Don't stop") {
		t.Errorf("entities were not decoded: %q", got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("blank lines were not collapsed: %q", got)
	}
	// an unclosed container must not run away or panic
	if got := extractVersuriLyrics(`<div id="textdiv">dangling<br>lines`); !strings.Contains(got, "dangling") {
		t.Errorf("an unclosed container lost its text: %q", got)
	}
}

func TestQueryVersuri(t *testing.T) {
	page := `<div id="textdiv">O blonda cu paru ondulat,<br>Dintr-o privire m-a fermecat.</div>`

	t.Run("hit", func(t *testing.T) {
		var gotPath string
		srv := lyricsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			_, _ = w.Write([]byte(page))
		})
		ob := versuriBaseURL
		versuriBaseURL = srv
		t.Cleanup(func() { versuriBaseURL = ob })

		lib := &Library{}
		res, err := lib.queryVersuri(context.Background(), "Florin Peste", "Ce Blonda Tare")
		if err != nil {
			t.Fatalf("queryVersuri: %v", err)
		}
		if res.source != "versuri.ro" {
			t.Errorf("source = %q", res.source)
		}
		if res.SyncedLyrics != "" {
			t.Error("claimed synced lyrics, which this provider cannot supply")
		}
		if !strings.Contains(res.PlainLyrics, "O blonda cu paru ondulat,") {
			t.Errorf("PlainLyrics = %q", res.PlainLyrics)
		}
		if want := "/florin-peste-ce-blonda-tare/"; gotPath != want {
			t.Errorf("requested %q, want %q", gotPath, want)
		}
	})

	t.Run("404 is a definitive miss", func(t *testing.T) {
		srv := lyricsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		ob := versuriBaseURL
		versuriBaseURL = srv
		t.Cleanup(func() { versuriBaseURL = ob })

		lib := &Library{}
		if _, err := lib.queryVersuri(context.Background(), "A", "B"); !errors.Is(err, errNoLyricsMatch) {
			t.Errorf("error = %v, want errNoLyricsMatch", err)
		}
	})

	// The site answers 200 with a shell page for an unknown slug, so an empty
	// container is a miss and not something to cache
	t.Run("200 with no lyrics is a miss", func(t *testing.T) {
		srv := lyricsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`<html><body><div id="nav">nothing here</div></body></html>`))
		})
		ob := versuriBaseURL
		versuriBaseURL = srv
		t.Cleanup(func() { versuriBaseURL = ob })

		lib := &Library{}
		if _, err := lib.queryVersuri(context.Background(), "A", "B"); !errors.Is(err, errNoLyricsMatch) {
			t.Errorf("error = %v, want errNoLyricsMatch", err)
		}
	})

	t.Run("outage is transient", func(t *testing.T) {
		srv := lyricsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		})
		ob := versuriBaseURL
		versuriBaseURL = srv
		t.Cleanup(func() { versuriBaseURL = ob })

		lib := &Library{}
		_, err := lib.queryVersuri(context.Background(), "A", "B")
		if err == nil || errors.Is(err, errNoLyricsMatch) {
			t.Errorf("error = %v, want a transient error so the track is retried", err)
		}
	})

	t.Run("unusable input", func(t *testing.T) {
		lib := &Library{}
		for _, c := range []struct{ a, ti string }{{"", "x"}, {"x", ""}, {"!!!", "x"}, {"x", "!!!"}} {
			if _, err := lib.queryVersuri(context.Background(), c.a, c.ti); !errors.Is(err, errNoLyricsMatch) {
				t.Errorf("queryVersuri(%q, %q) error = %v, want errNoLyricsMatch", c.a, c.ti, err)
			}
		}
	})
}
