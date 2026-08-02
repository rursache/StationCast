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

// The offset control and the fetch race were both real bugs. They touch a
// little browser surface, so it is stubbed rather than pulling in a DOM
func TestLyricsOffsetAndLoadRaceUnderNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node unavailable: %v", err)
	}
	src, ok := staticFiles["utils.js"]
	if !ok {
		t.Fatal("utils.js missing from the embedded static set")
	}

	pick := func(name string) string {
		re := regexp.MustCompile(`(?s)function ` + name + `\(.*?\n}`)
		m := re.Find(src)
		if m == nil {
			t.Fatalf("could not find %s in utils.js", name)
		}
		return string(m)
	}
	pickConst := func(name string) string {
		re := regexp.MustCompile(`const ` + name + ` = [^\n]+`)
		m := re.Find(src)
		if m == nil {
			t.Fatalf("could not find const %s in utils.js", name)
		}
		return string(m)
	}

	harness := strings.Join([]string{
		pickConst("LYRICS_OFFSET_KEY"),
		pickConst("LYRICS_OFFSET_DEFAULT"),
		pickConst("LYRICS_OFFSET_MIN"),
		pickConst("LYRICS_OFFSET_MAX"),
		pick("getLyricsOffset"),
		pick("setLyricsOffset"),
		pick("parseLyrics"),
		pick("activeLyricIndex"),
		pick("escapeHTML"),
		pick("openModal"),
		pick("closeModal"),
		pick("initLyrics"),
	}, "\n") + `

let fail = 0;
const check = (name, cond, extra) => {
  if (!cond) { fail++; console.log("FAIL  " + name + "  " + JSON.stringify(extra)); }
};

// --- offset persistence, including storage that throws --------------
let store = {};
globalThis.localStorage = {
  getItem: k => (k in store ? store[k] : null),
  setItem: (k, v) => { store[k] = String(v); },
};
check("default when unset", getLyricsOffset() === LYRICS_OFFSET_DEFAULT, getLyricsOffset());
setLyricsOffset(8);
check("round trips", getLyricsOffset() === 8, getLyricsOffset());
check("clamps above max", setLyricsOffset(LYRICS_OFFSET_MAX + 50) === LYRICS_OFFSET_MAX, null);
check("clamps below min", setLyricsOffset(LYRICS_OFFSET_MIN - 50) === LYRICS_OFFSET_MIN, null);
store = { [LYRICS_OFFSET_KEY]: "not a number" };
check("garbage falls back to default", getLyricsOffset() === LYRICS_OFFSET_DEFAULT, getLyricsOffset());

// storage that throws outright, as in a sandboxed frame. This used to take
// down every piece of page setup wired after it
globalThis.localStorage = {
  getItem: () => { throw new Error("storage disabled"); },
  setItem: () => { throw new Error("storage disabled"); },
};
let threw = false;
try { check("returns default when storage throws", getLyricsOffset() === LYRICS_OFFSET_DEFAULT, null); }
catch (e) { threw = true; }
check("getLyricsOffset does not throw", !threw, null);
try { setLyricsOffset(7); } catch (e) { fail++; console.log("FAIL  setLyricsOffset threw"); }

// --- the stale fetch race -------------------------------------------
store = {};
globalThis.localStorage = { getItem: k => (k in store ? store[k] : null), setItem: (k, v) => { store[k] = String(v); } };
globalThis.setInterval = () => 0;

const el = () => ({ _c: new Set(), classList: {
    add(...c) { c.forEach(x => this._owner._c.add(x)); },
    remove(...c) { c.forEach(x => this._owner._c.delete(x)); },
    toggle(c, on) { on ? this._owner._c.add(c) : this._owner._c.delete(c); },
    contains(c) { return this._owner._c.has(c); } },
  setAttribute() {}, addEventListener() {}, querySelectorAll: () => [], innerHTML: "" });
const mk = () => { const o = el(); o.classList._owner = o; return o; };

const els = { button: mk(), modal: mk(), body: mk(), syncControls: mk(), offsetLabel: mk(), minus: mk(), plus: mk() };

// slow track A resolves after fast track B
let resolveA;
globalThis.fetch = (url) => {
  if (url.endsWith("/A")) return new Promise(r => { resolveA = () => r({ ok: true, text: async () => "[00:01.00]song A" }); });
  return Promise.resolve({ ok: true, text: async () => "[00:02.00]song B" });
};

const ctl = initLyrics(els, () => ({ has_lyrics: true, lyrics_url: "/lyrics/B", started_at: 0 }));
(async () => {
  ctl.sync({ has_lyrics: true, lyrics_url: "/lyrics/A", started_at: 0 });   // slow
  ctl.sync({ has_lyrics: true, lyrics_url: "/lyrics/B", started_at: 0 });   // fast, wins
  await new Promise(r => setTimeout(r, 30));
  resolveA();                                                               // A lands late
  await new Promise(r => setTimeout(r, 30));
  check("stale fetch does not overwrite the current track", els.body.innerHTML.includes("song B"), els.body.innerHTML);
  check("stale fetch content absent", !els.body.innerHTML.includes("song A"), els.body.innerHTML);

  // a track with no lyrics hides the button again
  ctl.sync({ has_lyrics: false });
  check("button hidden when the track has no lyrics", els.button.classList.contains("hidden"), null);

  console.log(fail === 0 ? "PASS" : fail + " failures");
  process.exit(fail === 0 ? 0 : 1);
})();
`

	dir := t.TempDir()
	script := filepath.Join(dir, "offset_race_test.js")
	if err := os.WriteFile(script, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("lyrics controller failed its cases:\n%s", strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("unexpected harness output:\n%s", out)
	}
}
