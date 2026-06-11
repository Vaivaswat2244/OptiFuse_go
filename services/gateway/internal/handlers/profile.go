package handlers

import (
	"net/http"

	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/auth"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/db"
	"github.com/gin-gonic/gin"
)

// GetProfile handles GET /api/profile/settings/
// Python: ProfileSettingsView.get()
func GetProfile(database *db.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := auth.MustGetUser(c)

		profile, err := database.GetProfileByUserID(c.Request.Context(), user.ID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"username":        user.Username,
			"subscription":    profile.Subscription,
			"aws_role_arn":    profile.AWSRoleARN,
			"aws_external_id": profile.AWSExternalID,
		})
	}
}

// UpdateProfile handles POST /api/profile/settings/
// Python: ProfileSettingsView.post()
func UpdateProfile(database *db.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := auth.MustGetUser(c)

		var body struct {
			AWSRoleARN string `json:"aws_role_arn" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "aws_role_arn is required"})
			return
		}

		if err := database.UpdateAWSRoleARN(c.Request.Context(), user.ID, body.AWSRoleARN); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update profile"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "AWS Role ARN updated successfully"})
	}
}
