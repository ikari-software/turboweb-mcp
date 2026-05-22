package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// --- normalizeKey ---

func TestNormalizeKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"taskQueue", "taskqueue"},
		{"task_queue", "taskqueue"},
		{"task-queue", "taskqueue"},
		{"Task Queue", "taskqueue"},
		{"workflowId", "workflowid"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeKey(c.in); got != c.want {
			t.Errorf("normalizeKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- isSensitiveField ---

func TestIsSensitiveField(t *testing.T) {
	cases := []struct {
		name string
		f    formField
		want bool
	}{
		{"password type", formField{Type: "password", Name: "pw"}, true},
		{"token by name", formField{Type: "text", Name: "apiToken"}, true},
		{"secret by id", formField{Type: "text", ID: "client_secret"}, true},
		{"api_key by name", formField{Type: "text", Name: "api_key"}, true},
		{"password by label", formField{Type: "text", Label: "Your Password"}, true},
		{"plain text field", formField{Type: "text", Name: "workflowId"}, false},
		{"plain email", formField{Type: "email", Name: "email"}, false},
	}
	for _, c := range cases {
		if got := isSensitiveField(c.f); got != c.want {
			t.Errorf("%s: isSensitiveField = %v, want %v", c.name, got, c.want)
		}
	}
}

// --- mapField: the heuristic cascade ---

func TestMapField(t *testing.T) {
	fields := []formField{
		{Tag: "input", Type: "text", Name: "workflowId", ID: "wf-id", Selector: "#wf-id"},
		{Tag: "input", Type: "text", Name: "task_queue", ID: "tq", Selector: "#tq"},
		{Tag: "input", Type: "text", Name: "", ID: "", Label: "Workflow Type", Selector: "input.wftype"},
		{Tag: "input", Type: "password", Name: "authToken", ID: "tok", Selector: "#tok"},
		{Tag: "input", Type: "text", Name: "namespace", ID: "ns", Selector: "#ns"},
	}

	cases := []struct {
		name       string
		key        string
		actionPar  []string
		known      []string
		wantParam  string
		wantRule   string
		wantConf   mapConfidence
		wantNoSel  bool // expect Selector == "" (no field matched)
		wantReason string
	}{
		{
			name:      "exact name match",
			key:       "workflowId",
			wantParam: "workflowId", wantRule: "exact", wantConf: confHigh,
		},
		{
			name:      "normalized match taskQueue->task_queue",
			key:       "taskQueue",
			wantParam: "task_queue", wantRule: "normalized", wantConf: confHigh,
		},
		{
			name:      "action-param introspection wins",
			key:       "namespace",
			actionPar: []string{"namespace"},
			wantParam: "namespace", wantRule: "action-param", wantConf: confHigh,
		},
		{
			name:       "label match with no name/id is unmapped",
			key:        "Workflow Type",
			wantRule:   "label",
			wantParam:  "",
			wantReason: "field has no name/id",
		},
		{
			name:       "sensitive field refused into URL",
			key:        "authToken",
			wantParam:  "",
			wantReason: "sensitive field",
		},
		{
			name:       "unmatched key — no field on page",
			key:        "nonexistent",
			wantParam:  "",
			wantNoSel:  true,
			wantReason: "no form field matched",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := mapField(c.key, "val", fields, c.actionPar, c.known)
			if m.Param != c.wantParam {
				t.Errorf("Param = %q, want %q", m.Param, c.wantParam)
			}
			if c.wantRule != "" && m.Rule != c.wantRule {
				t.Errorf("Rule = %q, want %q", m.Rule, c.wantRule)
			}
			if c.wantConf != "" && m.Confidence != c.wantConf {
				t.Errorf("Confidence = %q, want %q", m.Confidence, c.wantConf)
			}
			if c.wantNoSel && m.Selector != "" {
				t.Errorf("Selector = %q, want empty", m.Selector)
			}
			if c.wantReason != "" && !strings.Contains(m.Reason, c.wantReason) {
				t.Errorf("Reason = %q, want substring %q", m.Reason, c.wantReason)
			}
		})
	}
}

// A label-only field whose name IS present and corroborated by the known
// table is promoted to high confidence; uncorroborated stays low and routes
// to manualFields.
func TestMapField_LabelKnownTable(t *testing.T) {
	fields := []formField{
		{Tag: "input", Type: "text", Name: "title", Label: "Issue Title", Selector: "#t"},
	}
	// Label match, name "title" corroborated by known table -> high/known.
	m := mapField("Issue Title", "x", fields, nil, []string{"title", "body"})
	if m.Param != "title" || m.Rule != "known" || m.Confidence != confHigh {
		t.Errorf("corroborated label: got param=%q rule=%q conf=%q", m.Param, m.Rule, m.Confidence)
	}
	// Label match, name NOT in known table -> low confidence, no param.
	m2 := mapField("Issue Title", "x", fields, nil, nil)
	if m2.Param != "" || m2.Confidence != confLow {
		t.Errorf("uncorroborated label: got param=%q conf=%q", m2.Param, m2.Confidence)
	}
}

// --- buildPrefillURL: URL construction ---

func TestBuildPrefillURL(t *testing.T) {
	cases := []struct {
		name     string
		base     string
		mappings []fieldMapping
		want     string
	}{
		{
			name: "simple two params",
			base: "https://temporal.example/start-workflow",
			mappings: []fieldMapping{
				{Name: "workflowId", Value: "order-42", Param: "workflowId"},
				{Name: "taskQueue", Value: "orders", Param: "taskQueue"},
			},
			want: "https://temporal.example/start-workflow?taskQueue=orders&workflowId=order-42",
		},
		{
			name: "preserves existing query params",
			base: "https://app.example/new?source=cli",
			mappings: []fieldMapping{
				{Name: "title", Value: "Bug", Param: "title"},
			},
			want: "https://app.example/new?source=cli&title=Bug",
		},
		{
			name: "colliding key overridden by formData",
			base: "https://app.example/new?title=old",
			mappings: []fieldMapping{
				{Name: "title", Value: "new value", Param: "title"},
			},
			want: "https://app.example/new?title=new+value",
		},
		{
			name: "value needing escaping",
			base: "https://app.example/new",
			mappings: []fieldMapping{
				{Name: "q", Value: "a&b=c d", Param: "q"},
			},
			want: "https://app.example/new?q=a%26b%3Dc+d",
		},
		{
			name: "unmapped field contributes nothing",
			base: "https://app.example/new",
			mappings: []fieldMapping{
				{Name: "title", Value: "X", Param: "title"},
				{Name: "other", Value: "Y", Param: ""},
			},
			want: "https://app.example/new?title=X",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := buildPrefillURL(c.base, c.mappings)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got != c.want {
				t.Errorf("buildPrefillURL = %q, want %q", got, c.want)
			}
		})
	}
}

// An oversized value is demoted out of the URL with a "value too long" reason.
func TestBuildPrefillURL_LengthCap(t *testing.T) {
	huge := strings.Repeat("x", maxURLLen+100)
	mappings := []fieldMapping{
		{Name: "body", Value: huge, Param: "body"},
		{Name: "title", Value: "ok", Param: "title"},
	}
	got, out, err := buildPrefillURL("https://app.example/new", mappings)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(got) > maxURLLen {
		t.Errorf("URL length %d exceeds cap %d", len(got), maxURLLen)
	}
	if !strings.Contains(got, "title=ok") {
		t.Errorf("short field should still be in URL: %q", got)
	}
	if strings.Contains(got, "body=") {
		t.Errorf("oversized field should be demoted: %q", got)
	}
	// The demoted mapping has Param cleared and a reason set.
	var bodyMapping fieldMapping
	for _, m := range out {
		if m.Name == "body" {
			bodyMapping = m
		}
	}
	if bodyMapping.Param != "" || !strings.Contains(bodyMapping.Reason, "too long") {
		t.Errorf("body mapping not demoted: %+v", bodyMapping)
	}
}

// --- collectActionParams ---

func TestCollectActionParams(t *testing.T) {
	got := collectActionParams(
		"/submit?foo=1&bar=2",
		"https://app.example/page?baz=3",
		"https://app.example/page?foo=9",
	)
	want := map[string]bool{"foo": true, "bar": true, "baz": true}
	if len(got) != len(want) {
		t.Fatalf("collectActionParams = %v, want keys %v", got, want)
	}
	for _, k := range got {
		if !want[k] {
			t.Errorf("unexpected param %q", k)
		}
	}
}

// --- verifyField: post-navigation verification ---

func TestVerifyField(t *testing.T) {
	post := []formField{
		{Selector: "#a", Value: "order-42"},
		{Selector: "#b", Value: "  orders  "}, // whitespace differs
		{Selector: "#c", Value: ""},           // app dropped the value
		{Selector: "#d", Value: "transformed"},
	}
	cases := []struct {
		name       string
		intended   string
		sel        string
		wantStatus string
	}{
		{"exact match -> verified", "order-42", "#a", "verified"},
		{"whitespace lenient -> verified", "orders", "#b", "verified"},
		{"empty field -> mismatch", "wanted", "#c", "mismatch"},
		{"different value -> mismatch", "wanted", "#d", "mismatch"},
		{"field gone -> missing", "x", "#gone", "missing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, _ := verifyField(c.intended, post, c.sel)
			if status != c.wantStatus {
				t.Errorf("verifyField = %q, want %q", status, c.wantStatus)
			}
		})
	}
}

