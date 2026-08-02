package httpx

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The LRC parser is the one piece of non-trivial logic that lives in
// JavaScript, and it handles input from a third party in a format with a lot
// of dialects. It is exercised here by pulling the pure functions out of
// utils.js and running them under node, so the cases live with the rest of
// the suite rather than in a scratch file. Skipped when node is unavailable
func TestLyricsParserUnderNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node unavailable: %v", err)
	}

	src, ok := staticFiles["utils.js"]
	if !ok {
		t.Fatal("utils.js missing from the embedded static set")
	}

	// Take just the two pure functions so the harness does not need a DOM
	pick := func(name string) string {
		re := regexp.MustCompile(`(?s)function ` + name + `\(.*?\n}`)
		m := re.Find(src)
		if m == nil {
			t.Fatalf("could not find %s in utils.js", name)
		}
		return string(m)
	}

	harness := pick("parseLyrics") + "\n" + pick("activeLyricIndex") + "\n" + `
let fail = 0;
const check = (name, cond, extra) => {
  if (!cond) { fail++; console.log("FAIL  " + name + "  " + JSON.stringify(extra)); }
};

// Windows line endings are common in LRC files. A trailing \r defeats the $
// anchor, which silently turned every line into untimed plain text
let p = parseLyrics("[00:01.00]one\r\n[00:02.00]two\r\n[00:20.00]six");
check("crlf synced", p.synced === true, p);
check("crlf line count", p.lines.length === 3, p.lines);
check("crlf text clean", p.lines[0].text === "one", p.lines[0]);
check("crlf highlight past end", activeLyricIndex(p.lines, 25) === 2, activeLyricIndex(p.lines, 25));

// Untimed lines inside a synced document broke the binary search invariant,
// because the sort put them first while the search treated them as past the
// target, hiding every real timestamp behind them
p = parseLyrics("stray header\n[00:10.00]first\n[00:20.00]second");
check("untimed dropped when synced", p.lines.every(l => l.t !== null), p.lines);
check("mixed doc highlights last", activeLyricIndex(p.lines, 25) === p.lines.length - 1, p.lines);
check("before first cue", activeLyricIndex(p.lines, 0) === -1, null);

// Real LRC metadata goes, a bracketed lyric annotation stays
p = parseLyrics("[ar:Coldplay]\n[offset:+500]\n[Verse: 1]\nreal words");
check("metadata dropped", !p.lines.some(l => /^\[ar:|^\[offset:/.test(l.text)), p.lines);
check("annotation kept", p.lines.some(l => l.text === "[Verse: 1]"), p.lines);

// A repeated chorus carries several timestamps on one line
p = parseLyrics("[00:10.00][01:20.00]chorus");
check("chorus expands", p.lines.length === 2, p.lines);
check("chorus ordered", p.lines[0].t === 10 && p.lines[1].t === 80, p.lines);

// Some writers use a colon before the fraction
p = parseLyrics("[00:12:30]colon variant");
check("colon fraction", p.synced && Math.abs(p.lines[0].t - 12.3) < 0.001, p.lines);

// Plain text keeps its blank lines so stanza breaks survive
p = parseLyrics("verse one\n\nverse two");
check("plain not synced", p.synced === false, p);
check("plain keeps blank", p.lines.length === 3, p.lines);

// Nothing may throw, whatever arrives
for (const bad of ["", null, undefined, "[[[", "]]]", "\n\n\n", "x".repeat(5000)]) {
  try { parseLyrics(bad); } catch (e) { fail++; console.log("FAIL  threw on " + JSON.stringify(bad) + ": " + e.message); }
}
check("empty search", activeLyricIndex([], 5) === -1, null);

console.log(fail === 0 ? "PASS" : fail + " failures");
process.exit(fail === 0 ? 0 : 1);
`

	dir := t.TempDir()
	script := filepath.Join(dir, "lyrics_test.js")
	if err := os.WriteFile(script, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("lyrics parser failed its cases:\n%s", strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("unexpected harness output:\n%s", out)
	}
}
