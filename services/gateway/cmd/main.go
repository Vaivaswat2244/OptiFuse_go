package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/auth"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/db"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/grpcclient"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/handlers"
	"github.com/Vaivaswat2244/OptiFuse_go/shared/logger"
	"github.com/gin-gonic/gin"
)

var log *slog.Logger

func main() {
	log = logger.New("gateway")
	ctx := context.Background()

	database, err := db.New(ctx, mustEnv("DATABASE_URL"))
	if err != nil {
		log.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	log.Info("connected to database")

	clients, err := grpcclient.New()
	if err != nil {
		log.Error("failed to connect to internal services", "error", err)
		os.Exit(1)
	}
	log.Info("connected to internal services",
		"parser", os.Getenv("PARSER_ADDR"),
		"enricher", os.Getenv("ENRICHER_ADDR"),
		"optimizer", os.Getenv("OPTIMIZER_ADDR"),
	)

	r := gin.Default()
	r.Use(corsMiddleware())
	r.Use(requestLogger())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := r.Group("/api")
	{
		api.POST("/auth/github/", handlers.GitHubLogin(database))

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
	log.Info("gateway started", "port", port)
	if err := r.Run(":" + port); err != nil {
		log.Error("server error", "error", err)
		os.Exit(1)
	}
}

// requestLogger logs every incoming HTTP request.
func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		log.Info("request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"ip", c.ClientIP(),
		)
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
		log.Error("required env var not set", "key", key)
		os.Exit(1)
	}
	return v
}
