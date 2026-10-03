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

// Budgets for the simulation pipeline. Each hop gets its own so one slow
// dependency cannot starve the next, under a single overall cap so a
// pathological request cannot tie up a worker for the sum of every hop.
const (
	// The hops cannot all take their maximum and still fit inside this — that is
	// deliberate. It bounds what one request can cost without having to reason
	// about the worst case of every stage at once.
	overallBudget = 120 * time.Second

	// Parsing is pure CPU over a small YAML file. Anything slower is a bug,
	// not load.
	parseBudget = 10 * time.Second

	// CloudWatch Logs Insights is poll-until-complete: the enricher starts a
	// query and waits for it to be scheduled, which dominates the time and is
	// entirely outside our control.
	enrichBudget = 45 * time.Second

	// Must exceed MtxILP's own 60s glpsol deadline, or we cancel the solver
	// before it gets the chance to give up and report.
	optimizeBudget = 90 * time.Second
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
		fetchStart := time.Now()
		yamlContent, err := auth.FetchFileFromGitHub(
			profile.GitHubAccessToken, body.Owner, body.RepoName, "serverless.yml",
		)
		fetchResult := "success"
		if err != nil {
			fetchResult = "error"
		}
		githubDuration.WithLabelValues("fetch_file", fetchResult).
			Observe(time.Since(fetchStart).Seconds())

		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}

		// One overall cap, then a budget per hop beneath it.
		//
		// A single shared budget let a slow enricher consume the optimizer's time,
		// and at 30s it was *shorter* than MtxILP's own 60s solver deadline — so a
		// large graph was cancelled by the caller before the solver had finished
		// the work it was asked to do. Deriving from the request context means a
		// client disconnect still cancels everything immediately.
		reqCtx, cancelAll := context.WithTimeout(c.Request.Context(), overallBudget)
		defer cancelAll()

		// Step 2: Parse YAML → Graph.
		parseCtx, cancelParse := context.WithTimeout(reqCtx, parseBudget)
		defer cancelParse()

		parsed, err := clients.Parser.Parse(parseCtx, body.RepoName, yamlContent)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "failed to parse serverless.yml",
				"details": err.Error(),
			})
			return
		}
		graph := parsed.Graph
		warnings := parsed.Warnings

		// Step 3: Enrich with CloudWatch data (only if AWS creds are configured).
		// If not configured, we proceed with zero-value telemetry — algorithms
		// still work, they just use YAML-derived values instead of real metrics.
		//
		// Which of the two happened is reported back as `telemetry`. The UI used
		// to say "live performance data from AWS" over numbers that had come from
		// serverless.yml, with nothing telling the user the enricher never ran.
		telemetry := "estimates"
		if profile.AWSRoleARN == "" {
			warnings = append(warnings, "No AWS role configured. These results use the runtime estimates in serverless.yml; add your role ARN in Settings to use CloudWatch data.")
		} else {
			// Log groups are /aws/lambda/{service}-{stage}-{fn}. Both halves come
			// from the YAML, and `service:` is often not the repo name.
			// Fall back to the repo name only if the YAML omits `service:`.
			serviceName := parsed.ServiceName
			if serviceName == "" {
				serviceName = body.RepoName
			}

			enrichCtx, cancelEnrich := context.WithTimeout(reqCtx, enrichBudget)
			defer cancelEnrich()

			enriched, err := clients.Enricher.Enrich(enrichCtx, graph,
				profile.AWSRoleARN, profile.AWSExternalID,
				serviceName, parsed.Stage,
			)
			if err != nil {
				// Enrichment failure is non-fatal — log and continue with base graph.
				// Python: the Django version would 500 here; we're more resilient.
				warnings = append(warnings, "CloudWatch enrichment failed: "+err.Error()+"; using YAML-derived values")
			} else {
				graph = enriched
				telemetry = "cloudwatch"
			}
		}

		// Step 4: Run all 6 optimization algorithms.
		optimizeCtx, cancelOptimize := context.WithTimeout(reqCtx, optimizeBudget)
		defer cancelOptimize()

		plan, err := clients.Optimizer.Optimize(optimizeCtx, graph)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "optimization failed",
				"details": err.Error(),
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"results":   plan,
			"warnings":  warnings,
			"telemetry": telemetry,
		})
	}
}
