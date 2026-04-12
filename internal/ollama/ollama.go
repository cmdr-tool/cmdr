package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

var (
	baseURL = envOr("CMDR_OLLAMA_URL", "https://ollama.106source.ca")
	model   = envOr("CMDR_OLLAMA_MODEL", "gemma4")
)

// Summarize asks Ollama to generate a concise title for the given content.
// Uses tool calling to get structured output (just the title, no preamble).
func Summarize(ctx context.Context, content string) (string, error) {
	reqBody := chatRequest{
		Model:  model,
		Stream: false,
		Messages: []message{
			{
				Role:    "system",
				Content: "Generate a concise title (under 80 characters) summarizing the user's content. Call the set_title tool with your result.",
			},
			{
				Role:    "user",
				Content: content,
			},
		},
		Tools: []tool{{
			Type: "function",
			Function: toolFunction{
				Name:        "set_title",
				Description: "Set a concise title summarizing the content",
				Parameters: toolParams{
					Type: "object",
					Properties: map[string]toolProp{
						"title": {
							Type:        "string",
							Description: "A concise title, max 80 characters",
						},
					},
					Required: []string{"title"},
				},
			},
		}},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama: status %d", resp.StatusCode)
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}

	if len(chatResp.Message.ToolCalls) == 0 {
		return "", fmt.Errorf("ollama: no tool call in response")
	}

	title, ok := chatResp.Message.ToolCalls[0].Function.Arguments["title"]
	if !ok || title == "" {
		return "", fmt.Errorf("ollama: empty title in tool call")
	}

	return title, nil
}

// --- Request/Response types ---

type chatRequest struct {
	Model    string    `json:"model"`
	Stream   bool      `json:"stream"`
	Messages []message `json:"messages"`
	Tools    []tool    `json:"tools"`
}

type message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
}

type tool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  toolParams `json:"parameters"`
}

type toolParams struct {
	Type       string              `json:"type"`
	Properties map[string]toolProp `json:"properties"`
	Required   []string            `json:"required"`
}

type toolProp struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type chatResponse struct {
	Message message `json:"message"`
}

type toolCall struct {
	Function toolCallFunction `json:"function"`
}

type toolCallFunction struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments"`
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
