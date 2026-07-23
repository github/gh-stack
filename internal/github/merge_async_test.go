package github

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type recordedRequest struct {
	method string
	path   string
	body   string
}

// testAsyncClient builds a Client whose REST client is backed by a stub
// transport returning the given status and body. When rec is non-nil the
// request's method, path and body are captured for assertions.
func testAsyncClient(t *testing.T, status int, respBody string, rec *recordedRequest) *Client {
	t.Helper()
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if rec != nil {
			rec.method = r.Method
			rec.path = r.URL.Path
			if r.Body != nil {
				b, _ := io.ReadAll(r.Body)
				rec.body = string(b)
			}
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(respBody)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    r,
		}, nil
	})
	rest, err := api.NewRESTClient(api.ClientOptions{Host: "github.com", AuthToken: "x", Transport: rt})
	require.NoError(t, err)
	return &Client{rest: rest, owner: "o", repo: "r"}
}

func TestMergeStackAsync_Accepted(t *testing.T) {
	var rec recordedRequest
	body := `{"queued":true,"merged":false,"details":{"message":"Merge request enqueued.","uuid":"u-1","merge_method":"squash","expected_head_sha":"abc"}}`
	c := testAsyncClient(t, http.StatusAccepted, body, &rec)

	res, err := c.MergeStackAsync(42, "squash")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPut, rec.method)
	assert.Equal(t, "/repos/o/r/pulls/42/merge-async", rec.path)
	assert.JSONEq(t, `{"merge_method":"squash"}`, rec.body)

	assert.True(t, res.Queued)
	assert.False(t, res.Merged)
	assert.Equal(t, "u-1", res.Details.UUID)
	assert.Equal(t, "squash", res.Details.MergeMethod)
	assert.True(t, res.InProgress())
}

func TestMergeStackAsync_AlreadyMerged(t *testing.T) {
	body := `{"queued":false,"merged":true,"details":{"message":"Pull request is already merged.","sha":"deadbeef"}}`
	res, err := testAsyncClient(t, http.StatusOK, body, nil).MergeStackAsync(42, "merge")
	require.NoError(t, err)
	assert.True(t, res.Merged)
	assert.Equal(t, "deadbeef", res.Details.SHA)
}

func TestMergeStackAsync_ExistingRequestConflict(t *testing.T) {
	// The go-gh REST client discards the 409 body, so we can't recover the
	// existing UUID; the request surfaces as a clear "already exists" error.
	_, err := testAsyncClient(t, http.StatusConflict, `{"queued":true,"merged":false,"details":{"uuid":"u-2"}}`, nil).MergeStackAsync(42, "merge")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestMergeStackAsync_NotMergeable(t *testing.T) {
	// A 400 preflight failure is reported as a clear error (the specific
	// details.message isn't recoverable through the REST client).
	_, err := testAsyncClient(t, http.StatusBadRequest, `{"queued":false,"merged":false,"details":{"message":"Pull request is closed."}}`, nil).MergeStackAsync(42, "merge")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "can no longer be merged")
}

func TestMergeStackAsync_NotAvailable(t *testing.T) {
	_, err := testAsyncClient(t, http.StatusNotFound, `{"message":"Not Found"}`, nil).MergeStackAsync(42, "merge")
	assert.ErrorIs(t, err, ErrAsyncMergeUnavailable)
}

func TestMergeStackAsync_ValidationFailed(t *testing.T) {
	_, err := testAsyncClient(t, http.StatusUnprocessableEntity, `{"message":"Validation Failed"}`, nil).MergeStackAsync(42, "merge")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Validation Failed")
}

func TestGetAsyncMergeResult_States(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantQueued bool
		wantMerged bool
	}{
		{"pending", `{"queued":true,"merged":false,"details":{"message":"Merge request is in progress.","uuid":"u","merge_method":"merge","expected_head_sha":"abc"}}`, true, false},
		{"merged", `{"queued":false,"merged":true,"details":{"message":"Pull request was merged.","sha":"abc"}}`, false, true},
		{"failed", `{"queued":false,"merged":false,"details":{"message":"Merge conflict."}}`, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rec recordedRequest
			res, err := testAsyncClient(t, http.StatusOK, tt.body, &rec).GetAsyncMergeResult(42, "u")
			require.NoError(t, err)
			assert.Equal(t, http.MethodGet, rec.method)
			assert.Equal(t, "/repos/o/r/pulls/42/merge-async/u", rec.path)
			assert.Equal(t, tt.wantQueued, res.Queued)
			assert.Equal(t, tt.wantMerged, res.Merged)
		})
	}
}

