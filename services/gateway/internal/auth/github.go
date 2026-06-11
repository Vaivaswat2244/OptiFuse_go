package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// GitHubUser holds the fields we care about from the GitHub /user API.
// Python: user_data = user_res.json()
type GitHubUser struct {
	Login string `json:"login"` // GitHub username
	Email string `json:"email"`
}

// ExchangeCodeForToken exchanges a GitHub OAuth code for an access token.
// Python: requests.post('https://github.com/login/oauth/access_token', ...)
func ExchangeCodeForToken(code, clientID, clientSecret string) (string, error) {
	params := url.Values{}
	params.Set("client_id", clientID)
	params.Set("client_secret", clientSecret)
	params.Set("code", code)

	req, err := http.NewRequest(http.MethodPost,
		"https://github.com/login/oauth/access_token?"+params.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github token request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("github oauth error: %s", result.Error)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("github returned empty access token")
	}
	return result.AccessToken, nil
}

// GetGitHubUser fetches the authenticated user's profile from GitHub.
// Python: requests.get('https://api.github.com/user', headers=...)
func GetGitHubUser(accessToken string) (*GitHubUser, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+accessToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github user request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github user API returned %d: %s", resp.StatusCode, body)
	}

	var user GitHubUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode user response: %w", err)
	}
	if user.Login == "" {
		return nil, fmt.Errorf("github returned empty username")
	}
	return &user, nil
}

// FetchFileFromGitHub fetches a file's content from a GitHub repository.
// Python: fetch_github_file() in simulation/views.py
func FetchFileFromGitHub(accessToken, owner, repo, path string) ([]byte, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", owner, repo, path)
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+accessToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github file request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("file '%s' not found in %s/%s", path, owner, repo)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github API returned %d: %s", resp.StatusCode, body)
	}

	var fileData struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fileData); err != nil {
		return nil, fmt.Errorf("decode file response: %w", err)
	}

	// GitHub returns base64-encoded content with newlines — decode it.
	// Python: robust_b64decode(base64_content)
	content, err := decodeBase64Content(fileData.Content)
	if err != nil {
		return nil, fmt.Errorf("decode file content: %w", err)
	}
	return content, nil
}

// decodeBase64Content decodes GitHub's base64 content (which includes newlines).
func decodeBase64Content(encoded string) ([]byte, error) {
	clean := strings.ReplaceAll(encoded, "\n", "")
	clean = strings.TrimSpace(clean)
	decoded, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	return decoded, nil
}
