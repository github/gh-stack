package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
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
// async merge endpoints. The Queued/Merged flags distinguish an enqueued merge
// (202) from an already-merged pull request (200).
type AsyncMergeResult struct {
	Queued  bool              `json:"queued"`
	Merged  bool              `json:"merged"`
	Details AsyncMergeDetails `json:"details"`
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
// On success the returned result is populated for the 200 (already merged) and
// 202 (enqueued) responses. A 404 returns ErrAsyncMergeUnavailable, a 409
// (a request already exists) returns a clear "already exists" error, and any
// other non-2xx status is returned as-is.
func (c *Client) MergeStackAsync(prNumber int, method string) (*AsyncMergeResult, error) {
	type reqBody struct {
		MergeMethod string `json:"merge_method,omitempty"`
	}

	body, err := json.Marshal(reqBody{MergeMethod: method})
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	path := fmt.Sprintf("repos/%s/%s/pulls/%d/merge-async", c.owner, c.repo, prNumber)
	var result AsyncMergeResult
	if err := c.rest.Put(path, bytes.NewReader(body), &result); err != nil {
		return nil, classifyAsyncMergeError(err)
	}
	return &result, nil
}

// GetAsyncMergeResult fetches the current result of a previously submitted async
// merge, identified by the UUID returned from MergeStackAsync. A valid lookup
// always returns 200, so the wrapped Queued/Merged/Details state reflects the
// merge's progress (queued, merged, or failed).
func (c *Client) GetAsyncMergeResult(prNumber int, uuid string) (*AsyncMergeResult, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/merge-async/%s", c.owner, c.repo, prNumber, uuid)
	var result AsyncMergeResult
	if err := c.rest.Get(path, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// PRTitles fetches the titles for a set of pull request numbers in a single
// GraphQL query. Missing PRs are simply absent from the result. Best-effort:
// callers may ignore the error and proceed without titles.
func (c *Client) PRTitles(numbers []int) (map[int]string, error) {
	titles := make(map[int]string, len(numbers))
	if len(numbers) == 0 {
		return titles, nil
	}

	// Batch to keep individual queries small for very large stacks.
	const batchSize = 50
	for start := 0; start < len(numbers); start += batchSize {
		end := start + batchSize
		if end > len(numbers) {
			end = len(numbers)
		}

		var q strings.Builder
		q.WriteString("query($owner:String!,$name:String!){repository(owner:$owner,name:$name){")
		for i, n := range numbers[start:end] {
			fmt.Fprintf(&q, "pr%d:pullRequest(number:%d){number title} ", i, n)
		}
		q.WriteString("}}")

		var resp struct {
			Repository map[string]struct {
				Number int    `json:"number"`
				Title  string `json:"title"`
			} `json:"repository"`
		}
		vars := map[string]interface{}{"owner": c.owner, "name": c.repo}
		if err := c.gql.Do(q.String(), vars, &resp); err != nil {
			return titles, fmt.Errorf("querying pull request titles: %w", err)
		}
		for _, pr := range resp.Repository {
			if pr.Number != 0 {
				titles[pr.Number] = pr.Title
			}
		}
	}
	return titles, nil
}

// classifyAsyncMergeError maps a go-gh REST error into a domain error. A 404
// means async merge isn't available for the repository or token; a 409 means a
// merge request already exists for this stack. Other errors pass through.
//
// Note: the go-gh REST client discards non-2xx response bodies, so the specific
// "details.message" from a 400 (not mergeable) and the existing UUID from a 409
// aren't recovered here. Those are rare — the in-range PRs are validated open,
// non-draft and non-merged before submitting, and real merge failures (e.g.
// conflicts) surface through the 200 poll body — so status-based handling is
// sufficient.
func classifyAsyncMergeError(err error) error {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusNotFound:
			return ErrAsyncMergeUnavailable
		case http.StatusConflict:
			return errors.New("a merge request already exists for this stack")
		case http.StatusBadRequest:
			return errors.New("the stack can no longer be merged as requested; refresh and try again")
		}
	}
	return err
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
