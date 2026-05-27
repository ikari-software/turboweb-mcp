package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// GeminiClient calls the Google Gemini API for preprocessing tool results.
// It is the second choice in the auto-backend cascade (after Haiku) and the
// only choice when TURBOWEB_AI_BACKEND=gemini.
type GeminiClient struct {
	apiKey     string
	httpClient *http.Client
	model      string
}

var (
	gemini           *GeminiClient
	geminiInitReason string // why gemini is nil; used by the banner logic
)

const (
	geminiModel   = "gemini-2.0-flash-lite"
	geminiBaseURL = "https://generativelanguage.googleapis.com/v1beta/models/"
)

func initGemini() {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		geminiInitReason = "no GEMINI_API_KEY in MCP server env"
		return
	}
	gemini = &GeminiClient{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		model:      geminiModel,
	}
	logger.Printf("Gemini preprocessing enabled (model=%s)", geminiModel)
}

// ── Gemini API request / response types ──────────────────────────────────────

type geminiRequest struct {
	SystemInstruction *geminiContent `json:"systemInstruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
	GenerationConfig  geminiGenConfig `json:"generationConfig"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text       string          `json:"text,omitempty"`
	InlineData *geminiInline   `json:"inlineData,omitempty"`
}

type geminiInline struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"` // base64
}

type geminiGenConfig struct {
	MaxOutputTokens int `json:"maxOutputTokens"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error,omitempty"`
}

func (g *GeminiClient) ask(question, context, imageBase64, systemPrompt string) (string, error) {
	if systemPrompt == "" {
		systemPrompt = defaultSystem
	}

	// Truncate context to 80k chars (same limit as Haiku)
	if len(context) > 80000 {
		context = context[:80000]
	}

	var parts []geminiPart
	if imageBase64 != "" {
		parts = append(parts, geminiPart{
			InlineData: &geminiInline{MimeType: "image/jpeg", Data: imageBase64},
		})
	}
	parts = append(parts,
		geminiPart{Text: "Context data:\n" + context},
		geminiPart{Text: "Question: " + question},
	)

	reqBody := geminiRequest{
		SystemInstruction: &geminiContent{
			Parts: []geminiPart{{Text: systemPrompt}},
		},
		Contents: []geminiContent{
			{Role: "user", Parts: parts},
		},
		GenerationConfig: geminiGenConfig{MaxOutputTokens: 1024},
	}

	body, _ := json.Marshal(reqBody)
	url := geminiBaseURL + g.model + ":generateContent?key=" + g.apiKey
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("content-type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini API error: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("gemini API %d: %s", resp.StatusCode, string(respBody))
	}

	var r geminiResponse
	if err := json.Unmarshal(respBody, &r); err != nil {
		return "", fmt.Errorf("gemini parse error: %w", err)
	}
	if r.Error != nil {
		return "", fmt.Errorf("gemini error %d: %s", r.Error.Code, r.Error.Message)
	}
	if len(r.Candidates) == 0 || len(r.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini returned empty response")
	}
	return r.Candidates[0].Content.Parts[0].Text, nil
}
