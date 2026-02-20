// Package web provides the HTTP server and dashboard for ralph-wiggo.
package web

import (
	"context"
	"embed"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/radvoogh/ralph-wiggo/internal/claude"
	"github.com/radvoogh/ralph-wiggo/internal/prd"
	"github.com/radvoogh/ralph-wiggo/internal/state"
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

// storyDetailData is the template context for a story detail page.
type storyDetailData struct {
	Project     string
	BranchName  string
	Story       prd.UserStory
	StatusClass string
	HasStore    bool
}

// Server is the web dashboard HTTP server.
type Server struct {
	prdPath string
	tmpl    *template.Template
	srv     *http.Server
	store   *state.MemoryStore
}

// NewServer creates a new web server that reads PRD data from the given path.
// The store parameter may be nil if no state store is available.
func NewServer(prdPath string, port int, store *state.MemoryStore) (*Server, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}

	s := &Server{
		prdPath: prdPath,
		tmpl:    tmpl,
		store:   store,
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

	// Story detail page.
	mux.HandleFunc("/story/", s.handleStoryDetail)

	// SSE streaming endpoint for story events.
	mux.HandleFunc("/api/story/", s.handleStoryAPI)

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

// handleStoryDetail renders the story detail page.
func (s *Server) handleStoryDetail(w http.ResponseWriter, r *http.Request) {
	storyID := strings.TrimPrefix(r.URL.Path, "/story/")
	if storyID == "" {
		http.NotFound(w, r)
		return
	}

	p, err := prd.LoadPRD(s.prdPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("loading PRD: %v", err), http.StatusInternalServerError)
		return
	}

	var story *prd.UserStory
	for i := range p.UserStories {
		if p.UserStories[i].ID == storyID {
			story = &p.UserStories[i]
			break
		}
	}
	if story == nil {
		http.NotFound(w, r)
		return
	}

	statusClass := "pending"
	if story.Passes {
		statusClass = "passed"
	} else if s.store != nil {
		session := s.store.GetLatestSession(storyID)
		if session != nil {
			switch session.Status {
			case state.StatusRunning:
				statusClass = "running"
			case state.StatusFailed:
				statusClass = "failed"
			}
		}
	}

	data := storyDetailData{
		Project:     p.Project,
		BranchName:  p.BranchName,
		Story:       *story,
		StatusClass: statusClass,
		HasStore:    s.store != nil,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "story.html", data); err != nil {
		http.Error(w, fmt.Sprintf("rendering story detail: %v", err), http.StatusInternalServerError)
	}
}

// handleStoryAPI routes /api/story/<id>/stream to the SSE handler.
func (s *Server) handleStoryAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/story/")
	if strings.HasSuffix(path, "/stream") {
		storyID := strings.TrimSuffix(path, "/stream")
		s.handleSSEStream(w, r, storyID)
		return
	}
	http.NotFound(w, r)
}

// handleSSEStream streams story events as server-sent events.
func (s *Server) handleSSEStream(w http.ResponseWriter, r *http.Request, storyID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Verify the story exists.
	p, err := prd.LoadPRD(s.prdPath)
	if err != nil {
		writeSSE(w, "message", `<div class="event event-error">Failed to load PRD</div>`)
		writeSSE(w, "done", "")
		flusher.Flush()
		return
	}

	var story *prd.UserStory
	for i := range p.UserStories {
		if p.UserStories[i].ID == storyID {
			story = &p.UserStories[i]
			break
		}
	}
	if story == nil {
		writeSSE(w, "message", `<div class="event event-error">Story not found</div>`)
		writeSSE(w, "done", "")
		flusher.Flush()
		return
	}

	if s.store == nil {
		writeSSE(w, "message", `<div class="event event-info">No event data available</div>`)
		writeSSE(w, "done", "")
		flusher.Flush()
		return
	}

	// For completed stories: send all historical events then close.
	if story.Passes {
		session := s.store.GetLatestSession(storyID)
		if session != nil {
			for _, iter := range session.Iterations {
				for _, evt := range iter.Events {
					h := renderEventHTML(evt)
					if h != "" {
						writeSSE(w, "message", h)
					}
				}
			}
		}
		writeSSE(w, "message", `<div class="event event-result">Stream complete</div>`)
		writeSSE(w, "done", "")
		flusher.Flush()
		return
	}

	// For running/pending stories: subscribe to live events.
	snapshot, ch, unsub := s.store.Subscribe(storyID)
	defer unsub()

	// Send events that were published before the subscription.
	for _, evt := range snapshot {
		h := renderEventHTML(evt)
		if h != "" {
			writeSSE(w, "message", h)
		}
	}
	flusher.Flush()

	// Stream live events until the channel closes or the client disconnects.
	ctx := r.Context()
	for {
		select {
		case evt, open := <-ch:
			if !open {
				writeSSE(w, "message", `<div class="event event-result">Stream complete</div>`)
				writeSSE(w, "done", "")
				flusher.Flush()
				return
			}
			h := renderEventHTML(evt)
			if h != "" {
				writeSSE(w, "message", h)
				flusher.Flush()
			}
		case <-ctx.Done():
			return
		}
	}
}

// writeSSE writes a single server-sent event to the response writer.
func writeSSE(w http.ResponseWriter, event, data string) {
	fmt.Fprintf(w, "event: %s\n", event)
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
}

// renderEventHTML converts a streaming event into an HTML fragment for the SSE stream.
func renderEventHTML(evt claude.StreamEvent) string {
	switch evt.Type {
	case claude.EventAssistant:
		if evt.Message == "" {
			return ""
		}
		return fmt.Sprintf(`<div class="event event-text">%s</div>`, html.EscapeString(evt.Message))
	case claude.EventToolUse:
		input := ""
		if evt.Input != nil {
			input = truncateString(string(evt.Input), 2000)
		}
		return fmt.Sprintf(`<div class="event event-tool"><details><summary>tool: %s</summary><pre class="tool-io">%s</pre></details></div>`,
			html.EscapeString(evt.ToolName), html.EscapeString(input))
	case claude.EventToolResult:
		output := ""
		if evt.Output != nil {
			output = truncateString(string(evt.Output), 2000)
		}
		return fmt.Sprintf(`<div class="event event-tool-result"><details><summary>result</summary><pre class="tool-io">%s</pre></details></div>`,
			html.EscapeString(output))
	case claude.EventError:
		return fmt.Sprintf(`<div class="event event-error">%s</div>`, html.EscapeString(evt.Message))
	case claude.EventInit:
		if evt.SessionID == "" {
			return ""
		}
		return fmt.Sprintf(`<div class="event event-init">session: %s</div>`, html.EscapeString(evt.SessionID))
	case claude.EventResult:
		return `<div class="event event-result">Agent finished</div>`
	default:
		return ""
	}
}

// truncateString limits a string to maxLen characters, appending an indicator if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "... (truncated)"
}