// --- redirected ---

func TestRedirected(t *testing.T) {
	cases := []struct {
		intended, actual string
		want             bool
	}{
		{"https://app.example/new?q=1", "https://app.example/new", false},
		{"https://app.example/new", "https://app.example/login", true},
		{"https://app.example/new", "https://other.example/new", true},
		{"https://app.example/new", "", false},
	}
	for _, c := range cases {
		if got := redirected(c.intended, c.actual); got != c.want {
			t.Errorf("redirected(%q,%q) = %v, want %v", c.intended, c.actual, got, c.want)
		}
	}
}

// --- buildPrefillResult: result assembly ---

func TestBuildPrefillResult_Verify(t *testing.T) {
	mappings := []fieldMapping{
		{Name: "workflowId", Value: "order-42", Param: "workflowId", Selector: "#a", Confidence: confHigh},
		{Name: "taskQueue", Value: "orders", Param: "taskQueue", Selector: "#b", Confidence: confHigh},
		{Name: "workflowType", Value: "OrderWorkflow", Param: "", Selector: "#c", Reason: "no URL param discovered for this field"},
	}
	post := []formField{
		{Selector: "#a", Value: "order-42"},
		{Selector: "#b", Value: "orders"},
		{Selector: "#c", Value: ""},
	}
	res := buildPrefillResult(true, true, "https://x/", mappings, post, false)

	if res["prefilled"] != true || res["navigated"] != true {
		t.Errorf("flags wrong: %+v", res)
	}
	manual := res["manualFields"].([]map[string]any)
	if len(manual) != 1 || manual[0]["name"] != "workflowType" {
		t.Errorf("manualFields = %+v, want 1 (workflowType)", manual)
	}
	summary := res["summary"].(string)
	if !strings.Contains(summary, "2/3") {
		t.Errorf("summary = %q, want 2/3", summary)
	}
	fields := res["fields"].([]map[string]any)
	if len(fields) != 3 {
		t.Fatalf("want 3 fields, got %d", len(fields))
	}
}

