package auth

import (
	"net/http"
	"strings"

	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/db"
	"github.com/gin-gonic/gin"
)

// Keys used to store values in the Gin context.
// Handlers read these with c.MustGet(auth.UserKey).(*db.User)
const (
	UserKey    = "user"
	ProfileKey = "profile"
)

// TokenAuth returns a Gin middleware that validates the Authorization header.
// It expects: Authorization: Token <40-char-hex>
// Python equivalent: rest_framework.authentication.TokenAuthentication
//
// On success: attaches *db.User to the context under UserKey.
// On failure: returns 401 and aborts.
func TokenAuth(database *db.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authorization header required"})
			c.Abort()
			return
		}

		// Header must be "Token <key>" — same format as DRF.
		if !strings.HasPrefix(header, "Token ") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authorization header must start with 'Token '"})
			c.Abort()
			return
		}

		token := strings.TrimPrefix(header, "Token ")
		if len(token) != 40 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token format"})
			c.Abort()
			return
		}

		user, err := database.GetUserByToken(c.Request.Context(), token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			c.Abort()
			return
		}

		// Attach the user to the context so handlers can access it.
		// Python: request.user is set automatically by DRF middleware.
		// Go: we do it explicitly.
		c.Set(UserKey, user)
		c.Next()
	}
}

// MustGetUser retrieves the authenticated user from the Gin context.
// Panics if called outside an authenticated route — which is a programming
// error, not a runtime error, so a panic is appropriate.
func MustGetUser(c *gin.Context) *db.User {
	return c.MustGet(UserKey).(*db.User)
}
