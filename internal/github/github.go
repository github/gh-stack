package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	"github.com/cli/go-gh/v2/pkg/api"
	graphql "github.com/cli/shurcooL-graphql"
)

// MergeQueueEntry represents a merge queue entry. When the GraphQL field
// mergeQueueEntry is null (PR not queued), the pointer will be nil.
type MergeQueueEntry struct {
	ID string `graphql:"id"`
}

// PullRequest represents a GitHub pull request.
type PullRequest struct {
	ID              string           `graphql:"id"`
	Number          int              `graphql:"number"`
	Title           string           `graphql:"title"`
	State           string           `graphql:"state"`
	URL             string           `graphql:"url"`
	HeadRefName     string           `graphql:"headRefName"`
	BaseRefName     string           `graphql:"baseRefName"`
	IsDraft         bool             `graphql:"isDraft"`
	Merged          bool             `graphql:"merged"`
	MergeQueueEntry *MergeQueueEntry `graphql:"mergeQueueEntry"`
}

// IsQueued reports whether the pull request is currently in a merge queue.
func (pr *PullRequest) IsQueued() bool {
	return pr != nil && pr.MergeQueueEntry != nil && pr.MergeQueueEntry.ID != ""
}

// Client wraps GitHub API operations.
type Client struct {
	gql   *api.GraphQLClient
	rest  *api.RESTClient
	host  string
	owner string
	repo  string
	slug  string
}

// NewClient creates a new GitHub API client for the given repository.
// The host parameter specifies the GitHub hostname (e.g. "github.com" or a
// GHES hostname like "github.mycompany.com"). If empty, it defaults to
// "github.com".
func NewClient(host, owner, repo string) (*Client, error) {
	if host == "" {
		host = "github.com"
	}
	opts := api.ClientOptions{Host: host}
	gql, err := api.NewGraphQLClient(opts)
	if err != nil {
		return nil, fmt.Errorf("creating GraphQL client: %w", err)
	}
	rest, err := api.NewRESTClient(opts)
	if err != nil {
		return nil, fmt.Errorf("creating REST client: %w", err)
	}
	return &Client{
		gql:   gql,
		rest:  rest,
		host:  host,
		owner: owner,
		repo:  repo,
		slug:  owner + "/" + repo,
	}, nil
}

// PRURL constructs the web URL for a pull request on the given host.
func PRURL(host, owner, repo string, number int) string {
	if host == "" {
		host = "github.com"
	}
	return fmt.Sprintf("https://%s/%s/%s/pull/%d", host, owner, repo, number)
}

