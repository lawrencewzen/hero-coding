package agent

import "context"

// EventKind 事件类型
type EventKind string

const (
	EventTextDelta EventKind = "text_delta" // LLM 输出文本片段
	EventToolStart EventKind = "tool_start" // 工具开始执行
	EventToolEnd   EventKind = "tool_end"   // 工具执行完毕
	EventAgentDone EventKind = "agent_done" // agent loop 正常结束
	EventError     EventKind = "error"      // 发生错误
)

// Event 是 agent loop 对外发出的事件
type Event struct {
	Kind   EventKind
	Text   string         // EventTextDelta 时有值
	Tool   string         // EventToolStart / EventToolEnd 时有值
	Args   map[string]any // EventToolStart 时有值
	Result string         // EventToolEnd 时有值
	Err    error          // EventError / EventToolEnd(isError) 时有值
}

// HookBeforeToolCall 在工具执行前调用。可修改 args（返回新 map），或返回 error 阻止执行。
// nil 表示不拦截。
type HookBeforeToolCall func(ctx context.Context, name string, args map[string]any) (map[string]any, error)

// HookAfterToolCall 在工具执行后调用。可修改 result，或将 err 包装/替换。
// nil 表示不修改。
type HookAfterToolCall func(ctx context.Context, name string, result string, err error) (string, error)
