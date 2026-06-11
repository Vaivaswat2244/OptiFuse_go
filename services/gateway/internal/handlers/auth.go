package handlers

import (
	"net/http"
	"os"

	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/auth"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/db"
	"github.com/gin-gonic/gin"
)

// GitHubLogin handles POST /api/auth/github/
// Python: GitHubLogin.post()
//
// Flow:
//  1. Receive the OAuth code from the frontend
//  2. Exchange it for a GitHub access token
//  3. Fetch the GitHub user profile
//  4. Get or create the user + profile in our database
//  5. Get or create an Optifuse session token
//  6. Return the token + username to the frontend
func GitHubLogin(database *db.Pool) gin.HandlerFunc {
	clientID := os.Getenv("GITHUB_CLIENT_ID")
	clientSecret := os.Getenv("GITHUB_CLIENT_SECRET")

	return func(c *gin.Context) {
		var body struct {
			Code string `json:"code" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "code is required"})
			return
		}

		// Step 1: Exchange code for GitHub access token.
		githubToken, err := auth.ExchangeCodeForToken(body.Code, clientID, clientSecret)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "could not retrieve GitHub access token"})
			return
		}

		// Step 2: Fetch GitHub user profile.
		githubUser, err := auth.GetGitHubUser(githubToken)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "could not retrieve GitHub user profile"})
			return
		}

		// Step 3: Get or create user in our database.
		user, _, err := database.GetOrCreateUser(
			c.Request.Context(), githubUser.Login, githubUser.Email,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}

		// Step 4: Get or create profile, storing the GitHub token.
		_, err = database.GetOrCreateProfile(c.Request.Context(), user.ID, githubToken)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create profile"})
			return
		}

		// Step 5: Get or create Optifuse session token.
		token, err := database.GetOrCreateToken(c.Request.Context(), user.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create session token"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"token":    token,
			"username": user.Username,
		})
	}
}
