package config

import (
	"os"
	"regexp"
	"testing"
)

// TestReadmeTomlParses guards the documentation: every ```toml block in
// both READMEs must parse and validate (the example once shipped a
// duplicated key — invalid TOML that broke configs copied verbatim).
// Unknown keys must NOT fail: docs may mention keys removed from the
// code while old user configs still carry them.
func TestReadmeTomlParses(t *testing.T) {
	re := regexp.MustCompile("(?s)```toml\n(.*?)```")
	for _, name := range []string{"README.md", "README.ru.md"} {
		data, err := os.ReadFile("../" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		matches := re.FindAllStringSubmatch(string(data), -1)
		if len(matches) == 0 {
			t.Fatalf("%s: no ```toml block found", name)
		}
		for i, m := range matches {
			c, err := Parse([]byte(m[1]))
			if err != nil {
				t.Errorf("%s toml block #%d does not parse: %v", name, i+1, err)
				continue
			}
			if err := c.Validate(); err != nil {
				t.Errorf("%s toml block #%d does not validate: %v", name, i+1, err)
			}
		}
	}
}
