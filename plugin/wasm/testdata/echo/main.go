// echo is the goblog wasm test fixture: it implements every export of the
// host contract in the most literal way so the adapter can be tested.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	pdk "github.com/extism/go-pdk"
)

//go:wasmimport extism:host/user store_get
func hostStoreGet(uint64) uint64

//go:wasmimport extism:host/user store_set
func hostStoreSet(uint64, uint64) uint64

//go:wasmimport extism:host/user store_delete
func hostStoreDelete(uint64) uint64

//go:wasmimport extism:host/user store_list
func hostStoreList(uint64) uint64

// storeGetOK returns the value and whether the key exists: the host returns
// offset 0 (Extism's null) for a missing key.
func storeGetOK(key string) (string, bool) {
	k := pdk.AllocateString(key)
	defer k.Free()
	ptr := hostStoreGet(k.Offset())
	if ptr == 0 {
		return "", false
	}
	return pdk.ParamString(ptr), true
}

func storeGet(key string) string {
	v, _ := storeGetOK(key)
	return v
}

func storeSet(key, value string) {
	k := pdk.AllocateString(key)
	defer k.Free()
	v := pdk.AllocateString(value)
	defer v.Free()
	hostStoreSet(k.Offset(), v.Offset())
}

func storeDelete(key string) {
	k := pdk.AllocateString(key)
	defer k.Free()
	hostStoreDelete(k.Offset())
}

func storeList(prefix string) string {
	p := pdk.AllocateString(prefix)
	defer p.Free()
	return pdk.ParamString(hostStoreList(p.Offset()))
}

type ctx struct {
	Settings map[string]string `json:"settings"`
	Template string            `json:"template"`
	Request  struct {
		Path    string            `json:"path"`
		SubPath string            `json:"sub_path"`
		Query   map[string]string `json:"query"`
		Method  string            `json:"method"`
	} `json:"request"`
}

func readCtx() ctx {
	var c ctx
	json.Unmarshal(pdk.Input(), &c)
	return c
}

func out(v any) int32 {
	b, _ := json.Marshal(v)
	pdk.Output(b)
	return 0
}

//go:wasmexport identity
func identity() int32 {
	return out(map[string]string{"name": "echo", "display_name": "Echo", "version": "1.2.3"})
}

//go:wasmexport settings
func settings() int32 {
	return out([]map[string]string{
		{"key": "enabled", "type": "text", "default": "true", "label": "Enabled", "description": "on/off"},
		{"key": "greeting", "type": "text", "default": "hi", "label": "Greeting", "description": "footer text"},
	})
}

//go:wasmexport pages
func pages() int32 {
	return out([]map[string]any{{"page_type": "echo", "title": "Echo", "slug": "echo", "show_in_nav": true, "nav_order": 5, "description": "echo page"}})
}

//go:wasmexport jobs
func jobs() int32 {
	return out([]map[string]any{{"name": "tick", "interval_seconds": 60}})
}

//go:wasmexport template_footer
func templateFooter() int32 {
	c := readCtx()
	pdk.OutputString("<p>" + c.Settings["greeting"] + "</p>")
	return 0
}

//go:wasmexport template_head
func templateHead() int32 {
	pdk.OutputString("<!-- echo head -->")
	return 0
}

//go:wasmexport template_data
func templateData() int32 {
	c := readCtx()
	return out(map[string]any{"greeting": c.Settings["greeting"], "template": c.Template})
}

//go:wasmexport render_page
func renderPage() int32 {
	c := readCtx()
	switch c.Request.SubPath {
	case "":
		return out(map[string]any{"html": "<h2>echo</h2>" + c.Settings["greeting"]})
	case "data.json":
		return out(map[string]any{"raw": map[string]any{"status": 200, "content_type": "application/json", "body": `{"ok":true}`}})
	case "tmpl":
		return out(map[string]any{"template": "page_content.html", "data": map[string]any{"plugin_content": "from template", "has_plugin_content": true}})
	case "store":
		storeSet("k", "v-"+c.Request.Query["v"])
		got := storeGet("k")
		keys := storeList("")
		storeDelete("k")
		after, found := storeGetOK("k")
		if !found {
			after = "missing"
		}
		return out(map[string]any{"html": "got=" + got + " keys=" + keys + " after=[" + after + "]"})
	case "raw-badstatus":
		return out(map[string]any{"raw": map[string]any{"status": 5, "body": "x"}})
	case "raw-defaults":
		return out(map[string]any{"raw": map[string]any{"body": "plain"}})
	case "now":
		return out(map[string]any{"html": itoa(int(time.Now().Unix()))})
	case "rand":
		b := make([]byte, 16)
		rand.Read(b)
		return out(map[string]any{"html": hex.EncodeToString(b)})
	case "http":
		req := pdk.NewHTTPRequest(pdk.MethodGet, c.Request.Query["url"])
		resp := req.Send()
		return out(map[string]any{"html": "status=" + itoa(int(resp.Status())) + " body=" + string(resp.Body())})
	case "log":
		pdk.Log(pdk.LogInfo, "hello from echo")
		return out(map[string]any{"html": "logged"})
	case "spin":
		for {
		}
	case "big":
		b := make([]byte, 200<<20)
		return out(map[string]any{"html": itoa(len(b))})
	case "boom":
		pdk.SetErrorString("kaboom")
		return 1
	}
	return out(map[string]any{})
}

//go:wasmexport run_job
func runJob() int32 {
	var in struct {
		Name     string            `json:"name"`
		Settings map[string]string `json:"settings"`
	}
	json.Unmarshal(pdk.Input(), &in)
	storeSet("job_ran", in.Name+"/"+in.Settings["greeting"])
	return out(map[string]any{})
}

//go:wasmexport on_init
func onInit() int32 {
	var in struct {
		Settings map[string]string `json:"settings"`
	}
	json.Unmarshal(pdk.Input(), &in)
	storeSet("init", "1")
	storeSet("init_greeting", in.Settings["greeting"])
	return out(map[string]any{})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return strings.TrimLeft(string(b), " ")
}

func main() {}
