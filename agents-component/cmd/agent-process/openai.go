package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

const defaultOpenAIModel = "gpt-4o-mini"

type openAIClient struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

type openAIChatRequest struct {
	Model     string          `json:"model"`
	Messages  []openAIMessage `json:"messages"`
	Tools     []openAITool    `json:"tools,omitempty"`
	MaxTokens int64           `json:"max_tokens"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type openAIToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChatResponse struct {
	Choices []struct {
		FinishReason string        `json:"finish_reason"`
		Message      openAIMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func newOpenAIClientFromEnv() (*openAIClient, error) {
	apiKey := strings.TrimSpace(getenvFirst("OPENAI_API_KEY", "OPENAI_API_KEY_RUNTIME"))
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY environment variable is not set")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(getenvDefault("OPENAI_BASE_URL", "https://api.openai.com/v1")), "/")
	model := strings.TrimSpace(getenvDefault("OPENAI_MODEL", defaultOpenAIModel))
	return &openAIClient{apiKey: apiKey, baseURL: baseURL, model: model, http: &http.Client{Timeout: 180 * time.Second}}, nil
}

func (c *openAIClient) messagesNew(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	messages, err := openAIMessages(params.System, params.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := openAITools(params.Tools)
	if err != nil {
		return nil, err
	}
	reqBody := openAIChatRequest{Model: c.model, Messages: messages, Tools: tools, MaxTokens: params.MaxTokens}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal OpenAI request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build OpenAI request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OpenAI API call: %w", err)
	}
	defer resp.Body.Close()
	var out openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode OpenAI response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := resp.Status
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		return nil, fmt.Errorf("OpenAI API returned %s", msg)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("OpenAI response contained no choices")
	}
	return openAIChoiceToAnthropic(c.model, out.Choices[0].Message, out.Choices[0].FinishReason, out.Usage.PromptTokens, out.Usage.CompletionTokens)
}

func openAIMessages(system []anthropic.TextBlockParam, history []anthropic.MessageParam) ([]openAIMessage, error) {
	messages := make([]openAIMessage, 0, len(history)+1)
	if len(system) > 0 {
		parts := make([]string, 0, len(system))
		for _, block := range system {
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		if len(parts) > 0 {
			messages = append(messages, openAIMessage{Role: "system", Content: strings.Join(parts, "\n\n")})
		}
	}
	var raw []struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	b, err := json.Marshal(history)
	if err != nil {
		return nil, fmt.Errorf("marshal history: %w", err)
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal history: %w", err)
	}
	for _, msg := range raw {
		converted, err := openAIConvertMessage(msg.Role, msg.Content)
		if err != nil {
			return nil, err
		}
		messages = append(messages, converted...)
	}
	return messages, nil
}

func openAIConvertMessage(role string, blocks []json.RawMessage) ([]openAIMessage, error) {
	var textParts []string
	var toolCalls []openAIToolCall
	var messages []openAIMessage
	for _, raw := range blocks {
		var block struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, fmt.Errorf("unmarshal history block: %w", err)
		}
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case "tool_use":
			toolCalls = append(toolCalls, openAIToolCall{ID: block.ID, Type: "function", Function: openAIToolCallFunction{Name: block.Name, Arguments: rawJSONOrObject(block.Input)}})
		case "tool_result":
			messages = append(messages, openAIMessage{Role: "tool", ToolCallID: block.ToolUseID, Content: toolResultText(block.Content, block.IsError)})
		}
	}
	if role == "assistant" {
		messages = append([]openAIMessage{{Role: "assistant", Content: strings.Join(textParts, "\n"), ToolCalls: toolCalls}}, messages...)
		return messages, nil
	}
	if len(textParts) > 0 {
		messages = append([]openAIMessage{{Role: "user", Content: strings.Join(textParts, "\n")}}, messages...)
	}
	return messages, nil
}

func openAITools(toolDefs []anthropic.ToolUnionParam) ([]openAITool, error) {
	tools := make([]openAITool, 0, len(toolDefs))
	for _, def := range toolDefs {
		b, err := json.Marshal(def)
		if err != nil {
			return nil, fmt.Errorf("marshal tool definition: %w", err)
		}
		var raw struct {
			Name        string                 `json:"name"`
			Description string                 `json:"description"`
			InputSchema map[string]interface{} `json:"input_schema"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, fmt.Errorf("unmarshal tool definition: %w", err)
		}
		if raw.Name == "" {
			continue
		}
		if raw.InputSchema == nil {
			raw.InputSchema = map[string]interface{}{"type": "object"}
		}
		tools = append(tools, openAITool{Type: "function", Function: openAIFunction{Name: raw.Name, Description: raw.Description, Parameters: raw.InputSchema}})
	}
	return tools, nil
}

func openAIChoiceToAnthropic(model string, msg openAIMessage, finishReason string, inputTokens, outputTokens int64) (*anthropic.Message, error) {
	stopReason := "end_turn"
	if finishReason == "tool_calls" || len(msg.ToolCalls) > 0 {
		stopReason = "tool_use"
	} else if finishReason == "length" {
		stopReason = "max_tokens"
	}
	content := make([]map[string]interface{}, 0, 1+len(msg.ToolCalls))
	if msg.Content != "" {
		content = append(content, map[string]interface{}{"type": "text", "text": msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		var input interface{}
		if strings.TrimSpace(tc.Function.Arguments) == "" {
			input = map[string]interface{}{}
		} else if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
			input = map[string]interface{}{"raw_arguments": tc.Function.Arguments}
		}
		content = append(content, map[string]interface{}{"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": input})
	}
	raw := map[string]interface{}{
		"id":          "openai-chat-completion",
		"type":        "message",
		"role":        "assistant",
		"model":       model,
		"stop_reason": stopReason,
		"content":     content,
		"usage": map[string]interface{}{
			"input_tokens":                inputTokens,
			"output_tokens":               outputTokens,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     0,
		},
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var out anthropic.Message
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("convert OpenAI response: %w", err)
	}
	return &out, nil
}

func toolResultText(raw json.RawMessage, isError bool) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		parts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		if len(parts) > 0 {
			if isError {
				return "ERROR: " + strings.Join(parts, "\n")
			}
			return strings.Join(parts, "\n")
		}
	}
	if isError {
		return "ERROR: " + string(raw)
	}
	return string(raw)
}

func rawJSONOrObject(raw json.RawMessage) string {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" {
		return "{}"
	}
	return string(raw)
}

func getenvFirst(keys ...string) string {
	for _, key := range keys {
		if val := strings.TrimSpace(getenvDefault(key, "")); val != "" {
			return val
		}
	}
	return ""
}

func getenvDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
