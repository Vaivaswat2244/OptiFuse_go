package handlers

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/auth"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/db"
	"github.com/gin-gonic/gin"
)

// iamRoleARN matches an IAM role ARN, the only thing sts:AssumeRole accepts.
//
// The CloudFormation template we hand users creates the role and exposes it as
// the stack's RoleArn output, but the console shows the *stack* ARN much more
// prominently — so pasting the wrong one is the obvious mistake to make. It is
// only caught at enrichment time, several layers away, as an opaque AWS 403.
var iamRoleARN = regexp.MustCompile(`^arn:aws[a-z-]*:iam::\d{12}:role/.+`)

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

		arn := strings.TrimSpace(body.AWSRoleARN)
		if !iamRoleARN.MatchString(arn) {
			msg := "aws_role_arn must be an IAM role ARN, e.g. arn:aws:iam::123456789012:role/Optifuse-User-Access-Role"
			if strings.HasPrefix(arn, "arn:aws:cloudformation:") {
				msg = "that is the CloudFormation stack ARN — copy the stack's RoleArn output instead, which looks like arn:aws:iam::123456789012:role/Optifuse-User-Access-Role"
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": msg})
			return
		}

		if err := database.UpdateAWSRoleARN(c.Request.Context(), user.ID, arn); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update profile"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "AWS Role ARN updated successfully"})
	}
}
