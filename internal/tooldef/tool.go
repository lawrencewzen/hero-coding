// Package tooldef defines the Tool interface and related types shared between
// the tools and nova packages to avoid import cycles.
package tooldef

import "context"

// Tool is the interface implemented by all agent tools.
type Tool interface {
	Definition() ToolDef
	Execute(ctx context.Context, args map[string]any) (string, error)
}

// ToolDef is the OpenAI function-calling format tool definition.
type ToolDef struct {
	Type     string      `json:"type"` // always "function"
	Function FunctionDef `json:"function"`
}

// FunctionDef describes a callable function exposed to the LLM.
type FunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}
