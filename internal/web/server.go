// Package web provides the HTTP server and dashboard for ralph-wiggo.
package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/radvoogh/ralph-wiggo/internal/prd"
)

//go:embed static/*
var staticFS embed.FS

//go:embed templates/*.html
var templateFS embed.FS

// dashboardData is the template context for the main dashboard.
type dashboardData struct {
	Project    string
	BranchName string
	Passed     int
	Total      int
	Percent    int
	Stories    []prd.UserStory
}

// Server is the web dashboard HTTP server.
type Server struct {
	prdPath string
	tmpl    *template.Template
	srv     *http.Server
}

// NewServer creates a new web server that reads PRD data from the given path.
func NewServer(prdPath string, port int) (*Server, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}

	s := &Server{
		prdPath: prdPath,
		tmpl:    tmpl,
	}

	mux := http.NewServeMux()

	// Serve embedded static files at /static/.
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("static fs: %w", err)
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Dashboard route.
	mux.HandleFunc("/", s.handleDashboard)

	// API endpoint for htmx polling of story list.
	mux.HandleFunc("/api/stories", s.handleStories)

	s.srv = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	return s, nil
}

// Start begins serving in a new goroutine. Use Shutdown to stop.
func (s *Server) Start() error {
	fmt.Printf("Dashboard: http://localhost%s\n", s.srv.Addr)
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("web server error: %v\n", err)
		}
	}()
	return nil
}

// ListenAndServe blocks until the server is shut down.
func (s *Server) ListenAndServe() error {
	fmt.Printf("Dashboard: http://localhost%s\n", s.srv.Addr)
	return s.srv.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// loadDashboardData reads the PRD and computes template data.
func (s *Server) loadDashboardData() (*dashboardData, error) {
	p, err := prd.LoadPRD(s.prdPath)
	if err != nil {
		return nil, err
	}

	passed := 0
	for _, story := range p.UserStories {
		if story.Passes {
			passed++
		}
	}

	total := len(p.UserStories)
	pct := 0
	if total > 0 {
		pct = (passed * 100) / total
	}

	return &dashboardData{
		Project:    p.Project,
		BranchName: p.BranchName,
		Passed:     passed,
		Total:      total,
		Percent:    pct,
		Stories:    p.UserStories,
	}, nil
}

// handleDashboard renders the full dashboard page.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data, err := s.loadDashboardData()
	if err != nil {
		http.Error(w, fmt.Sprintf("loading PRD: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		http.Error(w, fmt.Sprintf("rendering template: %v", err), http.StatusInternalServerError)
	}
}

// handleStories renders just the story list partial for htmx polling.
func (s *Server) handleStories(w http.ResponseWriter, r *http.Request) {
	data, err := s.loadDashboardData()
	if err != nil {
		http.Error(w, fmt.Sprintf("loading PRD: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "stories", data); err != nil {
		http.Error(w, fmt.Sprintf("rendering stories: %v", err), http.StatusInternalServerError)
	}
}
