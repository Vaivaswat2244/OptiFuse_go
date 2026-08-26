package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/auth"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/db"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/grpcclient"
	"github.com/Vaivaswat2244/OptiFuse_go/services/gateway/internal/handlers"
	"github.com/Vaivaswat2244/OptiFuse_go/shared/logger"
	"github.com/Vaivaswat2244/OptiFuse_go/shared/metrics"
	"github.com/Vaivaswat2244/OptiFuse_go/shared/reqid"
	"github.com/gin-gonic/gin"
)

var log *slog.Logger

// Set at build time via -ldflags "-X main.version=... -X main.commit=...".
// Logged at startup so it is always possible to tell which build a running pod
// is actually executing.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	log = logger.New("gateway")
	log.Info("starting", "version", version, "commit", commit)
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

	stopMetrics := metrics.Serve(metrics.Port(), log)

	r := gin.Default()
	r.Use(corsMiddleware())
	// Must precede requestLogger so the logged line carries the ID.
	r.Use(requestIDMiddleware())
	r.Use(requestLogger())
	r.Use(metricsMiddleware())

	// Liveness: is this process alive? Deliberately unconditional.
	//
	// A failing liveness probe makes Kubernetes *restart* the pod, so it must not
	// depend on anything external — otherwise one database blip restarts every
	// gateway in the cluster at once, which turns a brief outage into a long one.
	liveness := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
	r.GET("/healthz", liveness)
	r.GET("/health", liveness) // kept: the compose healthcheck still calls this

	// Readiness: should this pod receive traffic?
	//
	// Gated on the database only. Every authenticated request looks up a token,
	// so without Postgres the gateway can serve nothing — but the downstream gRPC
	// services are reported without gating, on purpose. If the optimizer is down,
	// marking the gateway unready would remove the only ingress and take down
	// login and repo browsing too, converting a partial outage into a total one.
	r.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		body := gin.H{"upstreams": clients.ConnStates()}

		if err := database.Ping(ctx); err != nil {
			body["status"] = "not ready"
			body["database"] = err.Error()
			c.JSON(http.StatusServiceUnavailable, body)
			return
		}
		body["status"] = "ready"
		body["database"] = "ok"
		c.JSON(http.StatusOK, body)
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

	srv := &http.Server{Addr: ":" + port, Handler: r}

	// Run the listener in the background so main can wait on a signal instead.
	go func() {
		log.Info("gateway started", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	<-sigCtx.Done()

	// Kubernetes removes the pod from Service endpoints concurrently with
	// SIGTERM rather than before it, so requests can still arrive for a moment
	// after this point. Keep serving briefly, then drain.
	log.Info("shutdown signal received, draining", "drain_delay", drainDelay)
	time.Sleep(drainDelay)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("graceful shutdown timed out, closing anyway", "error", err)
		_ = srv.Close()
	}

	// Stopped after the API so the final scrape can still observe the drain.
	if err := stopMetrics(shutdownCtx); err != nil {
		log.Warn("metrics server shutdown", "error", err)
	}

	clients.Close()
	log.Info("gateway stopped")
}

const (
	// drainDelay keeps the server accepting requests after SIGTERM while the
	// pod's removal propagates to every kube-proxy.
	drainDelay = 2 * time.Second

	// shutdownTimeout bounds the wait for in-flight requests. Kept under
	// Kubernetes' default 30s grace period so we exit before SIGKILL.
	shutdownTimeout = 25 * time.Second
)

// requestIDMiddleware assigns each request an ID and puts it on the request
// context, from where the gRPC client interceptor forwards it downstream.
//
// An inbound X-Request-ID is honoured rather than overwritten, so a load
// balancer or the frontend can supply its own and have it flow through. The ID
// is echoed in the response header: that is what lets you copy a value out of a
// browser network tab and search the logs for exactly that request.
func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" {
			id = reqid.Generate()
		}
		c.Header("X-Request-ID", id)
		c.Request = c.Request.WithContext(reqid.NewContext(c.Request.Context(), id))
		c.Next()
	}
}

// requestLogger logs every incoming HTTP request.
func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		reqid.Logger(c.Request.Context(), log).Info("request",
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
