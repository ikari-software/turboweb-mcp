package main

import (
	"context"
	"math/rand"
)

// DefaultTypeWPM is the cadence cdp_type uses when humanized typing is on and
// no wpm is supplied — brisk but human, ~110 words/min.
const DefaultTypeWPM = 110

// humanKeyActions builds a BiDi key-action sequence that types `text` at a
// human cadence: each key is held for a short dwell, and the gap before the
// next key derives from wpm with jitter plus longer pauses after spaces and
// punctuation. `rnd` returns a value in [0,1) and is injectable for tests.
//
// Timing is expressed as BiDi `pause` actions, so the BROWSER honors the
// durations as it replays the sequence — we do not sleep the server. baseMs is
// 60000/(wpm*5): one "word" ≈ 5 chars, so wpm*5 chars/min.
func humanKeyActions(text string, wpm int, rnd func() float64) []map[string]any {
	if wpm <= 0 {
		wpm = DefaultTypeWPM
	}
	baseMs := 60000.0 / float64(wpm*5)
	runes := []rune(text)
	actions := make([]map[string]any, 0, len(runes)*4)
	for i, ch := range runes {
		s := string(ch)
		dwell := int(30 + rnd()*50) // key held 30–80ms
		actions = append(actions,
			map[string]any{"type": "keyDown", "value": s},
			map[string]any{"type": "pause", "duration": dwell},
			map[string]any{"type": "keyUp", "value": s},
		)
		if i == len(runes)-1 {
			break // no trailing gap after the final key
		}
		gap := baseMs * (0.6 + rnd()) // 0.6×–1.6× jitter
		switch ch {
		case ' ', '\t', '\n':
			gap *= 1.8 // word boundary
		case '.', ',', '!', '?', ';', ':':
			gap *= 2.2 // clause/sentence boundary
		}
		if gap < 20 {
			gap = 20
		} else if gap > 1500 {
			gap = 1500
		}
		actions = append(actions, map[string]any{"type": "pause", "duration": int(gap)})
	}
	return actions
}

// bidiTypeHuman types `text` with human cadence via a single performActions
// call carrying the paused key sequence.
func bidiTypeHuman(ctx context.Context, contextID, text string, wpm int) error {
	return bidiKeyboardRawActions(ctx, contextID, humanKeyActions(text, wpm, rand.Float64))
}
