package update

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

const latestReleasePath = "repos/github/gh-stack/releases/latest"

func FetchLatestRelease(ctx context.Context) (Release, error) {
	client, err := api.NewRESTClient(api.ClientOptions{
		Host:         "github.com",
		Timeout:      2 * time.Second,
		LogIgnoreEnv: true,
	})
	if err != nil {
		return Release{}, err
	}
	var response struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := client.DoWithContext(ctx, http.MethodGet, latestReleasePath, nil, &response); err != nil {
		return Release{}, err
	}
	return Release{Version: response.TagName, URL: response.HTMLURL}, nil
}

func DefaultStatePath() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "gh-stack", "update-check.json"), nil
}
