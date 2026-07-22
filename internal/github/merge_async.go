package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cli/go-gh/v2/pkg/auth"
	graphql "github.com/cli/shurcooL-graphql"
)

// Merge method values accepted by the async merge REST API.
const (
	MergeMethodMerge  = "merge"
	MergeMethodSquash = "squash"
	MergeMethodRebase = "rebase"
)

// ErrAsyncMergeUnavailable indicates the async merge API is not available for
// the repository (or the token lacks access). Surfaced on a 404 from the submit
// endpoint.
var ErrAsyncMergeUnavailable = errors.New("async stack merge is not available for this repository")

// RepoMergeConfig describes which merge methods a repository allows, along with
// the viewer's default (last-used) merge method.
type RepoMergeConfig struct {
	MergeAllowed  bool
	SquashAllowed bool
	RebaseAllowed bool
	// DefaultMethod is the viewer's last-used merge method, or the repository
	// default, as one of MergeMethodMerge/MergeMethodSquash/MergeMethodRebase.
	DefaultMethod string
}

// AllowedMethods returns the enabled merge methods in display order
// (merge, squash, rebase).
func (c RepoMergeConfig) AllowedMethods() []string {
	var methods []string
	if c.MergeAllowed {
		methods = append(methods, MergeMethodMerge)
	}
	if c.SquashAllowed {
		methods = append(methods, MergeMethodSquash)
	}
	if c.RebaseAllowed {
		methods = append(methods, MergeMethodRebase)
	}
	return methods
}

// Allows reports whether the given merge method is enabled for the repository.
func (c RepoMergeConfig) Allows(method string) bool {
	switch method {
	case MergeMethodMerge:
		return c.MergeAllowed
	case MergeMethodSquash:
		return c.SquashAllowed
	case MergeMethodRebase:
		return c.RebaseAllowed
	}
	return false
}

// AsyncMergeDetails is the polymorphic "details" object shared by the submit and
// poll responses. Fields are populated based on the current state: a queued
// request carries UUID/MergeMethod/ExpectedHeadSHA, an already-merged result
// carries SHA, and a failed/not-mergeable result carries only Message.
type AsyncMergeDetails struct {
	Message         string `json:"message"`
	UUID            string `json:"uuid"`
	MergeMethod     string `json:"merge_method"`
	ExpectedHeadSHA string `json:"expected_head_sha"`
	SHA             string `json:"sha"`
}

// AsyncMergeResult is the response body returned by both the submit and poll
// async merge endpoints. StatusCode carries the HTTP status of the submit
// response so callers can distinguish enqueued (202) from an existing request
// (409) and an already-merged PR (200).
type AsyncMergeResult struct {
	Queued     bool              `json:"queued"`
	Merged     bool              `json:"merged"`
	Details    AsyncMergeDetails `json:"details"`
	StatusCode int               `json:"-"`
}

// InProgress reports whether the merge is still queued (running in the
// background).
func (r *AsyncMergeResult) InProgress() bool {
	return r != nil && r.Queued && !r.Merged
}

// RepoMergeConfig fetches the repository's allowed merge methods and the
// viewer's default (last-used) merge method.
func (c *Client) RepoMergeConfig() (*RepoMergeConfig, error) {
	var query struct {
		Repository struct {
			MergeCommitAllowed       bool   `graphql:"mergeCommitAllowed"`
			SquashMergeAllowed       bool   `graphql:"squashMergeAllowed"`
			RebaseMergeAllowed       bool   `graphql:"rebaseMergeAllowed"`
			ViewerDefaultMergeMethod string `graphql:"viewerDefaultMergeMethod"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]interface{}{
		"owner": graphql.String(c.owner),
		"name":  graphql.String(c.repo),
	}

	if err := c.gql.Query("RepoMergeConfig", &query, variables); err != nil {
		return nil, fmt.Errorf("querying repository merge config: %w", err)
	}

	r := query.Repository
	return &RepoMergeConfig{
		MergeAllowed:  r.MergeCommitAllowed,
		SquashAllowed: r.SquashMergeAllowed,
		RebaseAllowed: r.RebaseMergeAllowed,
		DefaultMethod: mergeMethodFromEnum(r.ViewerDefaultMergeMethod),
	}, nil
}

// MergeStackAsync requests an asynchronous merge of the given pull request. For
// a stacked PR this merges all members of the stack up to and including
// prNumber. A blank method lets the server apply its default.
//
// The returned result is populated for the 200 (already merged), 202 (enqueued)
// 409 (a request already exists) and 400 (not mergeable) responses; the HTTP
// status is recorded on StatusCode. A 404 returns ErrAsyncMergeUnavailable.
func (c *Client) MergeStackAsync(prNumber int, method string) (*AsyncMergeResult, error) {
	type reqBody struct {
		MergeMethod string `json:"merge_method,omitempty"`
	}

	body, err := json.Marshal(reqBody{MergeMethod: method})
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	path := fmt.Sprintf("repos/%s/%s/pulls/%d/merge-async", c.owner, c.repo, prNumber)
	resp, err := c.doAsyncRequest(http.MethodPut, path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusConflict, http.StatusBadRequest:
		return decodeAsyncMergeResult(resp)
	case http.StatusNotFound:
		return nil, ErrAsyncMergeUnavailable
	default:
		return nil, asyncMergeError(resp)
	}
}

// GetAsyncMergeResult fetches the current result of a previously submitted async
// merge, identified by the UUID returned from MergeStackAsync.
func (c *Client) GetAsyncMergeResult(prNumber int, uuid string) (*AsyncMergeResult, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/merge-async/%s", c.owner, c.repo, prNumber, uuid)
	resp, err := c.doAsyncRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		return decodeAsyncMergeResult(resp)
	}
	return nil, asyncMergeError(resp)
}

// doAsyncRequest issues an authenticated request to the REST API and returns the
// raw response without treating non-2xx statuses as errors, so the caller can
// read the merge result body for 4xx responses (which carry the UUID/message).
func (c *Client) doAsyncRequest(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

func decodeAsyncMergeResult(resp *http.Response) (*AsyncMergeResult, error) {
	var r AsyncMergeResult
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decoding merge response: %w", err)
	}
	r.StatusCode = resp.StatusCode
	return &r, nil
}

// asyncMergeError builds an error from an unexpected (403/422/5xx) response,
// extracting the API message when present.
func asyncMergeError(resp *http.Response) error {
	b, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(b, &parsed)
	if parsed.Message != "" {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, parsed.Message)
	}
	if trimmed := strings.TrimSpace(string(b)); trimmed != "" {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, trimmed)
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}

// mergeMethodFromEnum maps a GraphQL PullRequestMergeMethod enum value
// (MERGE/SQUASH/REBASE) to the lowercase REST API value. Unknown values fall
// back to MergeMethodMerge.
func mergeMethodFromEnum(enum string) string {
	switch strings.ToUpper(enum) {
	case "SQUASH":
		return MergeMethodSquash
	case "REBASE":
		return MergeMethodRebase
	default:
		return MergeMethodMerge
	}
}

// restBaseURL derives the REST API base URL for a host, mirroring go-gh's
// internal restPrefix so raw requests target the same endpoint as the REST
// client.
func restBaseURL(host string) string {
	if host == "" {
		host = "github.com"
	}
	normalized := auth.NormalizeHostname(host)
	if auth.IsEnterprise(normalized) {
		return fmt.Sprintf("https://%s/api/v3/", normalized)
	}
	if strings.EqualFold(normalized, "github.localhost") {
		return fmt.Sprintf("http://api.%s/", normalized)
	}
	return fmt.Sprintf("https://api.%s/", normalized)
}
