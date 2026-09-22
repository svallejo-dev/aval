package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/svallejo-dev/aval/internal/approval"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const (
	canary  = "canary-7f3a" // the bearer token; no error may contain it
	headSHA = "1111111111111111111111111111111111111111"
)

// newClient serves h under a GHES-style API root with a trailing slash.
func newClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, want := range map[string]string{
			"Accept":               "application/vnd.github+json",
			"X-GitHub-Api-Version": "2022-11-28",
			"Authorization":        "Bearer " + canary,
		} {
			if got := r.Header.Get(k); got != want {
				t.Errorf("header %s = %q, want %q", k, got, want)
			}
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v3/repos/") {
			t.Errorf("path %s is outside the API root", r.URL.Path)
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL + "/api/v3/", Token: canary, HTTP: srv.Client()}
}

func TestListReviewsPaginates(t *testing.T) {
	t.Parallel()

	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/o/r/pulls/7/reviews" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected request %s", r.URL)
		}
		switch r.URL.Query().Get("page") {
		case "":
			next := "http://" + r.Host + "/api/v3/repos/o/r/pulls/7/reviews?per_page=100&page=2"
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next", <%s>; rel="last"`, next, next))
			_, _ = io.WriteString(w, `[{"id":1,"user":{"login":"lead"},"state":"APPROVED","commit_id":"`+headSHA+`",
				"submitted_at":"2026-09-22T10:00:00Z","body":"aval:override hotfix","author_association":"MEMBER"}]`)
		case "2":
			w.Header().Set("Link", `</api/v3/repos/o/r/pulls/7/reviews?per_page=100&page=1>; rel="prev"`)
			_, _ = io.WriteString(w, `[{"id":2,"user":null,"state":"COMMENTED","commit_id":null,"submitted_at":"2026-09-22T10:05:00Z","body":""},
				{"id":3,"user":{"login":"maint"},"state":"PENDING","commit_id":"`+headSHA+`","body":"draft"}]`)
		default:
			t.Errorf("unexpected page %s", r.URL)
		}
	})

	got, err := c.ListReviews(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatalf("ListReviews: %v", err)
	}
	at := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	want := []approval.Review{
		{ID: 1, User: "lead", State: approval.Approved, CommitID: headSHA, SubmittedAt: at, Body: "aval:override hotfix"},
		{ID: 2, State: approval.Commented, SubmittedAt: at.Add(5 * time.Minute)},
		{ID: 3, User: "maint", State: approval.Pending, CommitID: headSHA, Body: "draft"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListReviews =\n%+v\nwant\n%+v", got, want)
	}
}

func TestListReviewsPageCap(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Link", `</api/v3/repos/o/r/pulls/7/reviews?page=`+strconv.Itoa(int(n)+1)+`>; rel="next"`)
		_, _ = io.WriteString(w, `[]`)
	})

	_, err := c.ListReviews(context.Background(), "o", "r", 7)
	if err == nil || !strings.Contains(err.Error(), "more than 30 pages") {
		t.Errorf("ListReviews = %v, want a page cap error", err)
	}
	if n := calls.Load(); n != maxPages {
		t.Errorf("made %d requests, want %d", n, maxPages)
	}
}

// foreignServer fails the test on any request: the token must never reach
// another origin.
func foreignServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("request reached another origin: %s", r.URL)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListReviewsRefusesNextLinksToAnotherOrigin(t *testing.T) {
	t.Parallel()

	foreign := foreignServer(t)
	for name, link := range map[string]func(host string) string{
		"another host":   func(string) string { return foreign.URL + "/api/v3/repos/o/r/pulls/7/reviews?page=2" },
		"another scheme": func(host string) string { return "https://" + host + "/api/v3/repos/o/r/pulls/7/reviews?page=2" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Link", "<"+link(r.Host)+`>; rel="next"`)
				_, _ = io.WriteString(w, `[]`)
			})
			_, err := c.ListReviews(context.Background(), "o", "r", 7)
			if err == nil || !strings.Contains(err.Error(), "next page link") {
				t.Errorf("ListReviews = %v, want a refused next link", err)
			}
		})
	}
}

