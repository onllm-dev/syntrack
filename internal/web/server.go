package web

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed all:static/*
var staticFS embed.FS

// Server wraps an HTTP server with graceful shutdown capabilities
type Server struct {
	httpServer *http.Server
	handler    *Handler
	logger     *slog.Logger
	port       int
}

// NewServer creates a new Server instance.
// passwordHash should be a SHA-256 hex hash of the admin password.
func NewServer(port int, handler *Handler, logger *slog.Logger, username, passwordHash string) *Server {
	if port == 0 {
		port = 9211 // default port
	}

	mux := http.NewServeMux()

	// Register routes
	mux.HandleFunc("/", handler.Dashboard)
	mux.HandleFunc("/login", handler.Login)
	mux.HandleFunc("/logout", handler.Logout)
	mux.HandleFunc("/api/providers", handler.Providers)
	mux.HandleFunc("/api/current", handler.Current)
	mux.HandleFunc("/api/history", handler.History)
	mux.HandleFunc("/api/cycles", handler.Cycles)
	mux.HandleFunc("/api/summary", handler.Summary)
	mux.HandleFunc("/api/sessions", handler.Sessions)
	mux.HandleFunc("/api/insights", handler.Insights)
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			handler.UpdateSettings(w, r)
		} else {
			handler.GetSettings(w, r)
		}
	})
	mux.HandleFunc("/api/password", handler.ChangePassword)

	// Static files from embedded filesystem
	staticDir, _ := fs.Sub(staticFS, "static")
	staticHandler := http.FileServer(http.FS(staticDir))
	mux.Handle("/static/", http.StripPrefix("/static/", contentTypeHandler(staticHandler)))

	// Apply session-based authentication middleware
	var finalHandler http.Handler = mux
	if username != "" && passwordHash != "" {
		sessions := NewSessionStore(username, passwordHash, handler.store)
		handler.sessions = sessions
		finalHandler = SessionAuthMiddleware(sessions)(mux)
	}

	return &Server{
		httpServer: &http.Server{
			Addr:    ":" + strconv.Itoa(port),
			Handler: finalHandler,
		},
		handler: handler,
		logger:  logger,
		port:    port,
	}
}

// contentTypeHandler wraps a handler and sets proper Content-Type headers
func contentTypeHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set content type based on file extension before serving
		if len(r.URL.Path) > 3 {
			switch {
			case len(r.URL.Path) > 4 && r.URL.Path[len(r.URL.Path)-4:] == ".css":
				w.Header().Set("Content-Type", "text/css")
			case r.URL.Path[len(r.URL.Path)-3:] == ".js":
				w.Header().Set("Content-Type", "application/javascript")
			case len(r.URL.Path) > 4 && r.URL.Path[len(r.URL.Path)-4:] == ".svg":
				w.Header().Set("Content-Type", "image/svg+xml")
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Start begins listening for HTTP requests
func (s *Server) Start() error {
	s.logger.Info("starting web server", "addr", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down web server")
	return s.httpServer.Shutdown(ctx)
}

// GetEmbeddedTemplates returns the embedded templates filesystem
func GetEmbeddedTemplates() embed.FS {
	return templatesFS
}

// GetEmbeddedStatic returns the embedded static files filesystem
func GetEmbeddedStatic() embed.FS {
	return staticFS
}
