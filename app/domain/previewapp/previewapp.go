// Package previewapp draws the check as it will print, for the live preview
// (docs/03-the-local-app.md, "The screens"): GET /preview.png with the check's
// fields in the query, answered with a picture, or with the sentence saying
// what doesn't fit.
//
// The picture is drawn by the same code as the paper and the image sent to
// Reconcile (business/domain/layout/layoutbus), so a payee that runs into the
// amount is seen here before it is printed.
package previewapp

import (
	"bytes"
	"errors"
	"image/png"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/reconcile-checks/business/domain/layout/layoutbus"
	"github.com/jroedel/reconcile-checks/business/types/money"
	"github.com/jroedel/reconcile-checks/foundation/render"
)

// Resolutions: the face sharp enough to read a memo, the whole page small
// enough to send at every pause in typing.
const (
	faceDPI  = 150
	wholeDPI = 90
)

// Config is what the app needs.
type Config struct {
	// Stock is the layout to draw on. One for now; per account once there are
	// accounts (docs/02-architecture.md, "The config, sketched").
	Stock string
}

// App holds the handlers.
type App struct {
	layout layoutbus.Layout
}

// New loads the layout.
func New(cfg Config) (*App, error) {
	l, err := layoutbus.Load(cfg.Stock)
	if err != nil {
		return nil, err
	}

	return &App{layout: l}, nil
}

// Preview answers GET /preview.png.
//
//	date      YYYY-MM-DD, default today
//	payee, amount, memo, note
//	number    the check number, drawn where the stock prints it
//	whole     1 for the whole page with both stubs; otherwise the check alone
func (a *App) Preview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	c := layoutbus.Check{
		Date:     time.Now(),
		Payee:    q.Get("payee"),
		Memo:     q.Get("memo"),
		StubNote: strings.ReplaceAll(q.Get("note"), "\r\n", "\n"),
	}

	if d := q.Get("date"); d != "" {
		t, err := time.Parse(time.DateOnly, d)
		if err != nil {
			refuse(w, "The date should look like 2026-11-20.")
			return
		}
		c.Date = t
	}

	if s := q.Get("amount"); s != "" {
		amount, err := money.Parse(s)
		if err != nil {
			refuse(w, err.Error())
			return
		}
		c.Amount = amount
	}

	if n, err := strconv.Atoi(q.Get("number")); err == nil && n > 0 {
		c.Number = n
	}

	var (
		doc  render.Doc
		area render.Area
		dpi  float64
		err  error
	)

	if q.Get("whole") == "1" {
		doc, err = a.layout.Preview(c)
		area, dpi = doc.Whole(), wholeDPI
	} else {
		doc, area, err = a.layout.Image(c)
		dpi = faceDPI
	}

	if err != nil {
		if _, ok := errors.AsType[layoutbus.TooLong](err); ok {
			refuse(w, err.Error())
			return
		}
		if _, ok := errors.AsType[layoutbus.BadCharacter](err); ok {
			refuse(w, err.Error())
			return
		}

		http.Error(w, "The preview could not be drawn.", http.StatusInternalServerError)

		return
	}

	img, err := render.PNG(doc, dpi, area)
	if err != nil {
		http.Error(w, "The preview could not be drawn.", http.StatusInternalServerError)
		return
	}

	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		http.Error(w, "The preview could not be drawn.", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Write(b.Bytes())
}

// refuse answers 422 with a sentence the page shows beside the field.
func refuse(w http.ResponseWriter, sentence string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	w.Write([]byte(sentence))
}
