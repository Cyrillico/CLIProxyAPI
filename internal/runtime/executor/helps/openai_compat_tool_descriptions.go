package helps

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// FillEmptyOpenAIToolDescriptions gives every function tool a non-empty
// description before the request leaves for the upstream. Some strict
// upstreams (e.g. the OpenCode Zen free tier) reject the whole request with
// `missing_required_parameter: tools[i].description is required`, although
// OpenAI itself treats the field as optional. A missing or whitespace-only
// description is replaced with a "Tool <name>" placeholder; explicit
// descriptions are never touched.
func FillEmptyOpenAIToolDescriptions(payload []byte) []byte {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return payload
	}
	out := payload
	changed := false
	for i, tool := range tools.Array() {
		fn := tool.Get("function")
		if !fn.Exists() {
			continue
		}
		if strings.TrimSpace(fn.Get("description").String()) != "" {
			continue
		}
		name := fn.Get("name").String()
		if name == "" {
			name = tool.Get("name").String()
		}
		if name == "" {
			continue
		}
		updated, err := sjson.SetBytes(out, "tools."+strconv.Itoa(i)+".function.description", "Tool "+name)
		if err != nil {
			continue
		}
		out = updated
		changed = true
	}
	if !changed {
		return payload
	}
	return out
}
