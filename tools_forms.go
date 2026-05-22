package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerFormTools registers form-oriented meta-tools. Currently just
// try_url_prefill — a Go-side orchestrator over the inspect_form +
// navigate extension actions. See design/try_url_prefill.md.
func registerFormTools(s *server.MCPServer) {
	// --- try_url_prefill ---
	addTool(s,
		mcp.NewTool("try_url_prefill",
			mcp.WithDescription("Prefill a form by constructing a URL with query parameters, "+
				"navigating to it, and verifying which fields were populated. Many web apps "+
				"(Temporal, Jira, GitHub issue creators) prefill required fields from URL params "+
				"— this collapses a multi-step click+type sequence into one navigation. Returns "+
				"per-field success and which fields still need manual filling."),
			mcp.WithString("url", mcp.Required(), mcp.Description(
				"Base URL of the page hosting the form, WITHOUT the prefill params "+
					"(existing params are preserved).")),
			mcp.WithObject("formData", mcp.Required(), mcp.Description(
				"Logical field name -> desired string value, "+
					"e.g. {\"workflowId\":\"order-42\",\"taskQueue\":\"orders\"}. "+
					"Keys are matched against the form's inputs.")),
			mcp.WithString("formSelector", mcp.Description(
				"CSS selector to disambiguate when the page has multiple forms. "+
					"Omit to use the first/largest form.")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithBoolean("apply", mcp.Description(
				"If false, do NOT navigate — return the planned URL and mapping only "+
					"(dry run). Default true.")),
			mcp.WithNumber("settleMs", mcp.Description(
				"How long to wait after navigation before verifying, to let SPAs "+
					"hydrate. Default 1200, capped at 5000.")),
		),
		handleTryURLPrefill,
	)
}

// --- form descriptor types (mirror the inspect_form extension payload) ---

// formField is one input/select/textarea/contenteditable descriptor as
// returned by the inspect_form content-script handler.
type formField struct {
	Tag         string `json:"tag"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	ID          string `json:"id"`
	Placeholder string `json:"placeholder"`
	Label       string `json:"label"`
	AriaLabel   string `json:"ariaLabel"`
	Required    bool   `json:"required"`
	Disabled    bool   `json:"disabled"`
	Value       string `json:"value"`
	Visible     bool   `json:"visible"`
	// FrameworkControlled flags inputs a framework owns (React/Vue/...).
	FrameworkControlled bool `json:"frameworkControlled"`
	// Selector is a CSS selector that uniquely targets the field, so the
	// agent can feed an unfilled field straight into type_text / cdp_type.
	Selector string `json:"selector"`
}

// formDescriptor is the inspect_form result: the chosen form plus its fields.
type formDescriptor struct {
	Form struct {
		Action   string `json:"action"`
		Method   string `json:"method"`
		Selector string `json:"selector"`
		// Location is the page URL at inspection time. Used post-navigation
		// to detect a server redirect away from the intended path.
		Location string `json:"location"`
	} `json:"form"`
	Fields []formField `json:"fields"`
}

// --- field -> URL-param mapping ---

// mapConfidence is how much the heuristic cascade trusts a mapping. Only
// high-confidence mappings are written into the URL by default; low ones
// are routed to manualFields (the agent fills them by hand).
type mapConfidence string

const (
	confHigh mapConfidence = "high"
	confLow  mapConfidence = "low"
)

// fieldMapping is the result of mapping one formData key onto the form.
type fieldMapping struct {
	// Name is the logical formData key the agent supplied.
	Name string
	// Value is the desired value for that field.
	Value string
	// Param is the URL query-parameter name to carry the value, or "" when
	// no param could be discovered (unmapped) or the value is being routed
	// to manualFields.
	Param string
	// Selector targets the matched field for manual filling; "" when the
	// formData key matched no field on the page at all.
	Selector string
	// Confidence is high for name/id/normalized matches, low for label/
	// known-pattern matches.
	Confidence mapConfidence
	// Rule is the cascade rule that produced the match (for debugging /
	// tests): "action-param", "exact", "normalized", "label", "known".
	Rule string
	// Reason explains why a key was routed to manualFields rather than the
	// URL (no field, sensitive, value too long, low confidence).
	Reason string
}

// maxURLLen is a conservative cap on the constructed URL length. Values
// whose inclusion would push past it are demoted to manualFields rather
// than producing a malformed/truncated URL (older browsers/servers reject
// URLs well past ~2000 chars).
const maxURLLen = 2000

// normalizeKey lowercases and strips -, _ and spaces so taskQueue, task_queue
// and task-queue all compare equal.
func normalizeKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r == '-' || r == '_' || r == ' ' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sensitiveNameHints are name/id substrings that, alongside type=password,
// mark a field whose value must never be placed in a URL (it would leak into
// browser history and the visible address bar).
var sensitiveNameHints = []string{"password", "passwd", "secret", "token", "apikey", "api_key", "otp", "cvv", "ssn", "creditcard", "cardnumber"}

// isSensitiveField reports whether a field's value is too sensitive to put in
// a URL — a password-type input, or a name/id/label hinting at a credential.
func isSensitiveField(f formField) bool {
	if strings.EqualFold(f.Type, "password") {
		return true
	}
	hay := normalizeKey(f.Name + " " + f.ID + " " + f.Label + " " + f.AriaLabel)
	for _, hint := range sensitiveNameHints {
		if strings.Contains(hay, strings.ReplaceAll(hint, "_", "")) {
			return true
		}
	}
	return false
}

// knownPrefillPattern is one entry of the well-known-apps fallback table: if
// the page host contains HostSubstr and the path contains PathSubstr, the
// listed param names are known to be URL-addressable on that app.
type knownPrefillPattern struct {
	HostSubstr string
	PathSubstr string
	Params     []string
}

// knownPrefillPatterns is a small, cheap-to-extend fallback table (cascade
// rule 5). It is only a tie-breaker — the name/id heuristics carry the load.
var knownPrefillPatterns = []knownPrefillPattern{
	{HostSubstr: "temporal", PathSubstr: "/start-workflow", Params: []string{"workflowId", "taskQueue", "workflowType"}},
	{HostSubstr: "github.com", PathSubstr: "/issues/new", Params: []string{"title", "body", "labels", "assignees", "milestone", "template"}},
}

// knownParamsFor returns the param names the known-pattern table associates
// with pageURL, or nil when no entry matches.
func knownParamsFor(pageURL string) []string {
	u, err := url.Parse(pageURL)
	if err != nil {
		return nil
	}
	host := strings.ToLower(u.Host)
	path := strings.ToLower(u.Path)
	for _, p := range knownPrefillPatterns {
		if strings.Contains(host, p.HostSubstr) && strings.Contains(path, p.PathSubstr) {
			return p.Params
		}
	}
	return nil
}

// paramName returns the URL-param name a server would read for a field —
// its `name`, falling back to its `id`.
func paramName(f formField) string {
	if f.Name != "" {
		return f.Name
	}
	return f.ID
}

// mapField runs the §3 heuristic cascade for one formData key against the
// form's fields. actionParams are param names harvested from the form action
// / page URL (cascade rule 1); known are names from the known-pattern table.
// Returns the chosen mapping; an unmatched key yields Selector=="" and a
// reason, an unmapped field yields Param=="".
func mapField(key, value string, fields []formField, actionParams, known []string) fieldMapping {
	m := fieldMapping{Name: key, Value: value}
	nKey := normalizeKey(key)

	// Locate the candidate field by exact name/id, then normalized match.
	var (
		field *formField
		rule  string
	)
	for i := range fields {
		f := &fields[i]
		if f.Name == key || f.ID == key {
			field, rule = f, "exact"
			break
		}
	}
	if field == nil {
		for i := range fields {
			f := &fields[i]
			if normalizeKey(f.Name) == nKey || normalizeKey(f.ID) == nKey {
				field, rule = f, "normalized"
				break
			}
		}
	}
	if field == nil {
		// Rule 4: label / placeholder / aria-label fuzzy match.
		for i := range fields {
			f := &fields[i]
			if f.Label == "" && f.Placeholder == "" && f.AriaLabel == "" {
				continue
			}
			if normalizeKey(f.Label) == nKey ||
				normalizeKey(f.Placeholder) == nKey ||
				normalizeKey(f.AriaLabel) == nKey {
				field, rule = f, "label"
				break
			}
		}
	}

	// No field on the page matched this formData key at all.
	if field == nil {
		m.Reason = "no form field matched this key"
		return m
	}

	m.Selector = field.Selector

	// Security: never place a sensitive value in a URL.
	if isSensitiveField(*field) {
		m.Reason = "sensitive field — not placed in URL"
		return m
	}

	pName := paramName(*field)
	if pName == "" {
		// Field exists but has no name/id — can't be URL-addressed.
		m.Rule = rule
		m.Reason = "field has no name/id to map to a URL param"
		return m
	}

	// Rule 1 (highest): the form action / page URL already carries this
	// param name — ground truth for what the server accepts.
	for _, ap := range actionParams {
		if ap == pName || normalizeKey(ap) == normalizeKey(pName) {
			m.Param, m.Rule, m.Confidence = pName, "action-param", confHigh
			return m
		}
	}

	switch rule {
	case "exact", "normalized":
		// Rules 2 & 3: trusted enough to write into the URL.
		m.Param, m.Rule, m.Confidence = pName, rule, confHigh
		return m
	case "label":
		// Rule 4: low confidence. Promote to high only if the known-pattern
		// table (rule 5) corroborates the param name; otherwise route to
		// manualFields rather than guessing a URL param.
		for _, k := range known {
			if k == pName || normalizeKey(k) == normalizeKey(pName) {
				m.Param, m.Rule, m.Confidence = pName, "known", confHigh
				return m
			}
		}
		m.Rule, m.Confidence = "label", confLow
		m.Reason = "low-confidence label match — fill manually rather than guess a URL param"
		return m
	}

	m.Reason = "no URL param discovered for this field"
	return m
}

// buildPrefillURL merges the high-confidence param mappings into base's query
// string and returns the encoded URL. Existing query params are preserved;
// colliding keys are overridden by the prefill values. Mappings whose
// inclusion would exceed maxURLLen are demoted: the function flips their Param
// to "" and sets a reason, so the caller routes them to manualFields.
func buildPrefillURL(base string, mappings []fieldMapping) (string, []fieldMapping, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", mappings, fmt.Errorf("invalid url: %w", err)
	}
	q := u.Query()
	out := make([]fieldMapping, len(mappings))
	copy(out, mappings)
	for i := range out {
		m := &out[i]
		if m.Param == "" {
			continue
		}
		// Tentatively add the param, then check the resulting URL length.
		prev := q.Get(m.Param)
		hadPrev := q.Has(m.Param)
		q.Set(m.Param, m.Value)
		u.RawQuery = q.Encode()
		if len(u.String()) > maxURLLen {
			// Undo and demote this field.
			if hadPrev {
				q.Set(m.Param, prev)
			} else {
				q.Del(m.Param)
			}
			u.RawQuery = q.Encode()
			m.Param = ""
			m.Reason = "value too long for URL"
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), out, nil
}

// findField returns the field in the descriptor whose selector matches sel,
// or nil if it is gone (SPA route change / conditional render).
func findField(fields []formField, sel string) *formField {
	for i := range fields {
		if fields[i].Selector == sel {
			return &fields[i]
		}
	}
	return nil
}

// normalizeValue trims surrounding whitespace and collapses internal runs so
// verification is lenient about a field's own trim/normalize behavior.
func normalizeValue(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// verifyField compares an intended value against a field's post-navigation
// value and returns the §2 status string: "verified", "mismatch" or
// "missing".
func verifyField(intended string, fields []formField, sel string) (status, actual string) {
	f := findField(fields, sel)
	if f == nil {
		return "missing", ""
	}
	if normalizeValue(f.Value) == normalizeValue(intended) {
		return "verified", f.Value
	}
	return "mismatch", f.Value
}

// handleTryURLPrefill is the Go-side orchestrator: inspect the form, map
// fields to URL params, build the URL, navigate, then re-inspect and verify.
// Failure modes return a structured prefilled:false verdict (textResult)
// rather than an MCP error — only transport failures bubble up as errors.
func handleTryURLPrefill(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	baseURL := getString(args, "url", "")
	if baseURL == "" {
		return textResult(map[string]any{"prefilled": false, "error": "url is required"})
	}
	// Reject targets the extension can't drive a form on.
	if strings.HasPrefix(baseURL, "about:") || strings.HasPrefix(baseURL, "chrome:") {
		return textResult(map[string]any{"prefilled": false, "error": "cannot prefill an about: / chrome: URL"})
	}
	formData := map[string]string{}
	if raw, ok := args["formData"].(map[string]any); ok {
		for k, v := range raw {
			formData[k] = fmt.Sprintf("%v", v)
		}
	}
	if len(formData) == 0 {
		return textResult(map[string]any{"prefilled": false, "error": "formData is required and must be a non-empty object"})
	}
	apply := getBool(args, "apply", true)
	settleMs := getInt(args, "settleMs", 1200)
	if settleMs > 5000 {
		settleMs = 5000
	}
	if settleMs < 0 {
		settleMs = 0
	}
	formSelector := getString(args, "formSelector", "")

	// Build the params common to the inspect_form calls (tabId, intent).
	inspectParams := func(withURL bool) map[string]any {
		p := rawArgs(args)
		// rawArgs preserves url/formData/etc.; trim to what inspect_form needs.
		out := map[string]any{}
		if v, ok := p["tabId"]; ok {
			out["tabId"] = v
		}
		if v, ok := p["_intent"]; ok {
			out["_intent"] = v
		}
		if formSelector != "" {
			out["formSelector"] = formSelector
		}
		if withURL {
			out["url"] = baseURL
		}
		return out
	}

	// --- Phase A: locate & inspect the form (navigate to base URL first). ---
	rawA, err := send("inspect_form", inspectParams(true), 8000)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var descA formDescriptor
	if err := json.Unmarshal(rawA, &descA); err != nil {
		return textResult(map[string]any{"prefilled": false, "error": "inspect_form returned unexpected payload: " + err.Error()})
	}
	if len(descA.Fields) == 0 {
		return textResult(map[string]any{
			"prefilled": false,
			"error":     "no form fields found on the page — nothing to prefill",
		})
	}

	// --- Build the mapping (pure Go, §3). ---
	actionParams := collectActionParams(descA.Form.Action, descA.Form.Location, baseURL)
	known := knownParamsFor(baseURL)
	var mappings []fieldMapping
	for key, val := range formData {
		mappings = append(mappings, mapField(key, val, descA.Fields, actionParams, known))
	}

	// --- Construct the URL. ---
	builtURL, mappings, err := buildPrefillURL(baseURL, mappings)
	if err != nil {
		return textResult(map[string]any{"prefilled": false, "error": err.Error()})
	}

	// --- Dry run: return the plan without navigating. ---
	if !apply {
		return textResult(buildPrefillResult(false, false, builtURL, mappings, descA.Fields, true))
	}

	// --- Navigate to the constructed URL (reuses the navigate action). ---
	navParams := map[string]any{"url": builtURL}
	if v, ok := args["tabId"]; ok && v != nil {
		navParams["tabId"] = v
	}
	if intent := extractIntent(args); intent != "" {
		navParams["_intent"] = intent
	}
	if _, err := send("navigate", navParams); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// --- Wait for the SPA to hydrate, then re-inspect & verify. ---
	if settleMs > 0 {
		time.Sleep(time.Duration(settleMs) * time.Millisecond)
	}
	rawB, err := send("inspect_form", inspectParams(false), 8000)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var descB formDescriptor
	if err := json.Unmarshal(rawB, &descB); err != nil {
		return textResult(map[string]any{"prefilled": false, "error": "post-navigation inspect_form returned unexpected payload: " + err.Error()})
	}

	// Detect a server redirect away from the intended path: the form is gone
	// and the location's path no longer matches.
	if redirected(builtURL, descB.Form.Location) && len(descB.Fields) == 0 {
		return textResult(map[string]any{
			"prefilled": false,
			"url":       builtURL,
			"navigated": true,
			"error": fmt.Sprintf("navigation redirected to %q — the form is gone (login wall, 404, or the param triggered a redirect)",
				descB.Form.Location),
		})
	}

	return textResult(buildPrefillResult(true, true, builtURL, mappings, descB.Fields, false))
}

// collectActionParams harvests query-param names from the form's action and
// from any pre-existing query on the page/base URL — cascade rule 1's ground
// truth for what the server accepts.
func collectActionParams(action, location, base string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range []string{action, location, base} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		for k := range u.Query() {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// redirected reports whether the post-navigation location landed on a
// different path than the URL we navigated to.
func redirected(intended, actual string) bool {
	if actual == "" {
		return false
	}
	iu, err1 := url.Parse(intended)
	au, err2 := url.Parse(actual)
	if err1 != nil || err2 != nil {
		return false
	}
	return iu.Path != au.Path || (iu.Host != "" && au.Host != "" && iu.Host != au.Host)
}

// buildPrefillResult assembles the textResult JSON: per-field status, the
// manualFields actionable list, and a summary line. When plan is true the
// fields carry status "planned" (dry run); otherwise they are verified
// against postFields.
func buildPrefillResult(prefilled, navigated bool, builtURL string, mappings []fieldMapping, postFields []formField, plan bool) map[string]any {
	fields := make([]map[string]any, 0, len(mappings))
	manual := make([]map[string]any, 0)
	verifiedCount := 0

	for _, m := range mappings {
		fe := map[string]any{"name": m.Name}
		if m.Param != "" {
			fe["param"] = m.Param
		} else {
			fe["param"] = nil
		}
		if m.Selector != "" {
			fe["selector"] = m.Selector
		} else {
			fe["selector"] = nil
		}
		if m.Confidence != "" {
			fe["confidence"] = string(m.Confidence)
		}

		switch {
		case plan:
			// Dry run — no verification has happened.
			if m.Param != "" {
				fe["status"] = "planned"
			} else if m.Selector == "" {
				fe["status"] = "unmapped"
				fe["reason"] = m.Reason
			} else {
				fe["status"] = "unmapped"
				fe["reason"] = m.Reason
			}
		case m.Selector == "":
			// formData key matched no field on the page at all.
			fe["status"] = "unmapped"
			fe["reason"] = m.Reason
		case m.Param == "":
			// Field exists but no param was written for it.
			fe["status"] = "unmapped"
			fe["reason"] = m.Reason
		default:
			status, actual := verifyField(m.Value, postFields, m.Selector)
			fe["status"] = status
			fe["actualValue"] = actual
			if status == "verified" {
				verifiedCount++
			}
		}

		fields = append(fields, fe)

		// Route every field whose value still needs setting to manualFields.
		needsManual := plan && m.Param == ""
		if !plan {
			st, _ := fe["status"].(string)
			needsManual = st != "verified"
		}
		if needsManual {
			mf := map[string]any{
				"name":  m.Name,
				"value": m.Value,
			}
			if m.Selector != "" {
				mf["selector"] = m.Selector
				mf["hint"] = "fill with type_text or cdp_type"
			} else {
				mf["selector"] = nil
				mf["hint"] = "no field matched this key on the page"
			}
			if m.Reason != "" {
				mf["reason"] = m.Reason
			}
			manual = append(manual, mf)
		}
	}

	total := len(mappings)
	res := map[string]any{
		"prefilled":    prefilled,
		"url":          builtURL,
		"navigated":    navigated,
		"fields":       fields,
		"manualFields": manual,
	}
	if plan {
		planned := total - len(manual)
		res["summary"] = fmt.Sprintf("dry run: %d/%d fields can be prefilled via URL; %d need manual filling",
			planned, total, len(manual))
	} else {
		res["summary"] = fmt.Sprintf("%d/%d fields prefilled via URL; %d need manual filling",
			verifiedCount, total, len(manual))
	}
	return res
}
