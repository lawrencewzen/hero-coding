package agent

import (
	"hero-coding/internal/config"
	"hero-coding/internal/logging"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
)

type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL string `json:"url"`
}

type ChatMessage struct {
	Role         string      `json:"role"`
	Content      any         `json:"content"` // string 或 []ContentPart
	ToolCallID   string      `json:"tool_call_id,omitempty"`
	ToolCalls    []ToolCall  `json:"tool_calls,omitempty"`
	Name         string      `json:"name,omitempty"`
	FinishReason string      `json:"-"`
	Usage        *LLMUsage   `json:"-"` // token 统计，纯内存传递，不序列化
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type LLMClient struct {
	cfg          *config.LLMConfig
	client       *http.Client // with timeout, for non-streaming
	streamClient *http.Client // no timeout, for SSE streaming (controlled by ctx)
}

// ModelName returns the configured model name.
func (c *LLMClient) ModelName() string {
	if c == nil || c.cfg == nil {
		return ""
	}
	return c.cfg.Model
}

func NewLLMClient(cfg *config.LLMConfig) *LLMClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &LLMClient{
		cfg: cfg,
		client: &http.Client{
			Timeout:   2 * time.Minute,
			Transport: transport,
		},
		streamClient: &http.Client{
			Transport: transport,
		},
	}
}

// ChatOptions adjusts a single Chat call. All fields are optional.
type ChatOptions struct {
	Temperature    *float64 // nil = provider default
	ResponseFormat *string  // e.g. "json_object"; nil = unset
}

// Chat 调用 OpenAI chat completions（非流式），返回 assistant message
func (c *LLMClient) Chat(ctx context.Context, msgs []ChatMessage, tools []map[string]any) (*ChatMessage, error) {
	return c.ChatWithOptions(ctx, msgs, tools, ChatOptions{})
}

