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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/radvoogh/ralph-wiggo/internal/claude"
	"github.com/radvoogh/ralph-wiggo/internal/prd"
	"github.com/radvoogh/ralph-wiggo/internal/state"
)

//go:embed static/*
var staticFS embed.FS

//go:embed templates/*.html
var templateFS embed.FS

// storyRow is an enriched story for the dashboard table.
type storyRow struct {
	ID          string
	Title       string
	Priority    int
	Status      string // pending, running, passed, failed
	StatusClass string // CSS class matching Status
	IterCount   int    // number of iterations attempted
	Elapsed     string // human-readable time indicator
}

// dashboardData is the template context for the main dashboard.
type dashboardData struct {
	Project    string
	BranchName string
	Passed     int
	Total      int
	Percent    int
	Stories    []storyRow
}

// storyDetailData is the template context for a story detail page.
type storyDetailData struct {
	Project     string
	BranchName  string
	Story       prd.UserStory
	StatusClass string
	HasStore    bool
}

// historyData is the template context for the run history page.
type historyData struct {
	Runs []runSummary
}

// runSummary is a summary of a single run for the history list.
type runSummary struct {
	ID         string
	BranchName string
	StartTime  string
	StoryCount int
	Passed     int
	Failed     int
	Status     string
}

// runDetailData is the template context for a single run's detail page.
type runDetailData struct {
	Run      *state.Run
	Sessions []sessionSummary
	Start    string
}

// sessionSummary summarizes an agent session within a run.
type sessionSummary struct {
	StoryID       string
	Status        string
	StatusClass   string
	IterCount     int
	LastIteration string
}

// runStoryDetailData is the template context for viewing a story's iterations within a run.
type runStoryDetailData struct {
	RunID       string
	StoryID     string
	BranchName  string
	Sessions    *state.AgentSession
	Iterations  []iterationView
	StatusClass string
}

// iterationView is a single iteration for display.
type iterationView struct {
	Number    int
	Status    string
	StatusCls string
	EndTime   string
	Events    []claude.StreamEvent
}

// runProgressData is the template context for viewing progress.txt.
type runProgressData struct {
	RunID   string
	Content string
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
	funcMap := template.FuncMap{
		"renderEvent": func(evt claude.StreamEvent) template.HTML {
			return template.HTML(renderEventHTML(evt))
		},
	}
	tmpl, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html")
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

	// History routes.
	mux.HandleFunc("/history", s.handleHistory)
	mux.HandleFunc("/history/", s.handleHistoryRoutes)

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

// loadDashboardData reads the PRD and computes template data, enriching
// story rows with state store information when available.
func (s *Server) loadDashboardData() (*dashboardData, error) {
	p, err := prd.LoadPRD(s.prdPath)
	if err != nil {
		return nil, err
	}

	passed := 0
	var rows []storyRow
	for _, story := range p.UserStories {
		row := storyRow{
			ID:       story.ID,
			Title:    story.Title,
			Priority: story.Priority,
		}

		if story.Passes {
			passed++
			row.Status = "passed"
			row.StatusClass = "passed"
		} else {
			row.Status = "pending"
			row.StatusClass = "pending"
		}

		// Enrich with state store data if available.
		if s.store != nil {
			session := s.store.GetLatestSession(story.ID)
			if session != nil {
				row.IterCount = len(session.Iterations)

				if !story.Passes {
					switch session.Status {
					case state.StatusRunning:
						row.Status = "running"
						row.StatusClass = "running"
					case state.StatusFailed:
						row.Status = "failed"
						row.StatusClass = "failed"
					}
				}

				// Compute elapsed time from iterations.
				if len(session.Iterations) > 0 {
					last := session.Iterations[len(session.Iterations)-1]
					if !last.EndTime.IsZero() {
						row.Elapsed = formatElapsed(time.Since(last.EndTime))
					} else if session.Status == state.StatusRunning {
						row.Elapsed = "running…"
					}
				}
			}
		}

		if row.Elapsed == "" {
			row.Elapsed = "-"
		}

		rows = append(rows, row)
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
		Stories:    rows,
	}, nil
}

// formatElapsed returns a human-readable string for a duration.
func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
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

// handleHistory renders the run history list page.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		http.Error(w, "No state store available", http.StatusServiceUnavailable)
		return
	}

	runs, err := s.store.ListRuns()
	if err != nil {
		http.Error(w, fmt.Sprintf("listing runs: %v", err), http.StatusInternalServerError)
		return
	}

	var summaries []runSummary
	for _, run := range runs {
		passed, failed := 0, 0
		for _, sess := range run.Stories {
			switch sess.Status {
			case state.StatusPassed:
				passed++
			case state.StatusFailed:
				failed++
			}
		}
		status := string(run.Status)
		summaries = append(summaries, runSummary{
			ID:         run.ID,
			BranchName: run.BranchName,
			StartTime:  run.StartTime.Format("2006-01-02 15:04"),
			StoryCount: len(run.Stories),
			Passed:     passed,
			Failed:     failed,
			Status:     status,
		})
	}

	data := historyData{Runs: summaries}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "history.html", data); err != nil {
		http.Error(w, fmt.Sprintf("rendering history: %v", err), http.StatusInternalServerError)
	}
}

