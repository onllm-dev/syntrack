package web

import (
	"compress/gzip"
	"context"
	"embed"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
	mux.HandleFunc("/settings", handler.SettingsPage)
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
	mux.HandleFunc("/api/settings/smtp/test", handler.SMTPTest)
	mux.HandleFunc("/api/password", handler.ChangePassword)
	mux.HandleFunc("/api/cycle-overview", handler.CycleOverview)
	mux.HandleFunc("/api/update/check", handler.CheckUpdate)
	mux.HandleFunc("/api/update/apply", handler.ApplyUpdate)
	mux.HandleFunc("/api/push/vapid", handler.PushVAPIDKey)
	mux.HandleFunc("/api/push/subscribe", handler.PushSubscribe)
	mux.HandleFunc("/api/push/test", handler.PushTest)

	// Service worker (must be served from root scope, no-cache)
	mux.HandleFunc("/sw.js", func(w http.ResponseWriter, r *http.Request) {
		data, _ := staticFS.ReadFile("static/sw.js")
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Service-Worker-Allowed", "/")
		w.Write(data)
	})
	// PWA manifest
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		data, _ := staticFS.ReadFile("static/manifest.json")
		w.Header().Set("Content-Type", "application/manifest+json")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data)
	})

	// Static files from embedded filesystem
	staticDir, _ := fs.Sub(staticFS, "static")
	staticHandler := http.FileServer(http.FS(staticDir))
	mux.Handle("/static/", http.StripPrefix("/static/", contentTypeHandler(staticHandler)))

	// Apply middleware chain: security headers -> gzip compression -> auth -> routes
	var finalHandler http.Handler = mux
	if username != "" && passwordHash != "" {
		sessions := NewSessionStore(username, passwordHash, handler.store)
		handler.sessions = sessions
		finalHandler = SessionAuthMiddleware(sessions, logger)(mux)
	}
	// Apply security headers and gzip compression (outermost)
	finalHandler = securityHeadersMiddleware(gzipHandler(finalHandler))

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

// contentTypeHandler wraps a handler and sets proper Content-Type and Cache-Control headers
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
		// Immutable caching — static assets are versioned via ?v= query param
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		next.ServeHTTP(w, r)
	})
}

// gzipResponseWriter wraps http.ResponseWriter with gzip compression
type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (grw *gzipResponseWriter) Write(b []byte) (int, error) {
	return grw.Writer.Write(b)
}

var gzipWriterPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return w
	},
}

// gzipHandler compresses responses for clients that accept gzip encoding
func gzipHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		gz := gzipWriterPool.Get().(*gzip.Writer)
		gz.Reset(w)
		defer func() {
			gz.Close()
			gzipWriterPool.Put(gz)
		}()

		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Del("Content-Length")

		next.ServeHTTP(&gzipResponseWriter{Writer: gz, ResponseWriter: w}, r)
	})
}

// securityHeadersMiddleware adds security headers to all responses
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent MIME type sniffing
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Prevent clickjacking
		w.Header().Set("X-Frame-Options", "DENY")
		// Enable XSS filter (legacy browsers)
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		// Control referrer information
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// Content Security Policy
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' cdn.jsdelivr.net; "+
				"style-src 'self' 'unsafe-inline' fonts.googleapis.com; "+
				"font-src 'self' fonts.gstatic.com; "+
				"img-src 'self' data:; "+
				"connect-src 'self'; "+
				"worker-src 'self'")
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
