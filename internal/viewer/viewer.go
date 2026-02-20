package viewer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/joshharrison/beadloom/internal/planner"
)

// --- Graph types (matches the visualiser's Graph schema) ---

type GraphNode struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	IsCritical bool   `json:"is_critical"`
	WaveIndex  int    `json:"wave_index"`
	BranchName string `json:"branch_name"`
}

type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type GraphMetadata struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"created_at"`
	TotalTasks int    `json:"total_tasks"`
	TotalWaves int    `json:"total_waves"`
}

type Graph struct {
	Nodes        []GraphNode   `json:"nodes"`
	Edges        []GraphEdge   `json:"edges"`
	CriticalPath []string      `json:"critical_path"`
	Metadata     GraphMetadata `json:"metadata"`
}

// toGraph converts an ExecutionPlan into the normalised Graph the UI renders.
func toGraph(plan *planner.ExecutionPlan) *Graph {
	nodes := make([]GraphNode, 0, len(plan.Tasks))
	for _, t := range plan.Tasks {
		nodes = append(nodes, GraphNode{
			ID:         t.TaskID,
			Title:      t.Title,
			Status:     "pending",
			IsCritical: t.IsCritical,
			WaveIndex:  t.WaveIndex,
			BranchName: t.BranchName,
		})
	}

	var edges []GraphEdge
	for taskID, preds := range plan.Deps.Predecessors {
		for _, pred := range preds {
			edges = append(edges, GraphEdge{From: pred, To: taskID})
		}
	}

	return &Graph{
		Nodes:        nodes,
		Edges:        edges,
		CriticalPath: plan.CriticalPath,
		Metadata: GraphMetadata{
			ID:         plan.ID,
			CreatedAt:  plan.CreatedAt.Format(time.RFC3339),
			TotalTasks: plan.TotalTasks,
			TotalWaves: plan.TotalWaves,
		},
	}
}

// --- HTTP server ---

type server struct {
	mu    sync.RWMutex
	graph *Graph
}

func (s *server) handlePostGraph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var plan planner.ExecutionPlan
	if err := json.NewDecoder(r.Body).Decode(&plan); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	g := toGraph(&plan)

	s.mu.Lock()
	s.graph = g
	s.mu.Unlock()

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(g)
}

func (s *server) handleGetGraph(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	g := s.graph
	s.mu.RUnlock()

	if g == nil {
		http.Error(w, "no graph loaded", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(g)
}

// spaHandler serves the embedded SPA static files with index.html fallback.
func spaHandler(distSub fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(distSub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try to serve the exact file
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Check if the file exists in the embedded FS
		f, err := distSub.Open(path)
		if err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}

		// Fallback to index.html for SPA client-side routing
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}

// Start launches the viewer HTTP server on the given port in the background.
// Returns the base URL (e.g. "http://localhost:7171") or an error.
func Start(port int) (string, error) {
	srv := &server{}
	mux := http.NewServeMux()

	mux.HandleFunc("/graph", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			srv.handlePostGraph(w, r)
		case http.MethodGet:
			srv.handleGetGraph(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Serve embedded SPA files
	distSub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return "", fmt.Errorf("embed sub fs: %w", err)
	}

	// Check if dist has real content (not just .gitkeep)
	hasContent := false
	fs.WalkDir(distSub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() != ".gitkeep" {
			hasContent = true
			return fs.SkipAll
		}
		return nil
	})

	if hasContent {
		mux.Handle("/", spaHandler(distSub))
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("No frontend assets embedded. Run 'make build-ui' first, then rebuild.\n"))
		})
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return "", fmt.Errorf("listen on port %d: %w", port, err)
	}

	go http.Serve(ln, mux)

	addr := fmt.Sprintf("http://localhost:%d", port)
	return addr, nil
}

// PostPlan sends an ExecutionPlan to a running viewer server.
func PostPlan(addr string, plan *planner.ExecutionPlan) error {
	data, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("marshal plan: %w", err)
	}

	resp, err := http.Post(addr+"/graph", "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("POST /graph: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("POST /graph returned %d", resp.StatusCode)
	}

	return nil
}

// IsPortOpen checks if something is listening on the given address.
func IsPortOpen(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
