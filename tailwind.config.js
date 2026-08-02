// Scans the templates and the frontend scripts for class names. Classes that
// only exist as runtime string concatenation will not be found, so write them
// out in full. See "Regenerating the CSS" in CLAUDE.md
module.exports = {
  content: [
    './internal/httpx/templates/*.html',
    './internal/httpx/static/*.js',
  ],
  theme: { extend: {} },
  plugins: [],
}