func TestGetAsyncMergeResult_NotFound(t *testing.T) {
	_, err := testAsyncClient(t, http.StatusNotFound, `{"message":"Not Found"}`, nil).GetAsyncMergeResult(42, "missing")
	require.Error(t, err)
}

func TestMergeMethodFromEnum(t *testing.T) {
	assert.Equal(t, MergeMethodMerge, mergeMethodFromEnum("MERGE"))
	assert.Equal(t, MergeMethodSquash, mergeMethodFromEnum("SQUASH"))
	assert.Equal(t, MergeMethodRebase, mergeMethodFromEnum("REBASE"))
	assert.Equal(t, MergeMethodMerge, mergeMethodFromEnum("UNKNOWN"))
	assert.Equal(t, MergeMethodMerge, mergeMethodFromEnum(""))
}

func TestRepoMergeConfig_AllowedMethods(t *testing.T) {
	c := RepoMergeConfig{MergeAllowed: true, RebaseAllowed: true}
	assert.Equal(t, []string{"merge", "rebase"}, c.AllowedMethods())
	assert.True(t, c.Allows("merge"))
	assert.False(t, c.Allows("squash"))
	assert.True(t, c.Allows("rebase"))

	empty := RepoMergeConfig{}
	assert.Empty(t, empty.AllowedMethods())
}

func TestAsyncMergeResult_InProgress(t *testing.T) {
	assert.True(t, (&AsyncMergeResult{Queued: true}).InProgress())
	assert.False(t, (&AsyncMergeResult{Queued: true, Merged: true}).InProgress())
	assert.False(t, (&AsyncMergeResult{}).InProgress())
	var nilRes *AsyncMergeResult
	assert.False(t, nilRes.InProgress())
}

// sanity check that the submit body omits merge_method when empty.
func TestMergeStackAsync_OmitsEmptyMethod(t *testing.T) {
	var rec recordedRequest
	_, err := testAsyncClient(t, http.StatusAccepted, `{"queued":true,"merged":false,"details":{"message":"m","uuid":"u","merge_method":"merge","expected_head_sha":"x"}}`, &rec).MergeStackAsync(1, "")
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(rec.body), &parsed))
	_, hasMethod := parsed["merge_method"]
	assert.False(t, hasMethod, "merge_method should be omitted when empty")
}

// testPolicyClient builds a Client whose GraphQL client is backed by a stub
// transport returning the given response body.
func testPolicyClient(t *testing.T, graphqlResp string) *Client {
	t.Helper()
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(graphqlResp)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    r,
		}, nil
	})
	gql, err := api.NewGraphQLClient(api.ClientOptions{Host: "github.com", AuthToken: "x", Transport: rt})
	require.NoError(t, err)
	return &Client{gql: gql, owner: "o", repo: "r"}
}

func TestBaseBranchPolicy(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantQueue bool
	}{
		{
			name: "no merge queue",
			body: `{"data":{"repository":{"mergeQueue":null,"ref":{"rules":{"nodes":[]}}}}}`,
		},
		{
			name:      "merge queue via mergeQueue field",
			body:      `{"data":{"repository":{"mergeQueue":{"id":"MQ"},"ref":{"rules":{"nodes":[]}}}}}`,
			wantQueue: true,
		},
		{
			name:      "merge queue via ruleset type",
			body:      `{"data":{"repository":{"mergeQueue":null,"ref":{"rules":{"nodes":[{"type":"MERGE_QUEUE"}]}}}}}`,
			wantQueue: true,
		},
		{
			name: "other rules, no merge queue",
			body: `{"data":{"repository":{"mergeQueue":null,"ref":{"rules":{"nodes":[{"type":"PULL_REQUEST"}]}}}}}`,
		},
		{
			name: "null ref",
			body: `{"data":{"repository":{"mergeQueue":null,"ref":null}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, err := testPolicyClient(t, tt.body).BaseBranchPolicy("main")
			require.NoError(t, err)
			assert.Equal(t, tt.wantQueue, policy.RequiresMergeQueue, "RequiresMergeQueue")
		})
	}
}
