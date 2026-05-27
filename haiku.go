package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

const (
	haikuModel    = "claude-haiku-4-5-20251001"
	defaultSystem = "You are a browser page analysis assistant. Answer concisely based on the provided page data. Include specific positions, selectors, and values when relevant. Be direct — no preamble."
	anthropicAPI  = "https://api.anthropic.com/v1/messages"
)

// HaikuClient calls the Anthropic API for preprocessing tool results.
type HaikuClient struct {
	apiKey     string
	httpClient *http.Client
	model      string
}

var (
	haiku           *HaikuClient
	haikuInitReason string // why haiku is nil; surfaced in the unavailable banner
)

func initHaiku() {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("CLAUDE_API_KEY")
	}
	if apiKey == "" {
		haikuInitReason = "no ANTHROPIC_API_KEY / CLAUDE_API_KEY in MCP server env at startup (the host may not be forwarding it — check the MCP `env` block in your client config)"
		logger.Println("No ANTHROPIC_API_KEY or CLAUDE_API_KEY — Haiku preprocessing disabled (tools still work, return raw data)")
		return
	}
	haiku = &HaikuClient{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		model:      haikuModel,
	}
	logger.Printf("Haiku preprocessing enabled (model=%s)", haikuModel)
}

// Anthropic API types

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *imageSource `json:"source,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // "image/jpeg"
	Data      string `json:"data"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func (h *HaikuClient) ask(question, context, imageBase64, systemPrompt string) (string, error) {
	if systemPrompt == "" {
		systemPrompt = defaultSystem
	}

	var blocks []contentBlock

	if imageBase64 != "" {
		blocks = append(blocks, contentBlock{
			Type: "image",
			Source: &imageSource{
				Type:      "base64",
				MediaType: "image/jpeg",
				Data:      imageBase64,
			},
		})
	}

	// Truncate context to 80k chars
	if len(context) > 80000 {
		context = context[:80000]
	}
	blocks = append(blocks,
		contentBlock{Type: "text", Text: "Context data:\n" + context},
		contentBlock{Type: "text", Text: "Question: " + question},
	)

	reqBody := anthropicRequest{
		Model:     h.model,
		MaxTokens: 1024,
		System:    systemPrompt,
		Messages: []anthropicMessage{
			{Role: "user", Content: blocks},
		},
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", anthropicAPI, bytes.NewReader(body))
	req.Header.Set("x-api-key", h.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("haiku API error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("haiku API %d: %s", resp.StatusCode, string(errBody))
	}

	var result anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("haiku parse error: %w", err)
	}

	var texts []string
	for _, block := range result.Content {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "\n"), nil
}

// maybeAsk returns raw data if no question is provided, or pipes through
// the configured AI backend (Haiku, Chrome's built-in Gemini Nano, or
// auto-fallback). See aiBackend() for the routing policy.
func maybeAsk(rawData json.RawMessage, question, imageBase64 string) (*mcp.CallToolResult, error) {
	if question == "" {
		return mcp.NewToolResultText(string(rawData)), nil
	}
	return askViaBackend(rawData, question, "", imageBase64)
}

// maybeAskWithSystem always tries to post-process via the configured
// backend, with a custom system prompt. Used by custom tools that bake
// in their own instructions for the post-processor.
func maybeAskWithSystem(rawData json.RawMessage, question, systemPrompt, imageBase64 string) (*mcp.CallToolResult, error) {
	return askViaBackend(rawData, question, systemPrompt, imageBase64)
}

// askViaBackend is the single routing point for AI post-processing. It
// honours TURBOWEB_AI_BACKEND, falls back gracefully when a backend
// isn't usable, and never lets a backend failure bubble up as a tool
// error — the worst case is "raw data follows" so the agent always gets
// something useful back.
//
// Notes on screenshots: only the Anthropic API path is multimodal today.
// When falling back to Gemini Nano, the image is dropped silently — local
// AI still answers the question from the text context.
func askViaBackend(rawData json.RawMessage, question, systemPrompt, imageBase64 string) (*mcp.CallToolResult, error) {
	switch aiBackend() {
	case "none":
		return mcp.NewToolResultText(string(rawData)), nil

	case "haiku":
		if haiku == nil {
			reason := haikuInitReason
			if reason == "" {
				reason = "Haiku client not initialised"
			}
			return mcp.NewToolResultText("[Haiku unavailable (" + reason + ") — raw data follows]\n" + string(rawData)), nil
		}
		answer, err := haiku.ask(question, string(rawData), imageBase64, systemPrompt)
		if err != nil {
			// Don't hard-error the tool — surface the cause in the banner
			// so the agent (and the human reading the transcript) can see
			// _why_ Haiku failed (bad key, deprecated model, rate limit, …).
			return mcp.NewToolResultText("[Haiku unavailable (" + err.Error() + ") — raw data follows]\n" + string(rawData)), nil
		}
		return mcp.NewToolResultText(answer), nil

	case "local":
		return askLocalOrRaw(rawData, question, systemPrompt)

	default: // "auto"
		// Prefer Haiku when configured — better quality, multimodal.
		var haikuErr error
		if haiku != nil {
			answer, err := haiku.ask(question, string(rawData), imageBase64, systemPrompt)
			if err == nil {
				return mcp.NewToolResultText(answer), nil
			}
			haikuErr = err
			logger.Printf("Haiku error, falling back to local Gemini Nano: %v", err)
		}
		return askLocalOrRawWithHaikuCause(rawData, question, systemPrompt, haikuErr)
	}
}

// askLocalOrRaw asks Chrome's built-in Gemini Nano via the extension. On
// any failure (no browser connected, model not downloaded, API absent)
// it returns the raw data with an explanatory prefix rather than erroring
// the whole tool call.
func askLocalOrRaw(rawData json.RawMessage, question, systemPrompt string) (*mcp.CallToolResult, error) {
	return askLocalOrRawWithHaikuCause(rawData, question, systemPrompt, nil)
}

// askLocalOrRawWithHaikuCause is the auto-mode-aware fallback: if the
// preceding Haiku attempt also failed, we surface _both_ causes in the
// banner so the user isn't told only about the secondary local-AI failure.
//
// Silent degradation policy: when local AI is simply not present/configured
// (LOCAL_AI_UNAVAILABLE = browser has no LanguageModel API; LOCAL_AI_NOT_READY
// = model not downloaded yet) AND Haiku was never configured, we return raw
// data with no banner. The raw data is the useful output and the banner was
// noise for users who haven't set up either AI backend. We only surface a
// banner for actionable failures: Haiku was configured but broke, or local AI
// hit an unexpected error (probe/create failed).
func askLocalOrRawWithHaikuCause(rawData json.RawMessage, question, systemPrompt string, haikuErr error) (*mcp.CallToolResult, error) {
	answer, err := localAsk(question, string(rawData), systemPrompt)
	if err == nil {
		return mcp.NewToolResultText(answer), nil
	}
	errStr := err.Error()
	// "Not present" and "not yet downloaded" are expected, non-actionable states
	// for most users — especially Chromium-based browsers that don't ship Gemini Nano
	// (Arc, Brave, etc.) or haven't enabled the flag.
	localExpected := strings.Contains(errStr, "LOCAL_AI_UNAVAILABLE") ||
		strings.Contains(errStr, "LOCAL_AI_NOT_READY")

	switch {
	case haikuErr != nil:
		// Haiku was configured and failed — that's the actionable error.
		// Don't append local-AI noise when it's just "not available".
		if localExpected {
			return mcp.NewToolResultText("[Haiku unavailable (" + haikuErr.Error() + ") — raw data follows]\n" + string(rawData)), nil
		}
		return mcp.NewToolResultText("[AI unavailable (haiku: " + haikuErr.Error() + "; local: " + errStr + ") — raw data follows]\n" + string(rawData)), nil
	case localExpected:
		// Neither backend configured — return raw data silently, no banner.
		return mcp.NewToolResultText(string(rawData)), nil
	default:
		// Unexpected local AI failure (probe/create error) — surface it.
		cause := errStr
		if haiku == nil && haikuInitReason != "" {
			cause = "haiku: " + haikuInitReason + "; local: " + errStr
		}
		return mcp.NewToolResultText("[AI unavailable (" + cause + ") — raw data follows]\n" + string(rawData)), nil
	}
}
