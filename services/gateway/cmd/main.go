package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/auth"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/db"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/grpcclient"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/handlers"
	"github.com/gin-gonic/gin"
)

func main() {
	ctx := context.Background()

	// ── Database ──────────────────────────────────────────────────────────────
	database, err := db.New(ctx, mustEnv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer database.Close()
	log.Println("✓ connected to database")

	// ── gRPC clients ──────────────────────────────────────────────────────────
	clients, err := grpcclient.New()
	if err != nil {
		log.Fatalf("failed to connect to internal services: %v", err)
	}
	log.Println("✓ connected to internal services")

	// ── Gin router ────────────────────────────────────────────────────────────
	r := gin.Default()

	// CORS — allow the Next.js frontend origin.
	r.Use(corsMiddleware())

	// Health check — used by Kubernetes liveness probe.
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := r.Group("/api")
	{
		// ── Public routes (no auth required) ─────────────────────────────────
		api.POST("/auth/github/", handlers.GitHubLogin(database))

		// ── Authenticated routes ──────────────────────────────────────────────
		authed := api.Group("/")
		authed.Use(auth.TokenAuth(database))
		{
			authed.GET("/repositories/", handlers.ListRepos(database))
			authed.GET("/repositories/:owner/:repo/file/", handlers.GetRepoFile(database))
			authed.GET("/profile/settings/", handlers.GetProfile(database))
			authed.POST("/profile/settings/", handlers.UpdateProfile(database))
			authed.POST("/simulate/live/", handlers.LiveSimulate(database, clients))
		}
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("✓ gateway listening on :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := os.Getenv("CLIENT_ORIGIN_URL")
		if origin == "" {
			origin = "http://localhost:3000"
		}
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set", key)
	}
	return v
}