func TestBuildPrefillResult_DryRun(t *testing.T) {
	mappings := []fieldMapping{
		{Name: "title", Value: "Bug", Param: "title", Selector: "#t", Confidence: confHigh},
		{Name: "ghost", Value: "X", Param: "", Selector: "", Reason: "no form field matched this key"},
	}
	res := buildPrefillResult(false, false, "https://x/?title=Bug", mappings, nil, true)

	if res["prefilled"] != false || res["navigated"] != false {
		t.Errorf("dry-run flags wrong: %+v", res)
	}
	fields := res["fields"].([]map[string]any)
	if fields[0]["status"] != "planned" {
		t.Errorf("mapped field status = %v, want planned", fields[0]["status"])
	}
	if fields[1]["status"] != "unmapped" {
		t.Errorf("ghost field status = %v, want unmapped", fields[1]["status"])
	}
	manual := res["manualFields"].([]map[string]any)
	if len(manual) != 1 || manual[0]["selector"] != nil {
		t.Errorf("manualFields = %+v, want 1 with nil selector", manual)
	}
}

// --- handler integration: dry run via mock browser ---

func TestHandleTryURLPrefill_DryRun(t *testing.T) {
	descriptor := map[string]any{
		"form": map[string]any{
			"action": "", "method": "get", "selector": "form", "location": "https://temporal.example/start-workflow",
		},
		"fields": []map[string]any{
			{"tag": "input", "type": "text", "name": "workflowId", "id": "wf", "selector": "#wf"},
			{"tag": "input", "type": "text", "name": "taskQueue", "id": "tq", "selector": "#tq"},
			{"tag": "input", "type": "text", "name": "extra", "id": "ex", "selector": "#ex"},
		},
	}
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		action, _ := msg["action"].(string)
		if action == "inspect_form" {
			return map[string]any{"result": descriptor}
		}
		return map[string]any{"result": map[string]any{}}
	}, func() {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
			"url": "https://temporal.example/start-workflow",
			"formData": map[string]any{
				"workflowId": "order-42",
				"taskQueue":  "orders",
			},
			"apply": false,
		}}}
		result, err := handleTryURLPrefill(context.Background(), req)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		text := extractText(t, result)
		var resp map[string]any
		if err := json.Unmarshal([]byte(text), &resp); err != nil {
			t.Fatalf("bad JSON: %v\n%s", err, text)
		}
		if resp["navigated"] != false {
			t.Errorf("dry run should not navigate: %+v", resp)
		}
		u, _ := resp["url"].(string)
		if !strings.Contains(u, "workflowId=order-42") || !strings.Contains(u, "taskQueue=orders") {
			t.Errorf("planned URL missing params: %q", u)
		}
	})
}