func TestRedirects(t *testing.T) {
	t.Parallel()

	foreign := foreignServer(t)
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/repos/o/r/pulls/7/reviews": // a renamed repository
			http.Redirect(w, r, "/api/v3/repos/renamed/r/pulls/7/reviews", http.StatusMovedPermanently)
		case "/api/v3/repos/renamed/r/pulls/7/reviews":
			_, _ = io.WriteString(w, `[{"id":1,"state":"APPROVED"}]`)
		default:
			http.Redirect(w, r, foreign.URL+"/api/v3/repos/o/moved/pulls/7/reviews", http.StatusFound)
		}
	})

	if got, err := c.ListReviews(context.Background(), "o", "r", 7); err != nil || len(got) != 1 {
		t.Errorf("ListReviews through a same-origin redirect = %v, %v", got, err)
	}
	if _, err := c.ListReviews(context.Background(), "o", "moved", 7); err == nil || !strings.Contains(err.Error(), "refusing a redirect") {
		t.Errorf("ListReviews through a redirect to another origin = %v, want a refusal", err)
	}
}

func TestResponseSizeLimit(t *testing.T) {
	t.Parallel()

	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "[]"+strings.Repeat(" ", maxBodyBytes))
	})
	if _, err := c.ListReviews(context.Background(), "o", "r", 7); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("ListReviews of a %d-byte body = %v, want a size error", maxBodyBytes+2, err)
	}
}

func TestPermission(t *testing.T) {
	t.Parallel()

	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/o/r/collaborators/maint/permission" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Not Found"}`)
			return
		}
		_, _ = io.WriteString(w, `{"permission":"write","role_name":"maintain","user":{"login":"maint"}}`)
	})

	want := approval.Access{Role: approval.RoleMaintain, Permission: "write"}
	if got, err := c.Permission(context.Background(), "o", "r", "maint"); err != nil || got != want {
		t.Errorf("Permission(maint) = %+v, %v; want %+v", got, err, want)
	}
	if got, err := c.Permission(context.Background(), "o", "r", "stranger"); err != nil || got != (approval.Access{}) {
		t.Errorf("Permission(stranger) = %+v, %v; want no access and no error on 404", got, err)
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
		want   int // status code carried by *Error; 0 means a non-API error
	}{
		{"unauthorized", http.StatusUnauthorized, `{"message":"Bad credentials"}`, http.StatusUnauthorized},
		{"forbidden", http.StatusForbidden, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden},
		{"server error without JSON", http.StatusBadGateway, `<html>bad gateway</html>`, http.StatusBadGateway},
		{"malformed body", http.StatusOK, `[{"id": "one"`, 0},
		{"wrong shape", http.StatusOK, `{"id": 1}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			_, listErr := c.ListReviews(context.Background(), "o", "r", 7)
			calls := map[string]error{"ListReviews": listErr}
			if tt.name != "wrong shape" { // an object is a valid permission body
				_, calls["Permission"] = c.Permission(context.Background(), "o", "r", "lead")
			}
			for call, err := range calls {
				if err == nil {
					t.Errorf("%s: no error", call)
					continue
				}
				if strings.Contains(err.Error(), canary) {
					t.Errorf("%s: error leaks the token: %v", call, err)
				}
				apiErr, ok := errors.AsType[*Error](err)
				switch {
				case tt.want == 0 && ok:
					t.Errorf("%s: %v is an API error, want a decode error", call, err)
				case tt.want != 0 && (!ok || apiErr.StatusCode != tt.want):
					t.Errorf("%s: %v, want an API error with status %d", call, err, tt.want)
				}
			}
		})
	}
}
