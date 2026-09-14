package resolve

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// tsConfig é o pedaço de tsconfig.json que muda a resolução de módulos:
// `baseUrl` e `paths` (`"@/*": ["src/*"]`). `extends` relativo é seguido.
type tsConfig struct {
	baseURL string
	paths   map[string][]string
}

// loadTSConfig lê <root>/tsconfig.json; sem arquivo (ou inválido) devolve
// uma config vazia, que não muda nada.
func loadTSConfig(root string) *tsConfig {
	cfg := &tsConfig{paths: map[string][]string{}}
	cfg.load(filepath.Join(root, "tsconfig.json"), 0)
	return cfg
}

func (c *tsConfig) load(file string, depth int) {
	if depth > 3 {
		return
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	var raw struct {
		Extends         string `json:"extends"`
		CompilerOptions struct {
			BaseURL string              `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if err := json.Unmarshal(stripJSONComments(data), &raw); err != nil {
		return
	}
	if raw.Extends != "" && (strings.HasPrefix(raw.Extends, ".") || strings.HasPrefix(raw.Extends, "/")) {
		c.load(filepath.Join(filepath.Dir(file), raw.Extends), depth+1) // o filho sobrescreve o pai
	}
	if raw.CompilerOptions.BaseURL != "" {
		c.baseURL = path.Clean(raw.CompilerOptions.BaseURL)
	}
	for pattern, targets := range raw.CompilerOptions.Paths {
		c.paths[pattern] = targets
	}
}

// rewrite devolve os caminhos (relativos à raiz) em que um módulo não
// relativo pode estar, segundo paths e baseUrl. Vazio = não é do projeto.
func (c *tsConfig) rewrite(module string) []string {
	if c == nil {
		return nil
	}
	base := c.baseURL
	if base == "" {
		base = "."
	}
	var out []string
	patterns := make([]string, 0, len(c.paths))
	for p := range c.paths {
		patterns = append(patterns, p)
	}
	sort.Slice(patterns, func(i, j int) bool { return len(patterns[i]) > len(patterns[j]) })
	for _, pattern := range patterns {
		captured, ok := matchPattern(pattern, module)
		if !ok {
			continue
		}
		for _, target := range c.paths[pattern] {
			out = append(out, path.Clean(path.Join(base, strings.Replace(target, "*", captured, 1))))
		}
	}
	if len(out) == 0 && c.baseURL != "" {
		out = append(out, path.Clean(path.Join(base, module)))
	}
	return out
}

// matchPattern casa `@/*` com `@/utils/x`, devolvendo a parte do `*`.
func matchPattern(pattern, module string) (string, bool) {
	star := strings.Index(pattern, "*")
	if star < 0 {
		return "", pattern == module
	}
	prefix, suffix := pattern[:star], pattern[star+1:]
	if !strings.HasPrefix(module, prefix) || !strings.HasSuffix(module, suffix) || len(module) < len(prefix)+len(suffix) {
		return "", false
	}
	return module[len(prefix) : len(module)-len(suffix)], true
}

// stripJSONComments tira `//`, `/* */` e vírgulas finais, que o tsconfig
// aceita e o encoding/json não.
func stripJSONComments(data []byte) []byte {
	var out []byte
	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case inString:
			out = append(out, c)
			if c == '\\' && i+1 < len(data) {
				i++
				out = append(out, data[i])
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			i--
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && (data[i] != '*' || data[i+1] != '/') {
				i++
			}
			i++
		case c == ',':
			// Vírgula final: pula se o próximo não-espaço fecha um objeto/lista.
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\n' || data[j] == '\t' || data[j] == '\r') {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}
