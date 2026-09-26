// Package github is a minimal client for the GitHub REST API endpoints the
// gate reads: the reviews of a pull request and a collaborator's access.
//
// Both work with the GITHUB_TOKEN of a workflow with contents: read and
// pull-requests: read: listing reviews needs the "Pull requests" repository
// permission (read), and the collaborator permission endpoint needs
// "Metadata" (read), which GitHub grants along with any other repository
// permission.
package github

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/svallejo-dev/aval/internal/approval"
)

// DefaultBaseURL is the API root used when Client.BaseURL is empty. In
// GitHub Actions, $GITHUB_API_URL holds the right one, also for GHES.
const DefaultBaseURL = "https://api.github.com"

const (
	apiVersion   = "2022-11-28"
	perPage      = 100
	maxPages     = 30 // 3,000 reviews; more is an error, never a silent truncation
	maxBodyBytes = 16 << 20
	maxRedirects = 10 // net/http's default
)

var defaultHTTP = &http.Client{Timeout: 30 * time.Second}

// Client calls the GitHub REST API. The zero value calls api.github.com
// without a token.
type Client struct {
	BaseURL string       // API root; empty means DefaultBaseURL
	Token   string       // sent as a bearer token; never logged or put in an error
	HTTP    *http.Client // nil means a client with a 30 s timeout
}

// Error is a GitHub response with a non-2xx status. Match it with
// errors.As to read the status code.
type Error struct {
	StatusCode int
	Method     string
	URL        string
	Message    string // GitHub's own message, when the body has one
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("github: %s %s: %d %s", e.Method, e.URL, e.StatusCode, http.StatusText(e.StatusCode))
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// ListReviews returns every review of pull request number, oldest first,
// following the Link header across pages. PENDING reviews are included; the
// caller (approval.Evaluate) ignores them.
func (c *Client) ListReviews(ctx context.Context, owner, repo string, number int) ([]approval.Review, error) {
	next := c.endpoint(fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=%d",
		url.PathEscape(owner), url.PathEscape(repo), number, perPage))
	var reviews []approval.Review
	for page := 0; next != ""; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("github: %s/%s#%d has more than %d pages of reviews", owner, repo, number, maxPages)
		}
		var batch []struct {
			ID   int64 `json:"id"`
			User *struct {
				Login string `json:"login"`
			} `json:"user"`
			State       string    `json:"state"`
			CommitID    string    `json:"commit_id"`
			SubmittedAt time.Time `json:"submitted_at"`
			Body        string    `json:"body"`
		}
		var err error
		if next, err = c.get(ctx, next, &batch); err != nil {
			return nil, err
		}
		for _, r := range batch {
			rv := approval.Review{ID: r.ID, State: approval.State(r.State), CommitID: r.CommitID, SubmittedAt: r.SubmittedAt, Body: r.Body}
			if r.User != nil {
				rv.User = r.User.Login
			}
			reviews = append(reviews, rv)
		}
	}
	return reviews, nil
}

// Permission returns user's access to the repository: role_name (admin,
// maintain, write, triage, read or a custom role) and the legacy permission
// (admin, write, read or none, where maintain reports as write). A 404 means
// no access: it returns the zero Access and no error.
func (c *Client) Permission(ctx context.Context, owner, repo, user string) (approval.Access, error) {
	var body struct {
		RoleName   string `json:"role_name"`
		Permission string `json:"permission"`
	}
	_, err := c.get(ctx, c.endpoint(fmt.Sprintf("/repos/%s/%s/collaborators/%s/permission",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(user))), &body)
	if apiErr, ok := errors.AsType[*Error](err); ok && apiErr.StatusCode == http.StatusNotFound {
		return approval.Access{}, nil
	}
	if err != nil {
		return approval.Access{}, err
	}
	return approval.Access{Role: body.RoleName, Permission: body.Permission}, nil
}

// DefaultBranch returns the repository's default branch, without refs/heads/.
// It is where the gate takes its trust base from when the event payload does
// not name it (ADR-0005 §1). An empty answer means GitHub reported no branch,
// which is not an error: the caller falls back to the remote's own refs.
func (c *Client) DefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	var body struct {
		DefaultBranch string `json:"default_branch"`
	}
	_, err := c.get(ctx, c.endpoint(fmt.Sprintf("/repos/%s/%s",
		url.PathEscape(owner), url.PathEscape(repo))), &body)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(body.DefaultBranch, "refs/heads/"), nil
}

func (c *Client) endpoint(path string) string {
	return strings.TrimSuffix(cmp.Or(c.BaseURL, DefaultBaseURL), "/") + path
}

// httpClient returns a copy of the configured client that refuses redirects
// to another origin, so the token never leaves the API host.
func (c *Client) httpClient() *http.Client {
	hc := *cmp.Or(c.HTTP, defaultHTTP)
	checkNext := hc.CheckRedirect
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if first := via[0].URL; req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
			return fmt.Errorf("github: refusing a redirect from %s://%s to %s://%s", first.Scheme, first.Host, req.URL.Scheme, req.URL.Host)
		}
		if checkNext != nil {
			return checkNext(req, via)
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("github: stopped after %d redirects", maxRedirects)
		}
		return nil
	}
	return &hc
}

// get fetches rawURL into v and returns the URL of the next page, if any.
func (c *Client) get(ctx context.Context, rawURL string, v any) (next string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("github: GET %s: %w", req.URL.Redacted(), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("github: read GET %s: %w", req.URL.Redacted(), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &msg) // best effort: the status code is the error
		return "", &Error{StatusCode: resp.StatusCode, Method: http.MethodGet, URL: req.URL.Redacted(), Message: msg.Message}
	}
	if len(data) > maxBodyBytes {
		return "", fmt.Errorf("github: GET %s: response larger than %d bytes", req.URL.Redacted(), maxBodyBytes)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return "", fmt.Errorf("github: decode GET %s: %w", req.URL.Redacted(), err)
	}
	return nextPage(req.URL, resp.Header.Values("Link"))
}

// nextPage returns the rel="next" target of a Link header, resolved against
// the current page. It refuses a target on another origin: the token must
// never leave the API host.
func nextPage(current *url.URL, links []string) (string, error) {
	for _, h := range links {
		for link := range strings.SplitSeq(h, ",") {
			target, params, ok := strings.Cut(link, ";")
			target = strings.TrimSpace(target)
			if !ok || !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") || !isNext(params) {
				continue
			}
			u, err := current.Parse(target[1 : len(target)-1])
			if err != nil {
				return "", fmt.Errorf("github: next page link: %w", err)
			}
			if u.Scheme != current.Scheme || u.Host != current.Host {
				return "", fmt.Errorf("github: next page link %s is not on %s://%s", u.Redacted(), current.Scheme, current.Host)
			}
			return u.String(), nil
		}
	}
	return "", nil
}

// isNext reports whether Link parameters include rel="next".
func isNext(params string) bool {
	for p := range strings.SplitSeq(params, ";") {
		k, v, _ := strings.Cut(p, "=")
		if strings.EqualFold(strings.TrimSpace(k), "rel") &&
			slices.Contains(strings.Fields(strings.Trim(strings.TrimSpace(v), `"`)), "next") {
			return true
		}
	}
	return false
}
