package rerankinstruction

import (
	"github.com/wgdl666/wgModelHub/config"
	"testing"
)

func TestResolveProtocolAndRawQuery(t *testing.T) {
	resolve := Resolver(func() map[string]config.RerankTemplate {
		return map[string]config.RerankTemplate{
			"color": {Instruct: "main color", QueryTemplate: "主色「{{query}}」"},
			"look":  {Instruct: "all conditions", QueryTemplate: "外观「{{query}}」"},
		}
	})
	query := `红色「{{query}}」{"x":1}`
	for _, key := range []string{"color", "look"} {
		for _, native := range []bool{true, false} {
			got, instruct, err := resolve.Resolve(query, "", key, native)
			if err != nil {
				t.Fatal(err)
			}
			if native {
				if got != query || instruct != resolve()[key].Instruct {
					t.Fatalf("native query=%q instruct=%q", got, instruct)
				}
			} else {
				prefix := "主色"
				if key == "look" {
					prefix = "外观"
				}
				if got != prefix+"「"+query+"」" || instruct != "" {
					t.Fatalf("rendered=%q instruct=%q", got, instruct)
				}
			}
		}
	}
	if _, _, err := resolve.Resolve("q", "", "missing", false); err == nil {
		t.Fatal("unknown key accepted")
	}
	if _, _, err := resolve.Resolve("q", "legacy", "color", true); err == nil {
		t.Fatal("ambiguous instructions accepted")
	}
	var missing Resolver
	if _, _, err := missing.Resolve("q", "", "look", false); err == nil {
		t.Fatal("missing config accepted")
	}
	if q, i, err := missing.Resolve("q", "legacy", "", true); err != nil || q != "q" || i != "legacy" {
		t.Fatal("legacy protocol changed")
	}
}
