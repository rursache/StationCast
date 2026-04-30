package httpx

import (
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
)

//go:embed all:templates
var templatesFS embed.FS

//go:embed all:static
var staticFS embed.FS

var staticFiles = map[string][]byte{}

func init() {
	_ = fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := staticFS.ReadFile(path)
		if err != nil {
			return nil
		}
		key := path[len("static/"):]
		staticFiles[key] = b
		return nil
	})
}

type Templates struct {
	tmpl *template.Template
}

func MustLoadTemplates() *Templates {
	t, err := template.New("").Funcs(template.FuncMap{
		"displayTitle": func(title, artist string) string {
			if title == "" {
				return ""
			}
			if artist == "" {
				return title
			}
			return artist + " - " + title
		},
		"add": func(a, b int) int { return a + b },
	}).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		panic(err)
	}
	return &Templates{tmpl: t}
}

func (t *Templates) Render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.tmpl.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("template", "name", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
