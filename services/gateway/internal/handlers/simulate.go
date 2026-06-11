package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/auth"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/db"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/grpcclient"
	"github.com/gin-gonic/gin"
)

// LiveSimulate handles POST /api/simulate/live/
// Python: LiveSimulationView.post()
//
// Pipeline:
//  1. Fetch serverless.yml from GitHub
//  2. gRPC → Parser service   → Graph
//  3. gRPC → Enricher service → Enriched Graph (if AWS creds configured)
//  4. gRPC → Optimizer service → OptimizationPlan
//  5. Return results as JSON
func LiveSimulate(database *db.Pool, clients *grpcclient.Clients) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			Owner    string `json:"owner"    binding:"required"`
			RepoName string `json:"repoName" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "owner and repoName are required"})
			return
		}

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

		// Step 1: Fetch serverless.yml from GitHub.
		yamlContent, err := auth.FetchFileFromGitHub(
			profile.GitHubAccessToken, body.Owner, body.RepoName, "serverless.yml",
		)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}

		// Each downstream gRPC call gets a 30-second timeout.
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()

		// Step 2: Parse YAML → Graph.
		graph, warnings, err := clients.Parser.Parse(ctx, body.RepoName, yamlContent)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "failed to parse serverless.yml",
				"details": err.Error(),
			})
			return
		}

		// Step 3: Enrich with CloudWatch data (only if AWS creds are configured).
		// If not configured, we proceed with zero-value telemetry — algorithms
		// still work, they just use YAML-derived values instead of real metrics.
		if profile.AWSRoleARN != "" {
			enriched, err := clients.Enricher.Enrich(ctx, graph,
				profile.AWSRoleARN, profile.AWSExternalID,
				body.RepoName, "dev",
			)
			if err != nil {
				// Enrichment failure is non-fatal — log and continue with base graph.
				// Python: the Django version would 500 here; we're more resilient.
				warnings = append(warnings, "CloudWatch enrichment failed: "+err.Error()+"; using YAML-derived values")
			} else {
				graph = enriched
			}
		}

		// Step 4: Run all 6 optimization algorithms.
		plan, err := clients.Optimizer.Optimize(ctx, graph)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "optimization failed",
				"details": err.Error(),
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"results":  plan,
			"warnings": warnings,
		})
	}
}
