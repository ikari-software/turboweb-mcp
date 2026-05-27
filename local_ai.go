package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// localAsk sends a question + context to Chrome's built-in Gemini Nano via
// the extension service worker (the `__ask_local` action). The extension
// returns the model's reply over the existing WS bridge.
//
// Returns the answer text, or an error whose message starts with
// LOCAL_AI_ when local AI isn't usable (model not downloaded, browser
// doesn't expose the LanguageModel API, etc.). Callers can use that
// prefix to decide whether to fall back to raw data.
func localAsk(question, context, systemPrompt string) (string, error) {
	raw, err := send("__ask_local", map[string]any{
		"question":     question,
		"context":      context,
		"systemPrompt": systemPrompt,
	}, 60000)
	if err != nil {
		// Surface the daemon/relay error verbatim. Most commonly this
		// is "No browser extensions connected" — caller will fall back.
		return "", fmt.Errorf("local AI bridge: %w", err)
	}
	var r struct {
		Answer  string `json:"answer"`
		Backend string `json:"backend"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("local AI parse: %w", err)
	}
	if r.Answer == "" {
		return "", errors.New("local AI returned empty answer")
	}
	return r.Answer, nil
}

// aiBackend returns the AI-backend preference read from the
// TURBOWEB_AI_BACKEND env var. One of:
//
//   - "auto" (default): Haiku → Gemini → local Gemini Nano → raw.
//     Uses whichever keys are present; silent raw when none are configured.
//   - "haiku": only use Haiku (Anthropic API). Set ANTHROPIC_API_KEY.
//   - "gemini": only use Google Gemini. Set GEMINI_API_KEY.
//   - "local": only use Chrome's built-in Gemini Nano. Returns raw if the
//     browser doesn't expose LanguageModel or the model isn't downloaded.
//   - "none": never post-process — always return raw data.
func aiBackend() string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("TURBOWEB_AI_BACKEND")))
	if v == "" {
		return "auto"
	}
	return v
}
