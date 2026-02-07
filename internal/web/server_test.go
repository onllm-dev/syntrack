package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServer_StartsOnPort(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler(nil, nil, logger, nil, nil)
	server := NewServer(0, handler, logger, "admin", HashPassword("test")) // Port 0 = auto-assign

	var wg sync.WaitGroup
	wg.Add(1)
	var startErr error
	go func() {
		defer wg.Done()
		startErr = server.Start()
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Check server is listening
	addr := server.httpServer.Addr
	if addr == "" {
		t.Fatal("Server address should not be empty")
	}

	// Shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(ctx)
	wg.Wait()

	if startErr != nil && startErr != http.ErrServerClosed {
		t.Fatalf("Unexpected error: %v", startErr)
	}
}

func TestServer_ServesHTML(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler(nil, nil, logger, nil, nil)
	server := NewServer(0, handler, logger, "admin", HashPassword("test"))

	// Start server
	go server.Start()
	time.Sleep(100 * time.Millisecond)

	// Get the actual port
	addr := server.httpServer.Addr
	if addr == "" {
		t.Fatal("Server not started")
	}

	// Make request
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("Expected text/html content type, got %s", contentType)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "SynTrack") {
		t.Error("Expected body to contain 'SynTrack'")
	}

	// Shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

func TestServer_ServesStaticCSS(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler(nil, nil, logger, nil, nil)
	server := NewServer(0, handler, logger, "admin", HashPassword("test"))

	go server.Start()
	time.Sleep(100 * time.Millisecond)

	addr := server.httpServer.Addr
	resp, err := http.Get("http://" + addr + "/static/style.css")
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType != "text/css" {
		t.Errorf("Expected text/css content type, got %s", contentType)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "SynTrack") {
		t.Error("Expected CSS to contain 'SynTrack'")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

func TestServer_ServesStaticJS(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler(nil, nil, logger, nil, nil)
	server := NewServer(0, handler, logger, "admin", HashPassword("test"))

	go server.Start()
	time.Sleep(100 * time.Millisecond)

	addr := server.httpServer.Addr
	resp, err := http.Get("http://" + addr + "/static/app.js")
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType != "application/javascript" {
		t.Errorf("Expected application/javascript content type, got %s", contentType)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "SynTrack") {
		t.Error("Expected JS to contain 'SynTrack'")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

func TestServer_GracefulShutdown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler(nil, nil, logger, nil, nil)
	server := NewServer(0, handler, logger, "admin", HashPassword("test"))

	go server.Start()
	time.Sleep(100 * time.Millisecond)

	// Make a request that will complete
	addr := server.httpServer.Addr
	resp, err := http.Get("http://" + addr + "/static/style.css")
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	resp.Body.Close()

	// Shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err = server.Shutdown(ctx)
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	if duration > 5*time.Second {
		t.Errorf("Shutdown took too long: %v", duration)
	}
}

func TestServer_EmbeddedAssets(t *testing.T) {
	// Test that embedded assets are accessible
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler(nil, nil, logger, nil, nil)
	server := NewServer(0, handler, logger, "admin", HashPassword("test"))

	go server.Start()
	time.Sleep(100 * time.Millisecond)

	addr := server.httpServer.Addr

	// Test all embedded files
	tests := []struct {
		path         string
		expectInBody string
	}{
		{"/static/style.css", "SynTrack"},
		{"/static/app.js", "SynTrack"},
	}

	for _, tt := range tests {
		resp, err := http.Get("http://" + addr + tt.path)
		if err != nil {
			t.Fatalf("Failed to get %s: %v", tt.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200 for %s, got %d", tt.path, resp.StatusCode)
		}

		if !strings.Contains(string(body), tt.expectInBody) {
			t.Errorf("Expected %s to contain '%s'", tt.path, tt.expectInBody)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

func TestMain(m *testing.M) {
	// Ensure templates directory exists for tests
	os.Exit(m.Run())
}