// --- handler integration: full apply path with verification ---

func TestHandleTryURLPrefill_Apply(t *testing.T) {
	preNav := map[string]any{
		"form": map[string]any{
			"action": "", "method": "get", "selector": "form", "location": "https://app.example/new",
		},
		"fields": []map[string]any{
			{"tag": "input", "type": "text", "name": "title", "id": "t", "selector": "#t"},
			{"tag": "input", "type": "text", "name": "body", "id": "b", "selector": "#b"},
		},
	}
	// Post-navigation: title got prefilled, body stayed empty.
	postNav := map[string]any{
		"form": map[string]any{
			"action": "", "method": "get", "selector": "form", "location": "https://app.example/new?title=Hello",
		},
		"fields": []map[string]any{
			{"tag": "input", "type": "text", "name": "title", "id": "t", "selector": "#t", "value": "Hello"},
			{"tag": "input", "type": "text", "name": "body", "id": "b", "selector": "#b", "value": ""},
		},
	}
	inspectCalls := 0
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		action, _ := msg["action"].(string)
		switch action {
		case "inspect_form":
			inspectCalls++
			if inspectCalls == 1 {
				return map[string]any{"result": preNav}
			}
			return map[string]any{"result": postNav}
		case "navigate":
			return map[string]any{"result": map[string]any{"url": "https://app.example/new?title=Hello"}}
		}
		return map[string]any{"result": map[string]any{}}
	}, func() {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
			"url": "https://app.example/new",
			"formData": map[string]any{
				"title": "Hello",
				"body":  "Some body text",
			},
			"settleMs": float64(0),
		}}}
		result, err := handleTryURLPrefill(context.Background(), req)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		text := extractText(t, result)
		var resp map[string]any
		if err := json.Unmarshal([]byte(text), &resp); err != nil {
			t.Fatalf("bad JSON: %v\n%s", err, text)
		}
		if resp["prefilled"] != true || resp["navigated"] != true {
			t.Errorf("expected prefilled+navigated: %+v", resp)
		}
		manual := resp["manualFields"].([]any)
		if len(manual) != 1 {
			t.Fatalf("want 1 manual field (body), got %d: %+v", len(manual), manual)
		}
		mf := manual[0].(map[string]any)
		if mf["name"] != "body" {
			t.Errorf("manual field = %v, want body", mf["name"])
		}
		summary, _ := resp["summary"].(string)
		if !strings.Contains(summary, "1/2") {
			t.Errorf("summary = %q, want 1/2", summary)
		}
	})
}

func TestHandleTryURLPrefill_NoFormData(t *testing.T) {
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"url": "https://app.example/new",
	}}}
	result, err := handleTryURLPrefill(context.Background(), req)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	text := extractText(t, result)
	if !strings.Contains(text, "formData is required") {
		t.Errorf("expected formData-required error: %q", text)
	}
}

func TestHandleTryURLPrefill_RejectsChromeURL(t *testing.T) {
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"url":      "chrome://settings",
		"formData": map[string]any{"x": "y"},
	}}}
	result, err := handleTryURLPrefill(context.Background(), req)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	text := extractText(t, result)
	if !strings.Contains(text, "chrome:") {
		t.Errorf("expected chrome: rejection: %q", text)
	}
}
