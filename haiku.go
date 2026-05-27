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
// error — the worst case is raw data so the agent always gets something.
//
// Backend selection (TURBOWEB_AI_BACKEND env var):
//
//	"auto"   (default) — Haiku → Gemini → local Gemini Nano → silent raw
//	"haiku"  — Anthropic Claude Haiku only
//	"gemini" — Google Gemini Flash only
//	"local"  — Chrome built-in Gemini Nano only
//	"none"   — always return raw data
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
			return mcp.NewToolResultText("[Haiku unavailable (" + err.Error() + ") — raw data follows]\n" + string(rawData)), nil
		}
		return mcp.NewToolResultText(answer), nil

	case "gemini":
		if gemini == nil {
			reason := geminiInitReason
			if reason == "" {
				reason = "Gemini client not initialised"
			}
			return mcp.NewToolResultText("[Gemini unavailable (" + reason + ") — raw data follows]\n" + string(rawData)), nil
		}
		answer, err := gemini.ask(question, string(rawData), imageBase64, systemPrompt)
		if err != nil {
			return mcp.NewToolResultText("[Gemini unavailable (" + err.Error() + ") — raw data follows]\n" + string(rawData)), nil
		}
		return mcp.NewToolResultText(answer), nil

	case "local":
		return askLocalOrRaw(rawData, question, systemPrompt)

	default: // "auto"
		// Cascade: Haiku (if key set) → Gemini (if key set) → local → silent raw.
		// When a cloud provider is configured but fails, surface its error in a
		// banner (the failure is actionable — bad key, quota, etc.).
		// When no cloud provider is configured, silent raw is the right default.
		if haiku != nil {
			answer, err := haiku.ask(question, string(rawData), imageBase64, systemPrompt)
			if err == nil {
				return mcp.NewToolResultText(answer), nil
			}
			logger.Printf("Haiku error, trying Gemini: %v", err)
			if gemini != nil {
				answer, gerr := gemini.ask(question, string(rawData), imageBase64, systemPrompt)
				if gerr == nil {
					return mcp.NewToolResultText(answer), nil
				}
				logger.Printf("Gemini error: %v", gerr)
			}
			// Cloud providers configured but failed — try local then raw.
			// Report the primary (Haiku) error if local also can't help.
			return askLocalOrRawWithCloudErr(rawData, question, systemPrompt, "haiku: "+err.Error())
		}
		if gemini != nil {
			answer, err := gemini.ask(question, string(rawData), imageBase64, systemPrompt)
			if err == nil {
				return mcp.NewToolResultText(answer), nil
			}
			logger.Printf("Gemini error, trying local AI: %v", err)
			return askLocalOrRawWithCloudErr(rawData, question, systemPrompt, "gemini: "+err.Error())
		}
		// No cloud AI configured — try local, then silent raw.
		return askLocalOrRaw(rawData, question, systemPrompt)
	}
}

// askLocalOrRaw tries Chrome's built-in Gemini Nano, then returns raw data
// silently (no banner) when no AI backend is configured. Used as the terminal
// fallback when no cloud provider is set up.
func askLocalOrRaw(rawData json.RawMessage, question, systemPrompt string) (*mcp.CallToolResult, error) {
	return askLocalOrRawWithCloudErr(rawData, question, systemPrompt, "")
}

// askLocalOrRawWithCloudErr tries local AI, then falls back to raw data.
// cloudErrDesc is a human-readable description of why the preceding cloud
// provider failed (empty when no cloud provider was configured).
//
// Silent degradation policy: when no cloud provider is configured AND local AI
// is simply not present (LOCAL_AI_UNAVAILABLE) or not downloaded yet
// (LOCAL_AI_NOT_READY), return raw data with no banner. The raw data IS the
// useful output; the banner adds noise without actionable guidance for users
// who haven't set up any AI backend. We only surface a banner when a
// configured provider actually broke (bad key, quota, unexpected error).
func askLocalOrRawWithCloudErr(rawData json.RawMessage, question, systemPrompt, cloudErrDesc string) (*mcp.CallToolResult, error) {
	answer, err := localAsk(question, string(rawData), systemPrompt)
	if err == nil {
		return mcp.NewToolResultText(answer), nil
	}
	errStr := err.Error()
	// "Not present" and "not yet downloaded" are expected, non-actionable states
	// for most users — Chromium-based browsers without Gemini Nano (Arc, Brave…).
	localExpected := strings.Contains(errStr, "LOCAL_AI_UNAVAILABLE") ||
		strings.Contains(errStr, "LOCAL_AI_NOT_READY")

	switch {
	case cloudErrDesc != "":
		// A cloud provider was configured and failed. Report that error.
		// When local AI is just not available (expected), the cloud error is
		// the only actionable piece; skip the irrelevant local noise.
		if localExpected {
			return mcp.NewToolResultText("[AI unavailable (" + cloudErrDesc + ") — raw data follows]\n" + string(rawData)), nil
		}
		return mcp.NewToolResultText("[AI unavailable (" + cloudErrDesc + "; local: " + errStr + ") — raw data follows]\n" + string(rawData)), nil
	case localExpected:
		// No cloud provider configured, local AI not available — silent raw.
		return mcp.NewToolResultText(string(rawData)), nil
	default:
		// Unexpected local AI failure — surface it.
		return mcp.NewToolResultText("[AI unavailable (local: " + errStr + ") — raw data follows]\n" + string(rawData)), nil
	}
}
