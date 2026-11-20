package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jroedel/reconcile-checks/foundation/web"
)

func port() string { return "7461" }

var ok = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

func TestRequireLocalHost(t *testing.T) {
	h := web.RequireLocalHost(port)(ok)

	for host, want := range map[string]int{
		"127.0.0.1:7461":    http.StatusOK,
		"localhost:7461":    http.StatusOK,
		"[::1]:7461":        http.StatusOK,
		"LOCALHOST:7461":    http.StatusOK,
		"127.0.0.1:7462":    http.StatusMisdirectedRequest, // another port
		"127.0.0.1":         http.StatusMisdirectedRequest, // no port: never this server
		"evil.example:7461": http.StatusMisdirectedRequest, // DNS rebinding
		"":                  http.StatusMisdirectedRequest,
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		w := httptest.NewRecorder()

		h.ServeHTTP(w, r)

		if w.Code != want {
			t.Errorf("Host %q: got %d, want %d", host, w.Code, want)
		}
	}
}

func TestSameOriginOnly(t *testing.T) {
	h := web.SameOriginOnly(port)(ok)

	cases := []struct {
		name   string
		method string
		site   string
		origin string
		want   int
	}{
		{"a read is never checked", http.MethodGet, "cross-site", "https://evil.example", http.StatusOK},
		{"our own page", http.MethodPost, "same-origin", "http://127.0.0.1:7461", http.StatusOK},
		{"typed or bookmarked", http.MethodPost, "none", "", http.StatusOK},
		{"a program on this computer", http.MethodPost, "", "", http.StatusOK},
		{"another website", http.MethodPost, "cross-site", "https://evil.example", http.StatusForbidden},
		{"a sibling site", http.MethodPost, "same-site", "http://127.0.0.1:7461", http.StatusForbidden},
		{"an origin that is not us", http.MethodPost, "", "http://evil.example:7461", http.StatusForbidden},
		{"us on another port", http.MethodPost, "", "http://127.0.0.1:9999", http.StatusForbidden},
		{"us over https, which we never are", http.MethodPost, "", "https://127.0.0.1:7461", http.StatusForbidden},
		{"null, vouched for", http.MethodPost, "same-origin", "null", http.StatusOK},
		{"null, not vouched for", http.MethodPost, "", "null", http.StatusForbidden},
	}

	for _, c := range cases {
		r := httptest.NewRequest(c.method, "/", nil)
		if c.site != "" {
			r.Header.Set("Sec-Fetch-Site", c.site)
		}
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()

		h.ServeHTTP(w, r)

		if w.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, w.Code, c.want)
		}
	}
}

func TestWrapAppliesTheFirstMiddlewareOutermost(t *testing.T) {
	var order []string
	mark := func(name string) web.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	web.Wrap(ok, mark("outer"), mark("inner")).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if len(order) != 2 || order[0] != "outer" || order[1] != "inner" {
		t.Errorf("order = %v", order)
	}
}
