package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/auth"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/db"
	"github.com/gin-gonic/gin"
)

// ListRepos handles GET /api/repositories/
// Python: RepositoryListView.get()
func ListRepos(database *db.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := auth.MustGetUser(c)

		profile, err := database.GetProfileByUserID(c.Request.Context(), user.ID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
			return
		}
		if profile.GitHubAccessToken == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "GitHub token not configured"})
			return
		}

		repos, err := fetchGitHubRepos(profile.GitHubAccessToken)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to fetch repositories from GitHub"})
			return
		}

		c.JSON(http.StatusOK, repos)
	}
}

// GetRepoFile handles GET /api/repositories/:owner/:repo/file/
// Python: RepositoryFileView.get()
func GetRepoFile(database *db.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := auth.MustGetUser(c)
		owner := c.Param("owner")
		repo := c.Param("repo")

		profile, err := database.GetProfileByUserID(c.Request.Context(), user.ID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
			return
		}

		content, err := auth.FetchFileFromGitHub(
			profile.GitHubAccessToken, owner, repo, "serverless.yml",
		)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"filename": "serverless.yml",
			"content":  string(content),
		})
	}
}

// fetchGitHubRepos calls the GitHub API and returns the raw repo list.
// Python: requests.get('https://api.github.com/user/repos?sort=updated', ...)
func fetchGitHubRepos(accessToken string) ([]json.RawMessage, error) {
	req, err := http.NewRequest(http.MethodGet,
		"https://api.github.com/user/repos?sort=updated&per_page=100", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+accessToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github returned %d: %s", resp.StatusCode, body)
	}

	var repos []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
		return nil, fmt.Errorf("decode repos: %w", err)
	}
	return repos, nil
}