// FindPRForBranch finds an open PR by head branch name.
func (c *Client) FindPRForBranch(branch string) (*PullRequest, error) {
	var query struct {
		Repository struct {
			PullRequests struct {
				Nodes []PullRequest
			} `graphql:"pullRequests(headRefName: $head, states: [OPEN], first: 1)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]interface{}{
		"owner": graphql.String(c.owner),
		"name":  graphql.String(c.repo),
		"head":  graphql.String(branch),
	}

	if err := c.gql.Query("FindPRForBranch", &query, variables); err != nil {
		return nil, fmt.Errorf("querying PRs: %w", err)
	}

	nodes := query.Repository.PullRequests.Nodes
	if len(nodes) == 0 {
		return nil, nil
	}

	n := nodes[0]
	return &PullRequest{
		ID:              n.ID,
		Number:          n.Number,
		Title:           n.Title,
		State:           n.State,
		URL:             n.URL,
		HeadRefName:     n.HeadRefName,
		BaseRefName:     n.BaseRefName,
		IsDraft:         n.IsDraft,
		Merged:          n.Merged,
		MergeQueueEntry: n.MergeQueueEntry,
	}, nil
}

// FindAnyPRForBranch finds the most recent PR by head branch name regardless of state.
func (c *Client) FindAnyPRForBranch(branch string) (*PullRequest, error) {
	var query struct {
		Repository struct {
			PullRequests struct {
				Nodes []PullRequest
			} `graphql:"pullRequests(headRefName: $head, last: 1)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]interface{}{
		"owner": graphql.String(c.owner),
		"name":  graphql.String(c.repo),
		"head":  graphql.String(branch),
	}

	if err := c.gql.Query("FindAnyPRForBranch", &query, variables); err != nil {
		return nil, fmt.Errorf("querying PRs: %w", err)
	}

	nodes := query.Repository.PullRequests.Nodes
	if len(nodes) == 0 {
		return nil, nil
	}

	n := nodes[0]
	return &PullRequest{
		ID:              n.ID,
		Number:          n.Number,
		Title:           n.Title,
		State:           n.State,
		URL:             n.URL,
		HeadRefName:     n.HeadRefName,
		BaseRefName:     n.BaseRefName,
		IsDraft:         n.IsDraft,
		Merged:          n.Merged,
		MergeQueueEntry: n.MergeQueueEntry,
	}, nil
}

// CreatePR creates a new pull request.
func (c *Client) CreatePR(base, head, title, body string, draft bool) (*PullRequest, error) {
	var mutation struct {
		CreatePullRequest struct {
			PullRequest struct {
				ID          string
				Number      int
				Title       string
				State       string
				URL         string `graphql:"url"`
				HeadRefName string
				BaseRefName string
				IsDraft     bool
			}
		} `graphql:"createPullRequest(input: $input)"`
	}

	repoID, err := c.repositoryID()
	if err != nil {
		return nil, err
	}

	type CreatePullRequestInput struct {
		RepositoryID string `json:"repositoryId"`
		BaseRefName  string `json:"baseRefName"`
		HeadRefName  string `json:"headRefName"`
		Title        string `json:"title"`
		Body         string `json:"body,omitempty"`
		Draft        bool   `json:"draft"`
	}

	variables := map[string]interface{}{
		"input": CreatePullRequestInput{
			RepositoryID: repoID,
			BaseRefName:  base,
			HeadRefName:  head,
			Title:        title,
			Body:         body,
			Draft:        draft,
		},
	}

	if err := c.gql.Mutate("CreatePullRequest", &mutation, variables); err != nil {
		return nil, fmt.Errorf("creating PR: %w", err)
	}

	pr := mutation.CreatePullRequest.PullRequest
	return &PullRequest{
		ID:          pr.ID,
		Number:      pr.Number,
		Title:       pr.Title,
		State:       pr.State,
		URL:         pr.URL,
		HeadRefName: pr.HeadRefName,
		BaseRefName: pr.BaseRefName,
		IsDraft:     pr.IsDraft,
	}, nil
}

// UpdatePRBase updates the base branch of an existing pull request.
func (c *Client) UpdatePRBase(number int, base string) error {
	type updatePRRequest struct {
		Base string `json:"base"`
	}

	body, err := json.Marshal(updatePRRequest{Base: base})
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	path := fmt.Sprintf("repos/%s/%s/pulls/%d", c.owner, c.repo, number)
	return c.rest.Patch(path, bytes.NewReader(body), nil)
}

// MarkPRReadyForReview converts a draft pull request to ready for review.
func (c *Client) MarkPRReadyForReview(prID string) error {
	var mutation struct {
		MarkPullRequestReadyForReview struct {
			PullRequest struct {
				ID string
			}
		} `graphql:"markPullRequestReadyForReview(input: $input)"`
	}

	type MarkPullRequestReadyForReviewInput struct {
		PullRequestID string `json:"pullRequestId"`
	}

	variables := map[string]interface{}{
		"input": MarkPullRequestReadyForReviewInput{
			PullRequestID: prID,
		},
	}

	if err := c.gql.Mutate("MarkPullRequestReadyForReview", &mutation, variables); err != nil {
		return fmt.Errorf("marking PR ready for review: %w", err)
	}

	return nil
}

func (c *Client) repositoryID() (string, error) {
	var query struct {
		Repository struct {
			ID string
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]interface{}{
		"owner": graphql.String(c.owner),
		"name":  graphql.String(c.repo),
	}

	if err := c.gql.Query("RepositoryID", &query, variables); err != nil {
		return "", fmt.Errorf("fetching repository ID: %w", err)
	}

	return query.Repository.ID, nil
}

// PRDetails holds enriched pull request data for display in the TUI.
type PRDetails struct {
	Number        int
	Title         string
	State         string // OPEN, CLOSED, MERGED
	URL           string
	IsDraft       bool
	Merged        bool
	IsQueued      bool
	CommentsCount int
}

// FindPRDetailsForBranch fetches enriched PR data for display purposes.
// Returns nil without error if no PR exists for the branch.
func (c *Client) FindPRDetailsForBranch(branch string) (*PRDetails, error) {
	var query struct {
		Repository struct {
			PullRequests struct {
				Nodes []struct {
					ID              string           `graphql:"id"`
					Number          int              `graphql:"number"`
					Title           string           `graphql:"title"`
					State           string           `graphql:"state"`
					URL             string           `graphql:"url"`
					HeadRefName     string           `graphql:"headRefName"`
					BaseRefName     string           `graphql:"baseRefName"`
					IsDraft         bool             `graphql:"isDraft"`
					Merged          bool             `graphql:"merged"`
					MergeQueueEntry *MergeQueueEntry `graphql:"mergeQueueEntry"`
					Comments        struct {
						TotalCount int `graphql:"totalCount"`
					} `graphql:"comments"`
				}
			} `graphql:"pullRequests(headRefName: $head, last: 1)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]interface{}{
		"owner": graphql.String(c.owner),
		"name":  graphql.String(c.repo),
		"head":  graphql.String(branch),
	}

	if err := c.gql.Query("FindPRDetailsForBranch", &query, variables); err != nil {
		return nil, fmt.Errorf("querying PR details: %w", err)
	}

	nodes := query.Repository.PullRequests.Nodes
	if len(nodes) == 0 {
		return nil, nil
	}

	n := nodes[0]
	return &PRDetails{
		Number:        n.Number,
		Title:         n.Title,
		State:         n.State,
		URL:           n.URL,
		IsDraft:       n.IsDraft,
		Merged:        n.Merged,
		IsQueued:      n.MergeQueueEntry != nil && n.MergeQueueEntry.ID != "",
		CommentsCount: n.Comments.TotalCount,
	}, nil
}

// FindPRByNumber fetches a pull request by its number.
func (c *Client) FindPRByNumber(number int) (*PullRequest, error) {
	gqlNumber, err := toGraphQLInt(number)
	if err != nil {
		return nil, err
	}

	var query struct {
		Repository struct {
			PullRequest struct {
				ID              string           `graphql:"id"`
				Number          int              `graphql:"number"`
				Title           string           `graphql:"title"`
				State           string           `graphql:"state"`
				URL             string           `graphql:"url"`
				HeadRefName     string           `graphql:"headRefName"`
				BaseRefName     string           `graphql:"baseRefName"`
				IsDraft         bool             `graphql:"isDraft"`
				Merged          bool             `graphql:"merged"`
				MergeQueueEntry *MergeQueueEntry `graphql:"mergeQueueEntry"`
			} `graphql:"pullRequest(number: $number)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]interface{}{
		"owner":  graphql.String(c.owner),
		"name":   graphql.String(c.repo),
		"number": gqlNumber,
	}

	if err := c.gql.Query("FindPRByNumber", &query, variables); err != nil {
		return nil, fmt.Errorf("querying PR #%d: %w", number, err)
	}

	n := query.Repository.PullRequest
	if n.Number == 0 && n.ID == "" {
		return nil, nil
	}
	return &PullRequest{
		ID:              n.ID,
		Number:          n.Number,
		Title:           n.Title,
		State:           n.State,
		URL:             n.URL,
		HeadRefName:     n.HeadRefName,
		BaseRefName:     n.BaseRefName,
		IsDraft:         n.IsDraft,
		Merged:          n.Merged,
		MergeQueueEntry: n.MergeQueueEntry,
	}, nil
}

func toGraphQLInt(n int) (graphql.Int, error) {
	if n < math.MinInt32 || n > math.MaxInt32 {
		return 0, fmt.Errorf("number %d is out of GraphQL Int range", n)
	}
	return graphql.Int(n), nil
}

type RemoteStack struct {
	ID           int   `json:"id"`
	PullRequests []int `json:"pull_requests"`
}

// ListStacks returns all stacks in the repository.
// Returns an empty slice if no stacks exist.
// A 404 response indicates stacked PRs are not enabled for this repository.
func (c *Client) ListStacks() ([]RemoteStack, error) {
	path := fmt.Sprintf("repos/%s/%s/cli_internal/pulls/stacks", c.owner, c.repo)
	var stacks []RemoteStack
	if err := c.rest.Get(path, &stacks); err != nil {
		return nil, err
	}
	if stacks == nil {
		stacks = []RemoteStack{}
	}
	return stacks, nil
}

// CreateStack creates a stack on GitHub from an ordered list of PR numbers.
// The PR numbers must be ordered from bottom to top of the stack and must
// form a valid base-to-head chain. Returns the server-assigned stack ID.
func (c *Client) CreateStack(prNumbers []int) (int, error) {
	type createStackRequest struct {
		PullRequestNumbers []int `json:"pull_request_numbers"`
	}

	body, err := json.Marshal(createStackRequest{PullRequestNumbers: prNumbers})
	if err != nil {
		return 0, fmt.Errorf("marshaling request: %w", err)
	}

	path := fmt.Sprintf("repos/%s/%s/cli_internal/pulls/stacks", c.owner, c.repo)

	var response struct {
		ID int `json:"id"`
	}

	if err := c.rest.Post(path, bytes.NewReader(body), &response); err != nil {
		return 0, err
	}

	return response.ID, nil
}

// UpdateStack adds pull requests to an existing stack on GitHub.
// The stack is identified by stackID. The full list of PR numbers in the
// updated stack must be provided, including existing and new PRs, ordered
// from bottom to top.
func (c *Client) UpdateStack(stackID string, prNumbers []int) error {
	type updateStackRequest struct {
		PullRequestNumbers []int `json:"pull_request_numbers"`
	}

	body, err := json.Marshal(updateStackRequest{PullRequestNumbers: prNumbers})
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	path := fmt.Sprintf("repos/%s/%s/cli_internal/pulls/stacks/%s", c.owner, c.repo, stackID)

	var response struct {
		ID int `json:"id"`
	}

	return c.rest.Put(path, bytes.NewReader(body), &response)
}

// DeleteStack deletes a stack on GitHub.
// The stack is identified by stackID. Returns nil on success (204).
func (c *Client) DeleteStack(stackID string) error {
	path := fmt.Sprintf("repos/%s/%s/cli_internal/pulls/stacks/%s", c.owner, c.repo, stackID)
	return c.rest.Delete(path, nil)
}
