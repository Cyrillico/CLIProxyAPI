package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestFillEmptyOpenAIToolDescriptions(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no tools",
			in:   `{"model":"m"}`,
			want: `{"model":"m"}`,
		},
		{
			name: "tools not an array",
			in:   `{"tools":{"type":"function"}}`,
			want: `{"tools":{"type":"function"}}`,
		},
		{
			name: "explicit description kept",
			in:   `{"tools":[{"type":"function","function":{"name":"read","description":"Read a file"}}]}`,
			want: `{"tools":[{"type":"function","function":{"name":"read","description":"Read a file"}}]}`,
		},
		{
			name: "missing description filled",
			in:   `{"tools":[{"type":"function","function":{"name":"read"}}]}`,
			want: `{"tools":[{"type":"function","function":{"name":"read","description":"Tool read"}}]}`,
		},
		{
			name: "whitespace description filled",
			in:   `{"tools":[{"type":"function","function":{"name":"read","description":"   "}}]}`,
			want: `{"tools":[{"type":"function","function":{"name":"read","description":"Tool read"}}]}`,
		},
		{
			name: "nameless function left alone",
			in:   `{"tools":[{"type":"function","function":{"description":""}}]}`,
			want: `{"tools":[{"type":"function","function":{"description":""}}]}`,
		},
		{
			name: "non function tool left alone",
			in:   `{"tools":[{"type":"web_search_preview"}]}`,
			want: `{"tools":[{"type":"web_search_preview"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(FillEmptyOpenAIToolDescriptions([]byte(tc.in)))
			if got != tc.want {
				t.Fatalf("FillEmptyOpenAIToolDescriptions() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestFillEmptyOpenAIToolDescriptionsFillsOnlyMissing(t *testing.T) {
	in := `{"model":"m","tools":[{"type":"function","function":{"name":"read","description":"Read a file"}},{"type":"function","function":{"name":"write"}}]}`
	got := FillEmptyOpenAIToolDescriptions([]byte(in))
	if desc := gjson.GetBytes(got, "tools.0.function.description").String(); desc != "Read a file" {
		t.Fatalf("explicit description rewritten: %q", desc)
	}
	if desc := gjson.GetBytes(got, "tools.1.function.description").String(); desc != "Tool write" {
		t.Fatalf("missing description not filled: %q", desc)
	}
	if strings.Contains(string(got), "Tool read") {
		t.Fatalf("explicit description replaced by placeholder: %s", got)
	}
}
