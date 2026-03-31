package github

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/cli/go-gh/v2/pkg/api"
	graphql "github.com/cli/shurcooL-graphql"
)

// PullRequest represents a GitHub pull request.
type PullRequest struct {
	ID          string `graphql:"id"`
	Number      int    `graphql:"number"`
	Title       string `graphql:"title"`
	State       string `graphql:"state"`
	URL         string `graphql:"url"`
	HeadRefName string `graphql:"headRefName"`
	BaseRefName string `graphql:"baseRefName"`
	IsDraft     bool   `graphql:"isDraft"`
	Merged      bool   `graphql:"merged"`
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
		ID:          n.ID,
		Number:      n.Number,
		Title:       n.Title,
		State:       n.State,
		URL:         n.URL,
		HeadRefName: n.HeadRefName,
		BaseRefName: n.BaseRefName,
		IsDraft:     n.IsDraft,
		Merged:      n.Merged,
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
		ID:          n.ID,
		Number:      n.Number,
		Title:       n.Title,
		State:       n.State,
		URL:         n.URL,
		HeadRefName: n.HeadRefName,
		BaseRefName: n.BaseRefName,
		IsDraft:     n.IsDraft,
		Merged:      n.Merged,
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

// PRDetails holds enriched pull request data for display in the TUI.
type PRDetails struct {
	Number        int
	Title         string
	State         string // OPEN, CLOSED, MERGED
	URL           string
	IsDraft       bool
	Merged        bool
	CommentsCount int
}

// FindPRDetailsForBranch fetches enriched PR data for display purposes.
// Returns nil without error if no PR exists for the branch.
func (c *Client) FindPRDetailsForBranch(branch string) (*PRDetails, error) {
	var query struct {
		Repository struct {
			PullRequests struct {
				Nodes []struct {
					ID          string `graphql:"id"`
					Number      int    `graphql:"number"`
					Title       string `graphql:"title"`
					State       string `graphql:"state"`
					URL         string `graphql:"url"`
					HeadRefName string `graphql:"headRefName"`
					BaseRefName string `graphql:"baseRefName"`
					IsDraft     bool   `graphql:"isDraft"`
					Merged      bool   `graphql:"merged"`
					Comments    struct {
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
		CommentsCount: n.Comments.TotalCount,
	}, nil
}

// DeleteStack deletes a stack on GitHub.
// TODO: Implement once the stack API is available.
func (c *Client) DeleteStack() error {
	return fmt.Errorf("deleting a stack on GitHub is not yet supported by the API")
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
