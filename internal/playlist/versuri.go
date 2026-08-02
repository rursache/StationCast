package playlist

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// versuri.ro is a Romanian community lyrics site. It is the last provider
// tried because it has no API: pages are fetched and parsed, which breaks
// whenever the site is restyled. It earns its place because it carries
// Romanian music, manele especially, that the international databases miss
// entirely, and those misses are not rare in a library of that genre.
//
// Only the lyric pages are read. Its robots.txt blocks a list of named
// crawlers outright and disallows /text/, /images/avatars/, /print/,
// /rssout.php and /vers.php for everyone else, none of which is touched here
var versuriBaseURL = "https://www.versuri.ro/versuri"

// The slug is built from the artist and title. Connector words are dropped
// because the site omits them: "Nicolae Guta si Modjo" is filed under
// nicolae-guta-modjo, and the form with "si" does not resolve
var versuriConnectors = map[string]bool{
	"si": true, "and": true, "feat": true, "ft": true, "featuring": true,
	"x": true, "vs": true, "with": true, "the": false,
}

var versuriNonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// versuriSlug folds a string to the site's URL form: diacritics removed,
// lowercased, everything else collapsed to single hyphens
func versuriSlug(s string, dropConnectors bool) string {
	folded := foldDiacritics(strings.ToLower(strings.TrimSpace(s)))
	parts := versuriNonSlug.Split(folded, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		if dropConnectors && versuriConnectors[p] {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, "-")
}

// foldDiacritics strips combining marks, so "Ce Blondă Tare" and "Florin
// Pește" reduce to the ASCII forms the site uses in its URLs
func foldDiacritics(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		return s
	}
	// the comma-below forms used in Romanian do not always decompose
	r := strings.NewReplacer("ș", "s", "ş", "s", "ț", "t", "ţ", "t", "ș", "s")
	return r.Replace(out)
}

// queryVersuri fetches and parses a lyric page. Plain text only, so it can
// never displace a synced result from an earlier provider
func (l *Library) queryVersuri(ctx context.Context, artist, title string) (*lrclibResponse, error) {
	if artist == "" || title == "" {
		return nil, errNoLyricsMatch
	}
	a, tt := versuriSlug(artist, true), versuriSlug(title, false)
	if a == "" || tt == "" {
		return nil, errNoLyricsMatch
	}

	reqCtx, cancel := context.WithTimeout(ctx, lyricsTimeout)
	defer cancel()
	url := versuriBaseURL + "/" + a + "-" + tt + "/"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", lrclibUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errNoLyricsMatch
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("versuri status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*maxLyricsBytes))
	if err != nil {
		return nil, err
	}
	lyrics := extractVersuriLyrics(string(body))
	if lyrics == "" {
		// The site answers 200 with a shell page for an unknown slug, so an
		// empty body is a miss rather than an error
		return nil, errNoLyricsMatch
	}
	return &lrclibResponse{
		source:      "versuri.ro",
		ArtistName:  artist,
		TrackName:   title,
		PlainLyrics: lyrics,
	}, nil
}

var versuriTagRe = regexp.MustCompile(`(?is)<(/?)div\b[^>]*>`)
var versuriBrRe = regexp.MustCompile(`(?is)<br\s*/?>`)
var versuriStripRe = regexp.MustCompile(`(?is)<(script|style|ins|iframe)\b.*?</(?:script|style|ins|iframe)>`)
var versuriAnyTagRe = regexp.MustCompile(`(?is)<[^>]+>`)

// extractVersuriLyrics pulls the lyric text out of the page. The container
// holds nested divs for adverts, so the closing tag is found by counting
// depth rather than taking the first one, which otherwise truncates the
// lyrics to their opening verse
func extractVersuriLyrics(page string) string {
	i := strings.Index(page, `<div id="textdiv"`)
	if i < 0 {
		return ""
	}
	j := strings.Index(page[i:], ">")
	if j < 0 {
		return ""
	}
	j += i + 1

	depth, end := 1, -1
	for _, m := range versuriTagRe.FindAllStringSubmatchIndex(page[j:], -1) {
		if page[j+m[2]:j+m[3]] == "/" {
			depth--
		} else {
			depth++
		}
		if depth == 0 {
			end = j + m[0]
			break
		}
	}
	if end < 0 {
		end = len(page)
	}

	inner := versuriStripRe.ReplaceAllString(page[j:end], "")
	inner = versuriBrRe.ReplaceAllString(inner, "\n")
	inner = versuriAnyTagRe.ReplaceAllString(inner, "")
	return tidyLyrics(unescapeHTML(inner))
}

// tidyLyrics trims each line and collapses runs of blank lines, since the
// markup puts a break between every line
func tidyLyrics(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			blank++
			if blank > 1 || len(out) == 0 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

var htmlEntities = strings.NewReplacer(
	"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`,
	"&#39;", "'", "&apos;", "'", "&nbsp;", " ", "&#039;", "'",
)

func unescapeHTML(s string) string { return htmlEntities.Replace(s) }
