package github

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testAsyncClient builds a Client wired to a test server for the async merge
// REST methods, which only use the raw HTTP client and base URL.
func testAsyncClient(base string) *Client {
	return &Client{http: http.DefaultClient, base: base + "/", owner: "o", repo: "r", slug: "o/r"}
}

type recordedRequest struct {
	method string
	path   string
	body   string
}

func serveOnce(t *testing.T, status int, respBody string, rec *recordedRequest) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rec != nil {
			b, _ := io.ReadAll(r.Body)
			rec.method = r.Method
			rec.path = r.URL.Path
			rec.body = string(b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMergeStackAsync_Accepted(t *testing.T) {
	var rec recordedRequest
	body := `{"queued":true,"merged":false,"details":{"message":"Merge request enqueued.","uuid":"u-1","merge_method":"squash","expected_head_sha":"abc"}}`
	srv := serveOnce(t, http.StatusAccepted, body, &rec)

	c := testAsyncClient(srv.URL)
	res, err := c.MergeStackAsync(42, "squash")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPut, rec.method)
	assert.Equal(t, "/repos/o/r/pulls/42/merge-async", rec.path)
	assert.JSONEq(t, `{"merge_method":"squash"}`, rec.body)

	assert.True(t, res.Queued)
	assert.False(t, res.Merged)
	assert.Equal(t, http.StatusAccepted, res.StatusCode)
	assert.Equal(t, "u-1", res.Details.UUID)
	assert.Equal(t, "squash", res.Details.MergeMethod)
	assert.True(t, res.InProgress())
}

func TestMergeStackAsync_AlreadyMerged(t *testing.T) {
	body := `{"queued":false,"merged":true,"details":{"message":"Pull request is already merged.","sha":"deadbeef"}}`
	srv := serveOnce(t, http.StatusOK, body, nil)

	res, err := testAsyncClient(srv.URL).MergeStackAsync(42, "merge")
	require.NoError(t, err)
	assert.True(t, res.Merged)
	assert.Equal(t, "deadbeef", res.Details.SHA)
	assert.Equal(t, http.StatusOK, res.StatusCode)
}

func TestMergeStackAsync_ExistingRequestConflict(t *testing.T) {
	body := `{"queued":true,"merged":false,"details":{"message":"A merge request already exists for this pull request.","uuid":"u-2","merge_method":"merge","expected_head_sha":"abc"}}`
	srv := serveOnce(t, http.StatusConflict, body, nil)

	res, err := testAsyncClient(srv.URL).MergeStackAsync(42, "merge")
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, res.StatusCode)
	assert.Equal(t, "u-2", res.Details.UUID)
	assert.True(t, res.InProgress())
}

func TestMergeStackAsync_NotMergeable(t *testing.T) {
	body := `{"queued":false,"merged":false,"details":{"message":"Pull request is closed."}}`
	srv := serveOnce(t, http.StatusBadRequest, body, nil)

	res, err := testAsyncClient(srv.URL).MergeStackAsync(42, "merge")
	require.NoError(t, err)
	assert.False(t, res.Queued)
	assert.False(t, res.Merged)
	assert.Equal(t, "Pull request is closed.", res.Details.Message)
}

func TestMergeStackAsync_NotAvailable(t *testing.T) {
	srv := serveOnce(t, http.StatusNotFound, `{"message":"Not Found"}`, nil)
	_, err := testAsyncClient(srv.URL).MergeStackAsync(42, "merge")
	assert.ErrorIs(t, err, ErrAsyncMergeUnavailable)
}

func TestMergeStackAsync_ValidationFailed(t *testing.T) {
	srv := serveOnce(t, http.StatusUnprocessableEntity, `{"message":"Validation Failed"}`, nil)
	_, err := testAsyncClient(srv.URL).MergeStackAsync(42, "merge")
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
			srv := serveOnce(t, http.StatusOK, tt.body, &rec)
			res, err := testAsyncClient(srv.URL).GetAsyncMergeResult(42, "u")
			require.NoError(t, err)
			assert.Equal(t, http.MethodGet, rec.method)
			assert.Equal(t, "/repos/o/r/pulls/42/merge-async/u", rec.path)
			assert.Equal(t, tt.wantQueued, res.Queued)
			assert.Equal(t, tt.wantMerged, res.Merged)
		})
	}
}

func TestGetAsyncMergeResult_NotFound(t *testing.T) {
	srv := serveOnce(t, http.StatusNotFound, `{"message":"Not Found"}`, nil)
	_, err := testAsyncClient(srv.URL).GetAsyncMergeResult(42, "missing")
	require.Error(t, err)
}

func TestRestBaseURL(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{"", "https://api.github.com/"},
		{"github.com", "https://api.github.com/"},
		{"github.example.com", "https://github.example.com/api/v3/"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, restBaseURL(tt.host), "host %q", tt.host)
	}
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
	srv := serveOnce(t, http.StatusAccepted, `{"queued":true,"merged":false,"details":{"message":"m","uuid":"u","merge_method":"merge","expected_head_sha":"x"}}`, &rec)
	_, err := testAsyncClient(srv.URL).MergeStackAsync(1, "")
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(rec.body), &parsed))
	_, hasMethod := parsed["merge_method"]
	assert.False(t, hasMethod, "merge_method should be omitted when empty")
}
