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
// pinned, since every other check here compares against the constant and
// would pass just as happily if the shipped default drifted
check("default is the measured -0.5s", LYRICS_OFFSET_DEFAULT === -0.5, LYRICS_OFFSET_DEFAULT);
check("default sits on the nudge grid", (LYRICS_OFFSET_DEFAULT * 2) % 1 === 0, LYRICS_OFFSET_DEFAULT);
check("default is within the allowed range",
  LYRICS_OFFSET_DEFAULT >= LYRICS_OFFSET_MIN && LYRICS_OFFSET_DEFAULT <= LYRICS_OFFSET_MAX, LYRICS_OFFSET_DEFAULT);
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

// makeSortable is pointer driven so it works with a finger as well as a
// mouse. The row follows the pointer and the DOM is reordered once on
// release, so the arithmetic that decides the landing slot is worth pinning
// down without a browser
func TestSortableUnderNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node unavailable: %v", err)
	}
	src, ok := staticFiles["utils.js"]
	if !ok {
		t.Fatal("utils.js missing from the embedded static set")
	}
	re := regexp.MustCompile(`(?s)function makeSortable\(.*?\n}`)
	fn := re.Find(src)
	if fn == nil {
		t.Fatal("makeSortable not found in utils.js")
	}

	harness := string(fn) + `

let fail = 0;
const check = (name, cond, extra) => {
  if (!cond) { fail++; console.log("FAIL  " + name + "  " + JSON.stringify(extra)); }
};

const PITCH = 20;

// A list of rows PITCH tall, stacked from y=0
function makeList(n) {
  const children = [];
  const list = {
    children,
    contains: () => true,
    _h: {},
    addEventListener(t, fn) { this._h[t] = fn; },
    insertBefore(el, ref) {
      const i = children.indexOf(el);
      if (i >= 0) children.splice(i, 1);
      const j = ref === null || ref === undefined ? children.length : children.indexOf(ref);
      children.splice(j < 0 ? children.length : j, 0, el);
    },
    fire(t, e) { (this._h[t] || (() => {}))(e); },
  };
  for (let i = 0; i < n; i++) {
    const el = {
      id: String(i), _i: i, _cls: new Set(), style: {},
      classList: { add: c => el._cls.add(c), remove: c => el._cls.delete(c), contains: c => el._cls.has(c) },
      getBoundingClientRect() {
        const top = children.indexOf(el) * PITCH;
        return { top, height: PITCH, bottom: top + PITCH, left: 0 };
      },
      get nextSibling() { const j = children.indexOf(el); return j >= 0 && j + 1 < children.length ? children[j + 1] : null; },
      closest: s => (s === '[data-sortable-item]' ? el : null),
      setPointerCapture() {}, focus() {},
    };
    el.handle = { closest: s => (s === '[data-sortable-item]' ? el : el.handle), setPointerCapture() {}, focus() {} };
    children.push(el);
  }
  return list;
}

// a real pointerdown always carries clientY, and the drag origin is read
// from it, so the stub has to supply one
const down = (list, el, y) => list.fire('pointerdown', {
  pointerId: 1, clientY: y === undefined ? el.getBoundingClientRect().top + PITCH / 2 : y,
  target: { closest: s => (s === '[data-sort-handle]' ? el.handle : null) }, preventDefault() {},
});
const moveTo = (list, y) => list.fire('pointermove', { pointerId: 1, clientY: y, preventDefault() {} });
const up = (list, y) => list.fire('pointerup', { pointerId: 1, clientY: y, preventDefault() {} });

// --- landing slot arithmetic ---
let moved = null;
let list = makeList(4);
makeSortable(list, (from, to) => { moved = [from, to]; });
let el0 = list.children[0];
down(list, el0);
moveTo(list, PITCH / 2 + 2 * PITCH);   // two rows down
up(list, PITCH / 2 + 2 * PITCH);
check("dragging down two rows lands two down", moved && moved[0] === 0 && moved[1] === 2, moved);
check("dom reordered on release", list.children.map(c => c.id).join() === "1,2,0,3", list.children.map(c => c.id));

// dragging up
moved = null;
list = makeList(4);
makeSortable(list, (from, to) => { moved = [from, to]; });
let el3 = list.children[3];
down(list, el3);
moveTo(list, 3 * PITCH + PITCH / 2 - 3 * PITCH);
up(list, 3 * PITCH + PITCH / 2 - 3 * PITCH);
check("dragging to the top lands first", moved && moved[0] === 3 && moved[1] === 0, moved);
check("dom order after moving up", list.children.map(c => c.id).join() === "3,0,1,2", list.children.map(c => c.id));

// --- the row follows the pointer, neighbours slide aside ---
list = makeList(4);
makeSortable(list, () => {});
const drag0 = list.children[0];
down(list, drag0);
check("dragged row is marked", drag0._cls.has("sorting"), [...drag0._cls]);
check("other rows are marked for transition", list.children.slice(1).every(r => r._cls.has("sort-shifting")), null);
moveTo(list, PITCH / 2 + 30);
check("dragged row tracks the pointer", drag0.style.transform === "translateY(30px)", drag0.style.transform);
check("passed neighbour slides up one pitch", list.children[1].style.transform === "translateY(-" + PITCH + "px)", list.children[1].style.transform);
check("row beyond the target is untouched", !list.children[3].style.transform, list.children[3].style.transform);
check("dom order is stable during the drag", list.children.map(c => c.id).join() === "0,1,2,3", list.children.map(c => c.id));
up(list, PITCH / 2 + 30);
check("transform cleared on release", !drag0.style.transform, drag0.style.transform);
check("classes cleared on release", !drag0._cls.has("sorting") && !list.children[0]._cls.has("sort-shifting"), null);

// --- clamped to the ends ---
moved = null;
list = makeList(3);
makeSortable(list, (from, to) => { moved = [from, to]; });
down(list, list.children[0]);
moveTo(list, 9999);      // far past the bottom
up(list, 9999);
check("dragging past the end clamps to the last slot", moved && moved[1] === 2, moved);

moved = null;
list = makeList(3);
makeSortable(list, (from, to) => { moved = [from, to]; });
down(list, list.children[2]);
moveTo(list, -9999);     // far above the top
up(list, -9999);
check("dragging past the top clamps to the first slot", moved && moved[1] === 0, moved);

// --- things that must not report a move ---
moved = null;
list = makeList(3);
makeSortable(list, (from, to) => { moved = [from, to]; });
down(list, list.children[0]);
up(list, PITCH / 2);
check("a press without dragging is not a move", moved === null, moved);

moved = null;
list = makeList(3);
const s2 = makeSortable(list, (from, to) => { moved = [from, to]; });
list.fire('pointerdown', { pointerId: 1, target: { closest: () => null }, preventDefault() {} });
check("a press off the handle does not start a drag", s2.dragging() === false, null);
moveTo(list, 50); up(list, 50);
check("an off-handle drag reports nothing", moved === null, moved);

// --- in-flight state, used to hold off a re-render ---
list = makeList(3);
const s3 = makeSortable(list, () => {});
check("idle at rest", s3.dragging() === false, null);
down(list, list.children[0]);
check("dragging while the pointer is down", s3.dragging() === true, null);
up(list, 0);
check("idle after release", s3.dragging() === false, null);

// a cancelled pointer must clean up and report nothing
moved = null;
list = makeList(3);
const s4 = makeSortable(list, (from, to) => { moved = [from, to]; });
const c0 = list.children[0];
down(list, c0);
moveTo(list, PITCH / 2 + 2 * PITCH);
list.fire('pointercancel', { pointerId: 1, clientY: PITCH / 2 + 2 * PITCH, preventDefault() {} });
check("a cancelled pointer ends the drag", s4.dragging() === false, null);
check("a cancelled pointer clears the transform", !c0.style.transform, c0.style.transform);
// the browser took the gesture away, the user did not drop it there
check("a cancelled pointer reports no move", moved === null, moved);
check("a cancelled pointer leaves the dom alone", list.children.map(c => c.id).join() === "0,1,2", list.children.map(c => c.id));

// losing capture without a pointerup must not wedge the drag on forever,
// since a stuck drag holds off every later refresh of the pane
moved = null;
list = makeList(3);
const s7 = makeSortable(list, (from, to) => { moved = [from, to]; });
const w0 = list.children[0];
down(list, w0);
moveTo(list, PITCH / 2 + 2 * PITCH);
list.fire('lostpointercapture', { pointerId: 1, clientY: PITCH / 2 + 2 * PITCH, preventDefault() {} });
check("losing capture ends the drag", s7.dragging() === false, null);
check("losing capture clears the transform", !w0.style.transform, w0.style.transform);
check("losing capture reports no move", moved === null, moved);
// a new drag has to be possible afterwards
down(list, list.children[0]);
check("a drag can start again after capture was lost", s7.dragging() === true, null);
up(list, PITCH / 2);

// capture is released on every normal drop too, so the event fires after
// pointerup and must not undo or double-apply the move it just made
moved = null;
list = makeList(3);
makeSortable(list, (from, to) => { moved = [from, to]; });
down(list, list.children[0]);
moveTo(list, PITCH / 2 + PITCH);
up(list, PITCH / 2 + PITCH);
const afterUp = list.children.map(c => c.id).join();
list.fire('lostpointercapture', { pointerId: 1, clientY: PITCH / 2 + PITCH, preventDefault() {} });
check("capture loss after a drop reports only one move", moved && moved[0] === 0 && moved[1] === 1, moved);
check("capture loss after a drop leaves the order alone", list.children.map(c => c.id).join() === afterUp, afterUp);

// a second pointer must not hijack a drag in progress
list = makeList(3);
const s5 = makeSortable(list, () => {});
down(list, list.children[0]);
list.fire('pointermove', { pointerId: 99, clientY: 500, preventDefault() {} });
check("a different pointer is ignored", !list.children[0].style.transform || list.children[0].style.transform === "", list.children[0].style.transform);
up(list, 0);

// --- keyboard reordering, the pointer-free path ---
const key = (list, el, k) => list.fire('keydown', {
  key: k, target: { closest: s => (s === '[data-sort-handle]' ? el.handle : null) }, preventDefault() {},
});

moved = null;
list = makeList(4);
makeSortable(list, (from, to) => { moved = [from, to]; });
key(list, list.children[0], 'ArrowDown');
check("arrow down reports a move one row down", moved && moved[0] === 0 && moved[1] === 1, moved);
check("arrow down reorders the dom", list.children.map(c => c.id).join() === "1,0,2,3", list.children.map(c => c.id));

moved = null;
list = makeList(4);
makeSortable(list, (from, to) => { moved = [from, to]; });
key(list, list.children[2], 'ArrowUp');
check("arrow up reports a move one row up", moved && moved[0] === 2 && moved[1] === 1, moved);
check("arrow up reorders the dom", list.children.map(c => c.id).join() === "0,2,1,3", list.children.map(c => c.id));

moved = null;
list = makeList(3);
makeSortable(list, (from, to) => { moved = [from, to]; });
key(list, list.children[0], 'ArrowUp');
check("arrow up on the first row is a no-op", moved === null && list.children.map(c => c.id).join() === "0,1,2", moved);
key(list, list.children[2], 'ArrowDown');
check("arrow down on the last row is a no-op", moved === null && list.children.map(c => c.id).join() === "0,1,2", moved);

moved = null;
list = makeList(3);
makeSortable(list, (from, to) => { moved = [from, to]; });
key(list, list.children[0], 'Enter');
list.fire('keydown', { key: 'ArrowDown', target: { closest: () => null }, preventDefault() {} });
check("an unrelated key and an off-handle key report nothing", moved === null, moved);

// --- degenerate lists ---
check("null list is inert", makeSortable(null, () => {}).dragging() === false, null);
const one = makeList(1);
const s6 = makeSortable(one, () => {});
down(one, one.children[0]);
moveTo(one, 100); up(one, 100);
check("a single row list does not throw", s6.dragging() === false, null);

console.log(fail === 0 ? "PASS" : fail + " failures");
process.exit(fail === 0 ? 0 : 1);
`

	dir := t.TempDir()
	script := filepath.Join(dir, "sortable_test.js")
	if err := os.WriteFile(script, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("sortable failed its cases:\n%s", strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("unexpected harness output:\n%s", out)
	}
}
