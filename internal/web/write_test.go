package web

import (
	"html/template"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

// A fragment whose template fails half-way answers 500 with no part of
// the fragment, as a whole page does: never a 200 and a torn block.
func TestTemplateFailingHalfWayWritesNothingOfIt(t *testing.T) {
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil)), pages: map[string]*template.Template{
		"p": template.Must(template.New("p").Parse(`{{define "frag"}}<div>partial{{index . 5}}</div>{{end}}`)),
	}}
	rec := httptest.NewRecorder()
	h.writeTemplate(rec, 200, "p", "frag", []int{})
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "partial") {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.writeTemplate(rec, 409, "p", "frag", []int{0, 1, 2, 3, 4, 5})
	if rec.Code != 409 || rec.Body.String() != "<div>partial5</div>" || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("%d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
}
