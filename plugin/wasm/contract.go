package wasm

import (
	"reflect"
	"strings"
)

// JSON shapes of the host contract (spec §1). Field names are the API: a
// plugin written in any language talks to goblog through exactly these keys.

// Identity is what the mandatory identity export returns.
type Identity struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Version     string `json:"version"`
}

type settingDef struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type pageDef struct {
	PageType    string `json:"page_type"`
	Title       string `json:"title"`
	Slug        string `json:"slug"`
	ShowInNav   bool   `json:"show_in_nav"`
	NavOrder    int    `json:"nav_order"`
	Description string `json:"description"`
}

type jobDef struct {
	Name            string `json:"name"`
	IntervalSeconds int    `json:"interval_seconds"`
}

type requestCtx struct {
	Path    string            `json:"path"`
	SubPath string            `json:"sub_path"`
	Query   map[string]string `json:"query"`
	Method  string            `json:"method"`
}

// hookInput is the input of template_* and render_page.
type hookInput struct {
	Settings map[string]string `json:"settings"`
	Template string            `json:"template"`
	Request  requestCtx        `json:"request"`
}

// jobInput is the input of run_job.
type jobInput struct {
	Name     string            `json:"name"`
	Settings map[string]string `json:"settings"`
}

// initInput is the input of on_init. The settings are the plugin's current
// values: its declared defaults overlaid with what is stored for it (the
// registry seeds missing keys right before calling OnInit), since the
// plugin has no other way to read them at that point.
type initInput struct {
	Settings map[string]string `json:"settings"`
}

type rawResponse struct {
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Body        string `json:"body"`
}

// renderResult is the output of render_page: exactly one of html, template
// (+data) or raw is expected.
type renderResult struct {
	HTML     *string        `json:"html"`
	Template string         `json:"template"`
	Data     map[string]any `json:"data"`
	Raw      *rawResponse   `json:"raw"`
}

// ContractFieldNames lists the JSON field names of every wire type in the
// plugin contract, for the documentation lint.
func ContractFieldNames() []string {
	var out []string
	for _, v := range []any{Identity{}, settingDef{}, pageDef{}, jobDef{}, requestCtx{}, hookInput{}, jobInput{}, initInput{}, rawResponse{}, renderResult{}} {
		t := reflect.TypeOf(v)
		for i := 0; i < t.NumField(); i++ {
			if tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); tag != "" && tag != "-" {
				out = append(out, tag)
			}
		}
	}
	return out
}
