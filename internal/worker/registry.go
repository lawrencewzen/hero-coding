package worker

import (
	"hero-coding/internal/tooldef"
	"hero-coding/internal/tools"
)

// buildTools instantiates the standard tool set scoped to workDir. The
// caller's allowedTools list further filters which of these are exposed
// to the LLM (see worker.Run); tools omitted from the LLM's schema are
// physically uncallable, no matter what the model decides.
func buildTools(workDir string) []tooldef.Tool {
	rt := tools.NewReadTracker()
	return []tooldef.Tool{
		&tools.BashTool{WorkDir: workDir},
		&tools.ReadFileTool{WorkDir: workDir, ReadTracker: rt},
		&tools.WriteFileTool{WorkDir: workDir, ReadTracker: rt},
		&tools.EditFileTool{WorkDir: workDir},
		&tools.GrepTool{WorkDir: workDir},
		&tools.FindTool{WorkDir: workDir},
		&tools.LsTool{WorkDir: workDir},
	}
}