// ChatWithOptions is Chat plus per-call overrides (temperature, response_format).
func (c *LLMClient) ChatWithOptions(ctx context.Context, msgs []ChatMessage, tools []map[string]any, opts ChatOptions) (*ChatMessage, error) {
	if c == nil {
		return nil, fmt.Errorf("llm client is nil")
	}
	if c.cfg == nil {
		return nil, fmt.Errorf("llm config is nil")
	}
	if c.client == nil {
		c.client = &http.Client{Timeout: 2 * time.Minute}
	}

	start := time.Now()

	reqBody := map[string]any{
		"model":    c.cfg.Model,
		"messages": msgs,
	}
	if len(tools) > 0 {
		reqBody["tools"] = tools
	}
	if opts.Temperature != nil {
		reqBody["temperature"] = *opts.Temperature
	}
	if opts.ResponseFormat != nil {
		reqBody["response_format"] = map[string]any{"type": *opts.ResponseFormat}
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal chat request: %w", err)
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build chat request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send chat request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read chat response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		raw := fmt.Errorf("chat completions returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
		return nil, classifyLLMError(raw, resp.StatusCode)
	}

	var chatResp struct {
		Choices []struct {
			Message      ChatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage *LLMUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return nil, fmt.Errorf("decode chat response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("chat response contains no choices")
	}

	msg := chatResp.Choices[0].Message
	msg.FinishReason = chatResp.Choices[0].FinishReason
	msg.Usage = chatResp.Usage
	if msg.Role == "" {
		msg.Role = "assistant"
	}

	logLLMResponse(logging.FromContext(ctx), c.cfg.Model, time.Since(start), chatResp.Usage, msg.FinishReason, len(msg.ToolCalls))

	return &msg, nil
}

// Stream 调用 OpenAI streaming chat completions（SSE），返回完整 assistant message。
// onDelta 在每个文本片段到达时调用（可为 nil）。
// 若响应 Content-Type 不含 event-stream，降级为普通 JSON 解析（兼容 mock server / 测试）。
func (c *LLMClient) Stream(ctx context.Context, msgs []ChatMessage, tools []map[string]any, onDelta func(text string)) (*ChatMessage, error) {
	if c == nil {
		return nil, fmt.Errorf("llm client is nil")
	}
	if c.cfg == nil {
		return nil, fmt.Errorf("llm config is nil")
	}

	start := time.Now()

	// 使用不带超时的 streamClient（由 ctx 控制），避免流式场景超时
	streamClient := c.streamClient

	reqBody := struct {
		Model         string           `json:"model"`
		Messages      []ChatMessage    `json:"messages"`
		Tools         []map[string]any `json:"tools,omitempty"`
		Stream        bool             `json:"stream"`
		StreamOptions *streamOptions   `json:"stream_options,omitempty"`
	}{
		Model:         c.cfg.Model,
		Messages:      msgs,
		Tools:         tools,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal stream request: %w", err)
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build stream request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send stream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(resp.Body)
		raw := fmt.Errorf("stream completions returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
		return nil, classifyLLMError(raw, resp.StatusCode)
	}

	// 如果响应不是 SSE，降级为普通 JSON 解析（兼容 mock server / 测试）
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "event-stream") {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read non-stream response: %w", err)
		}
		var chatResp struct {
			Choices []struct {
				Message      ChatMessage `json:"message"`
				FinishReason string      `json:"finish_reason"`
			} `json:"choices"`
			Usage *LLMUsage `json:"usage"`
		}
		if err := json.Unmarshal(body, &chatResp); err != nil {
			return nil, fmt.Errorf("decode non-stream response: %w", err)
		}
		if len(chatResp.Choices) == 0 {
			return nil, fmt.Errorf("non-stream response contains no choices")
		}
		msg := chatResp.Choices[0].Message
		msg.FinishReason = chatResp.Choices[0].FinishReason
		msg.Usage = chatResp.Usage
		if msg.Role == "" {
			msg.Role = "assistant"
		}
		// 通知 onDelta（全量文本一次发出）
		if onDelta != nil {
			if text := extractTextContent(msg.Content); text != "" {
				onDelta(text)
			}
		}
		logLLMResponse(logging.FromContext(ctx), c.cfg.Model, time.Since(start), chatResp.Usage, msg.FinishReason, len(msg.ToolCalls))
		return &msg, nil
	}

	// SSE 解析
	type toolCallAccum struct {
		id        string
		typ       string
		name      string
		argsAccum string
	}
	toolCallMap := make(map[int]*toolCallAccum)

	var textBuf strings.Builder
	var finishReason string
	var usage *LLMUsage

	const sseMaxLineBytes = 4 * 1024 * 1024 // 4 MB — handles large tool-call argument chunks
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), sseMaxLineBytes)

	for scanner.Scan() {
		// Check for context cancellation between SSE events.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		line := scanner.Text()

		// 跳过空行和注释行
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		// 只处理 data: 开头的行（SSE spec 允许 "data: " 和 "data:" 两种形式）
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			if data, ok = strings.CutPrefix(line, "data:"); !ok {
				continue
			}
		}

		// 结束标记
		if strings.TrimSpace(data) == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Role    string `json:"role"`
					Content string `json:"content"`
					ToolCalls []struct {
						Index int    `json:"index"`
						ID    string `json:"id"`
						Type  string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *LLMUsage `json:"usage"`
			Error *struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// 跳过无法解析的行
			continue
		}

		// 检测 SSE 流中的错误事件（如 API 验证错误）
		if chunk.Error != nil && chunk.Error.Message != "" {
			raw := fmt.Errorf("stream error from backend: %s", chunk.Error.Message)
			return nil, classifyLLMError(raw, 0)
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]

		// 更新 finish_reason（保留最后一个非空值）
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}

		// 捕获 usage（stream_options.include_usage 时最终 chunk 包含）
		if chunk.Usage != nil {
			usage = chunk.Usage
		}

		// 累积文本内容
		if delta := choice.Delta.Content; delta != "" {
			textBuf.WriteString(delta)
			if onDelta != nil {
				onDelta(delta)
			}
		}

		// 累积 tool_calls
		for _, tc := range choice.Delta.ToolCalls {
			idx := tc.Index
			if _, exists := toolCallMap[idx]; !exists {
				toolCallMap[idx] = &toolCallAccum{}
			}
			acc := toolCallMap[idx]
			if tc.ID != "" {
				acc.id = tc.ID
			}
			if tc.Type != "" {
				acc.typ = tc.Type
			}
			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}
			acc.argsAccum += tc.Function.Arguments
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan SSE stream: %w", err)
	}

	// 按 index 排序组装 ToolCalls
	var toolCalls []ToolCall
	if len(toolCallMap) > 0 {
		indices := make([]int, 0, len(toolCallMap))
		for idx := range toolCallMap {
			indices = append(indices, idx)
		}
		sort.Ints(indices)

		for _, idx := range indices {
			acc := toolCallMap[idx]
			tc := ToolCall{
				ID:   acc.id,
				Type: acc.typ,
				Function: FunctionCall{
					Name:      acc.name,
					Arguments: acc.argsAccum,
				},
			}
			if tc.Type == "" {
				tc.Type = "function"
			}
			toolCalls = append(toolCalls, tc)
		}
	}

	msg := &ChatMessage{
		Role:         "assistant",
		FinishReason: finishReason,
		Content:      textBuf.String(),
		Usage:        usage,
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}

	logLLMResponse(logging.FromContext(ctx), c.cfg.Model, time.Since(start), usage, finishReason, len(toolCalls))

	return msg, nil
}

// LLMUsage carries token counts from an LLM response.
type LLMUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func logLLMResponse(log *slog.Logger, model string, latency time.Duration, usage *LLMUsage, finishReason string, toolCallCount int) {
	attrs := []any{
		"model", model,
		"latency_ms", latency.Milliseconds(),
	}
	if usage != nil {
		attrs = append(attrs,
			"prompt_tokens", usage.PromptTokens,
			"completion_tokens", usage.CompletionTokens,
			"total_tokens", usage.TotalTokens,
		)
	}
	if finishReason != "" {
		attrs = append(attrs, "finish_reason", finishReason)
	}
	if toolCallCount > 0 {
		attrs = append(attrs, "tool_calls", toolCallCount)
	}
	log.Info("llm.response", attrs...)
}
