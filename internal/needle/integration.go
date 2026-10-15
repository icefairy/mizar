// Package needle provides a Needle 2 integration for Mizar.
// Needle 2 is a 45M-parameter tool-calling model that outputs 100% valid JSON.
package needle

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"mizar/internal/plugins"
)

// httpClient wraps the HTTP client for Needle 2 server communication.
type httpClient struct {
	baseURL    string
	httpClient interface{} // *http.Client
}

// Agent wraps the Needle 2 agent with Mizar-specific logic.
type Agent struct {
	serverURL string
	mu        sync.RWMutex
	tools     []Tool // cached tool definitions
}

// NewAgent creates a new Needle 2 agent connected to the server.
func NewAgent(serverURL string) *Agent {
	return &Agent{
		serverURL: serverURL,
	}
}

// SelectTool selects a tool from the query using Needle 2.
// Returns the tool name and arguments, or empty if no tool should be called.
func (a *Agent) SelectTool(ctx context.Context, query string) (*SelectResult, error) {
	a.mu.RLock()
	tools := a.tools
	a.mu.RUnlock()

	if len(tools) == 0 {
		return nil, fmt.Errorf("no tools available")
	}

	// Build HTTP client (lazy initialization)
	client := New(a.serverURL)
	return client.SelectTool(ctx, query, tools)
}

// SetTools updates the available tools.
func (a *Agent) SetTools(tools []plugins.Tool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.tools = make([]Tool, len(tools))
	for i, t := range tools {
		a.tools[i] = Tool{
			Name:        t.Name,
			Description: t.Description,
			// Parameters field needs to be added to plugins.Tool
		}
	}
}

// Health checks if the Needle 2 server is reachable.
func (a *Agent) Health(ctx context.Context) (bool, error) {
	client := New(a.serverURL)
	return client.Health(ctx)
}

// IsAvailable returns true if Needle 2 is ready to use.
func (a *Agent) IsAvailable(ctx context.Context) bool {
	healthy, err := a.Health(ctx)
	return err == nil && healthy
}

// ExtractActionJSON tries to extract a tool call from the LLM output.
// If Needle 2 is available, it uses Needle 2 for selection.
// Otherwise, falls back to regex-based extraction.
func (a *Agent) ExtractActionJSON(ctx context.Context, query string, llmOutput string, tools []plugins.Tool) (string, bool) {
	// Update tools
	a.SetTools(tools)

	// Try Needle 2 first
	if a.IsAvailable(ctx) {
		result, err := a.SelectTool(ctx, query)
		if err == nil && result.Tool != "" {
			// Needle 2 selected a tool
			argsJSON, _ := json.Marshal(result.Arguments)
			return fmt.Sprintf(`{"action":"tool","tool":"%s","args":"%s"}`, result.Tool, string(argsJSON)), true
		}
	}

	// Fallback to regex extraction
	return fallbackExtract(llmOutput), false
}

// fallbackExtract is a simple regex-based fallback for when Needle 2 is unavailable.
func fallbackExtract(text string) string {
	// Simple extraction: look for {"action":"tool"...} pattern
	if strings.Contains(text, `"action":"tool"`) {
		start := strings.Index(text, `{`)
		end := strings.LastIndex(text, `}`)
		if start != -1 && end > start {
			return text[start : end+1]
		}
	}
	return ""
}

// CompileTime is the build timestamp.
const CompileTime = "2026-09-17T19:00:00Z"
