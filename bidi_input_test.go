package main

import "testing"

// keyChordActions must order events modifiers-down → key-down → key-up →
// modifiers-up (reverse), so the browser fires the native shortcut.
func TestKeyChordActionsOrdering(t *testing.T) {
	acts := keyChordActions("a", []string{"Meta"})
	if len(acts) != 4 {
		t.Fatalf("expected 4 actions for Meta+a, got %d: %v", len(acts), acts)
	}
	meta := keyToUnicode("Meta")
	want := []struct {
		typ, val string
	}{
		{"keyDown", meta},
		{"keyDown", "a"},
		{"keyUp", "a"},
		{"keyUp", meta},
	}
	for i, w := range want {
		if acts[i]["type"] != w.typ || acts[i]["value"] != w.val {
			t.Errorf("action %d = {%v,%v}, want {%s,%s}", i, acts[i]["type"], acts[i]["value"], w.typ, w.val)
		}
	}
}

func TestKeyChordActionsNoModifiers(t *testing.T) {
	acts := keyChordActions("Enter", nil)
	if len(acts) != 2 || acts[0]["type"] != "keyDown" || acts[1]["type"] != "keyUp" {
		t.Fatalf("plain key press should be down+up, got %v", acts)
	}
	if acts[0]["value"] != keyToUnicode("Enter") {
		t.Errorf("Enter not mapped to its BiDi unicode value: %v", acts[0]["value"])
	}
}

func TestKeyChordActionsMultipleModifiers(t *testing.T) {
	acts := keyChordActions("Tab", []string{"Control", "Shift"})
	// Control down, Shift down, Tab down, Tab up, Shift up, Control up.
	if len(acts) != 6 {
		t.Fatalf("expected 6 actions, got %d: %v", len(acts), acts)
	}
	if acts[0]["value"] != keyToUnicode("Control") || acts[5]["value"] != keyToUnicode("Control") {
		t.Errorf("modifiers must release in reverse order (Control outermost): %v", acts)
	}
	if acts[1]["value"] != keyToUnicode("Shift") || acts[4]["value"] != keyToUnicode("Shift") {
		t.Errorf("Shift must be the inner modifier: %v", acts)
	}
}

// pointerClickActions: a triple-click is one move + three down/up pairs.
func TestPointerClickActionsTripleClick(t *testing.T) {
	acts := pointerClickActions(10, 20, "left", 3)
	if len(acts) != 1+3*2 {
		t.Fatalf("triple-click should be 1 move + 6 down/up, got %d: %v", len(acts), acts)
	}
	if acts[0]["type"] != "pointerMove" || acts[0]["x"] != 10 || acts[0]["y"] != 20 {
		t.Errorf("first action must be a pointerMove to the target: %v", acts[0])
	}
	downs, ups := 0, 0
	for _, a := range acts[1:] {
		switch a["type"] {
		case "pointerDown":
			downs++
		case "pointerUp":
			ups++
		}
	}
	if downs != 3 || ups != 3 {
		t.Errorf("expected 3 down + 3 up, got %d down %d up", downs, ups)
	}
}

func TestPointerClickActionsCountFloor(t *testing.T) {
	acts := pointerClickActions(0, 0, "", 0)
	if len(acts) != 3 { // move + one down/up
		t.Fatalf("count<1 should floor to a single click (3 actions), got %d", len(acts))
	}
}

func TestToStringSlice(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"nil", nil, nil},
		{"json array", []any{"Meta", "Shift"}, []string{"Meta", "Shift"}},
		{"drops empties", []any{"Meta", "", nil}, []string{"Meta"}},
		{"single string", "Control", []string{"Control"}},
		{"empty string", "", nil},
		{"native slice", []string{"Alt"}, []string{"Alt"}},
		{"wrong type", 42, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toStringSlice(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("toStringSlice(%v) = %v, want %v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("index %d = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// humanKeyActions builds keyDown/pause/keyUp + inter-key pause per char.
func TestHumanKeyActionsStructure(t *testing.T) {
	half := func() float64 { return 0.5 }
	acts := humanKeyActions("ab", 110, half)
	// per char: keyDown,pause(dwell),keyUp ; plus a gap pause after all but last.
	// "ab" → [kd a, p, ku a, p(gap), kd b, p, ku b] = 7
	if len(acts) != 7 {
		t.Fatalf("expected 7 actions, got %d: %v", len(acts), acts)
	}
	if acts[0]["type"] != "keyDown" || acts[0]["value"] != "a" {
		t.Errorf("action 0 should be keyDown a: %v", acts[0])
	}
	dwell := int(30 + 0.5*50) // 55
	if acts[1]["type"] != "pause" || acts[1]["duration"] != dwell {
		t.Errorf("action 1 should be dwell pause %d: %v", dwell, acts[1])
	}
	if acts[2]["type"] != "keyUp" || acts[2]["value"] != "a" {
		t.Errorf("action 2 should be keyUp a: %v", acts[2])
	}
	base := 60000.0 / (110 * 5)
	gap := int(base * (0.6 + 0.5)) // 1.1×
	if acts[3]["type"] != "pause" || acts[3]["duration"] != gap {
		t.Errorf("action 3 should be inter-key gap %d: %v", gap, acts[3])
	}
	// No trailing gap after the final key.
	if acts[len(acts)-1]["type"] != "keyUp" || acts[len(acts)-1]["value"] != "b" {
		t.Errorf("last action should be keyUp b (no trailing pause): %v", acts[len(acts)-1])
	}
}

func TestHumanKeyActionsSpaceAndPunctPauseLonger(t *testing.T) {
	half := func() float64 { return 0.5 }
	base := 60000.0 / (110 * 5)
	letterGap := int(base * 1.1)
	// gap pause is index 3 for a 2-char input.
	if g := humanKeyActions(" x", 110, half)[3]["duration"].(int); g != int(base*1.1*1.8) {
		t.Errorf("space gap %d, want %d", g, int(base*1.1*1.8))
	}
	if g := humanKeyActions(".x", 110, half)[3]["duration"].(int); g != int(base*1.1*2.2) {
		t.Errorf("punct gap %d, want %d", g, int(base*1.1*2.2))
	}
	if humanKeyActions("xx", 110, half)[3]["duration"].(int) != letterGap {
		t.Errorf("plain letter gap should be the base jittered value")
	}
}

func TestHumanKeyActionsDefaultsWPM(t *testing.T) {
	half := func() float64 { return 0.5 }
	// wpm<=0 should fall back to DefaultTypeWPM, not divide-by-zero.
	a := humanKeyActions("ab", 0, half)
	b := humanKeyActions("ab", DefaultTypeWPM, half)
	if a[3]["duration"] != b[3]["duration"] {
		t.Errorf("wpm=0 should default to %d: got gap %v vs %v", DefaultTypeWPM, a[3]["duration"], b[3]["duration"])
	}
}

func TestHumanKeyActionsClampsGap(t *testing.T) {
	zero := func() float64 { return 0 }
	// wpm=1 → base 12000ms × 0.6 = 7200ms → clamped to 1500.
	if g := humanKeyActions("ab", 1, zero)[3]["duration"].(int); g != 1500 {
		t.Errorf("slow wpm gap should clamp to 1500, got %d", g)
	}
	// huge wpm → tiny gap → floored at 20.
	if g := humanKeyActions("ab", 100000, zero)[3]["duration"].(int); g != 20 {
		t.Errorf("fast wpm gap should clamp to 20, got %d", g)
	}
}