// handleHistoryRoutes routes /history/<run-id>/... paths.
func (s *Server) handleHistoryRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/history/")
	if path == "" {
		s.handleHistory(w, r)
		return
	}

	// Parse: <run-id>, <run-id>/story/<story-id>, <run-id>/progress
	parts := strings.SplitN(path, "/", 3)
	runID := parts[0]

	if len(parts) == 1 {
		s.handleRunDetail(w, r, runID)
		return
	}

	if len(parts) == 2 && parts[1] == "progress" {
		s.handleRunProgress(w, r, runID)
		return
	}

	if len(parts) == 3 && parts[1] == "story" {
		s.handleRunStoryDetail(w, r, runID, parts[2])
		return
	}

	http.NotFound(w, r)
}

// handleRunDetail renders a single run's detail page with per-story outcomes.
func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request, runID string) {
	if s.store == nil {
		http.Error(w, "No state store available", http.StatusServiceUnavailable)
		return
	}

	run, err := s.store.GetRun(runID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var sessions []sessionSummary
	for _, sess := range run.Stories {
		statusClass := "pending"
		switch sess.Status {
		case state.StatusPassed:
			statusClass = "passed"
		case state.StatusFailed:
			statusClass = "failed"
		case state.StatusRunning:
			statusClass = "running"
		}
		lastIter := ""
		if len(sess.Iterations) > 0 {
			last := sess.Iterations[len(sess.Iterations)-1]
			if !last.EndTime.IsZero() {
				lastIter = last.EndTime.Format("15:04:05")
			}
		}
		sessions = append(sessions, sessionSummary{
			StoryID:       sess.StoryID,
			Status:        string(sess.Status),
			StatusClass:   statusClass,
			IterCount:     len(sess.Iterations),
			LastIteration: lastIter,
		})
	}

	data := runDetailData{
		Run:      run,
		Sessions: sessions,
		Start:    run.StartTime.Format(time.RFC1123),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "run_detail.html", data); err != nil {
		http.Error(w, fmt.Sprintf("rendering run detail: %v", err), http.StatusInternalServerError)
	}
}

// handleRunStoryDetail renders the iteration detail for a story within a run.
func (s *Server) handleRunStoryDetail(w http.ResponseWriter, r *http.Request, runID, storyID string) {
	if s.store == nil {
		http.Error(w, "No state store available", http.StatusServiceUnavailable)
		return
	}

	run, err := s.store.GetRun(runID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var session *state.AgentSession
	for _, sess := range run.Stories {
		if sess.StoryID == storyID {
			session = sess
			break
		}
	}
	if session == nil {
		http.NotFound(w, r)
		return
	}

	statusClass := "pending"
	switch session.Status {
	case state.StatusPassed:
		statusClass = "passed"
	case state.StatusFailed:
		statusClass = "failed"
	case state.StatusRunning:
		statusClass = "running"
	}

	var iterations []iterationView
	for _, iter := range session.Iterations {
		iterCls := "pending"
		switch iter.Status {
		case state.StatusPassed:
			iterCls = "passed"
		case state.StatusFailed:
			iterCls = "failed"
		case state.StatusRunning:
			iterCls = "running"
		}
		endTime := ""
		if !iter.EndTime.IsZero() {
			endTime = iter.EndTime.Format("2006-01-02 15:04:05")
		}
		iterations = append(iterations, iterationView{
			Number:    iter.Number,
			Status:    string(iter.Status),
			StatusCls: iterCls,
			EndTime:   endTime,
			Events:    iter.Events,
		})
	}

	data := runStoryDetailData{
		RunID:       runID,
		StoryID:     storyID,
		BranchName:  run.BranchName,
		Sessions:    session,
		Iterations:  iterations,
		StatusClass: statusClass,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "run_story.html", data); err != nil {
		http.Error(w, fmt.Sprintf("rendering run story detail: %v", err), http.StatusInternalServerError)
	}
}

// handleRunProgress shows progress.txt content for a run.
func (s *Server) handleRunProgress(w http.ResponseWriter, r *http.Request, runID string) {
	if s.store == nil {
		http.Error(w, "No state store available", http.StatusServiceUnavailable)
		return
	}

	// Verify the run exists.
	if _, err := s.store.GetRun(runID); err != nil {
		http.NotFound(w, r)
		return
	}

	// Read progress.txt from the same directory as prd.json.
	progressPath := filepath.Join(filepath.Dir(s.prdPath), "progress.txt")
	content := ""
	if data, err := os.ReadFile(progressPath); err == nil {
		content = string(data)
	} else {
		content = "(no progress.txt found)"
	}

	tplData := runProgressData{
		RunID:   runID,
		Content: content,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "run_progress.html", tplData); err != nil {
		http.Error(w, fmt.Sprintf("rendering progress: %v", err), http.StatusInternalServerError)
	}
}

// truncateString limits a string to maxLen characters, appending an indicator if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "... (truncated)"
}
