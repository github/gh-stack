package github

import (
	"fmt"

	"github.com/cli/go-gh/v2/pkg/api"
	graphql "github.com/cli/shurcooL-graphql"
)

// PullRequest represents a GitHub pull request.
type PullRequest struct {
	ID          string
	Number      int
	Title       string
	State       string
	URL         string
	HeadRefName string
	BaseRefName string
	IsDraft     bool
}

// Client wraps GitHub API operations.
type Client struct {
	gql   *api.GraphQLClient
	rest  *api.RESTClient
	owner string
	repo  string
	slug  string
}

// NewClient creates a new GitHub API client for the given repository.
func NewClient(owner, repo string) (*Client, error) {
	gql, err := api.DefaultGraphQLClient()
	if err != nil {
		return nil, fmt.Errorf("creating GraphQL client: %w", err)
	}
	rest, err := api.DefaultRESTClient()
	if err != nil {
		return nil, fmt.Errorf("creating REST client: %w", err)
	}
	return &Client{
		gql:   gql,
		rest:  rest,
		owner: owner,
		repo:  repo,
		slug:  owner + "/" + repo,
	}, nil
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

// UpdatePRBase updates the base branch of a pull request.
func (c *Client) UpdatePRBase(prID, newBase string) error {
	var mutation struct {
		UpdatePullRequest struct {
			PullRequest struct {
				ID string
			}
		} `graphql:"updatePullRequest(input: $input)"`
	}

	type UpdatePullRequestInput struct {
		PullRequestID string `json:"pullRequestId"`
		BaseRefName   string `json:"baseRefName"`
	}

	variables := map[string]interface{}{
		"input": UpdatePullRequestInput{
			PullRequestID: prID,
			BaseRefName:   newBase,
		},
	}

	return c.gql.Mutate("UpdatePullRequest", &mutation, variables)
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
