// Package page renders the app's pages and serves its embedded files.
//
// Every page is html/template, parsed once at start: a template that does not
// parse stops the program from starting, rather than showing a 500 to someone
// in the middle of writing a check. Each app hands over its own templates,
// which are parsed together with the shared base (templates/base.html), so
// that every screen has the same chrome (docs/12-look-and-voice.md).
//
// The embedded files (the stylesheet, the script, the icons) are served under
// a hash of their contents, so a browser may keep them for ever and still
// never runs an old script after the program updates itself.
package page

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/jroedel/reconcile-checks/app/sdk/mid"
)

//go:embed templates/base.html
var baseFS embed.FS

//go:embed assets
var assetsFS embed.FS

// Icon is the program's icon, 256 pixels square, as a PNG: for the Linux
// menu entry an install writes (foundation/shortcut).
func Icon() []byte {
	b, _ := assetsFS.ReadFile("assets/img/icon-256.png")
	return b
}

// StaticPrefix is where the embedded files are served.
const StaticPrefix = "/static/"

// View is what every template is given. A page's own data is .Data.
type View struct {
	Session   bool   // the session cookie was presented; shows the menu and the script
	FormToken string // for every form that writes
	Version   string

	Stylesheet, Script, IconSVG, Icon32 string

	Data any
}

// Renderer holds the parsed pages and the embedded files.
type Renderer struct {
	log      *slog.Logger
	sessions *mid.Sessions
	version  string

	pages  map[string]*template.Template
	assets map[string]asset // by name, e.g. "app.css", "img/icon-32.png"
}

type asset struct {
	body []byte
	hash string
	kind string
}

// New loads the embedded files and the shared base.
func New(log *slog.Logger, sessions *mid.Sessions, version string) (*Renderer, error) {
	r := &Renderer{
		log:      log,
		sessions: sessions,
		version:  version,
		pages:    make(map[string]*template.Template),
		assets:   make(map[string]asset),
	}

	err := fs.WalkDir(assetsFS, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		// A script's tests live beside it and are never served.
		if strings.HasSuffix(p, "_test.mjs") {
			return nil
		}

		body, err := assetsFS.ReadFile(p)
		if err != nil {
			return err
		}

		sum := sha256.Sum256(body)
		r.assets[strings.TrimPrefix(p, "assets/")] = asset{
			body: body,
			hash: hex.EncodeToString(sum[:5]),
			kind: contentType(p),
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("page: reading the embedded files: %w", err)
	}

	for _, need := range []string{"app.css", "app.mjs", "img/icon.svg", "img/icon-32.png"} {
		if _, ok := r.assets[need]; !ok {
			return nil, fmt.Errorf("page: the embedded file %s is missing", need)
		}
	}

	return r, nil
}

// Add parses one page: file, in fsys, defining "content" (and, if it likes,
// "title"), together with the base. name is what Render is called with.
func (r *Renderer) Add(name string, fsys fs.FS, file string) error {
	if _, dup := r.pages[name]; dup {
		return fmt.Errorf("page: two pages are both called %q", name)
	}

	// asset is a page's way to name an embedded file of its own, a script
	// usually, at its hashed address.
	t, err := template.New(name).Funcs(funcs).Funcs(template.FuncMap{"asset": r.URL}).ParseFS(baseFS, "templates/base.html")
	if err != nil {
		return fmt.Errorf("page: parsing the base: %w", err)
	}

	if _, err := t.ParseFS(fsys, file); err != nil {
		return fmt.Errorf("page: parsing %s: %w", file, err)
	}

	r.pages[name] = t

	return nil
}

// funcs is what every page may call beyond html/template's own: kept to what
// a page can't say without it.
var funcs = template.FuncMap{
	// join lists values as a sentence lists them: "tray-1, tray-2".
	"join": func(ss []string) string { return strings.Join(ss, ", ") },
}

// Pages lists the pages added, for a test that every one renders.
func (r *Renderer) Pages() []string { return slices.Sorted(maps.Keys(r.pages)) }

// URL is the address an embedded file is served at, with its hash.
func (r *Renderer) URL(name string) string {
	a, ok := r.assets[name]
	if !ok {
		return ""
	}

	return StaticPrefix + a.hash + "/" + name
}

// Render writes a page. It renders into a buffer first, so a template that
// fails half way never sends half a page under a 200.
func (r *Renderer) Render(w http.ResponseWriter, req *http.Request, status int, name string, data any) {
	t, ok := r.pages[name]
	if !ok {
		r.log.ErrorContext(req.Context(), "no such page", "page", name)
		http.Error(w, "Something went wrong in Reconcile Checks.", http.StatusInternalServerError)

		return
	}

	view := View{
		Session:    mid.HasSession(req.Context()),
		FormToken:  r.sessions.FormToken(),
		Version:    r.version,
		Stylesheet: r.URL("app.css"),
		Script:     r.URL("app.mjs"),
		IconSVG:    r.URL("img/icon.svg"),
		Icon32:     r.URL("img/icon-32.png"),
		Data:       data,
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base", view); err != nil {
		r.log.ErrorContext(req.Context(), "rendering a page", "page", name, "err", err)
		http.Error(w, "Something went wrong in Reconcile Checks.", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// contentType names the type of each kind of embedded file.
//
// Stated here rather than asked of mime.TypeByExtension, which on Windows
// reads the registry, where another program may have registered .js or .mjs
// as text/plain -- and a browser refuses to run a module script served as
// anything but JavaScript, so the page would quietly lose its script on
// exactly one of the two computers.
func contentType(name string) string {
	switch path.Ext(name) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".mjs", ".js":
		return "text/javascript; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	default:
		return "application/octet-stream"
	}
}

// Static serves the embedded files at /static/{hash}/{name}.
//
// A file asked for under its current hash may be kept for ever. One asked for
// under another hash -- a page rendered by the version before an update -- is
// still served, the current content, but not to be kept, so the next page
// load asks again under the new hash.
func (r *Renderer) Static(w http.ResponseWriter, req *http.Request) {
	hash := req.PathValue("hash")
	name := req.PathValue("name")

	a, ok := r.assets[name]
	if !ok {
		http.NotFound(w, req)
		return
	}

	h := w.Header()
	if hash == a.hash {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	h.Set("Content-Type", a.kind)

	w.Write(a.body)
}
