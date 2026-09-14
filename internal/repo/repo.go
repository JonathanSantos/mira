// Package repo localiza a raiz do repositório alvo e cuida do diretório
// .mira/ (config e caminho do banco). É compartilhado por CLI e MCP.
package repo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/JonathanSantos/mira/internal/lang"
)

const (
	DirName    = ".mira"
	DBFile     = "index.db"
	ConfigFile = "config.yaml"
)

// Config é o conteúdo de .mira/config.yaml.
type Config struct {
	// Languages restringe as linguagens indexadas; vazio = todas.
	Languages []string `yaml:"languages"`
	// Ignore são padrões extras no estilo .gitignore, relativos à raiz.
	Ignore []string `yaml:"ignore"`
	// SkipText desliga a indexação lexical de arquivos sem código
	// (properties, YAML, templates, SQL, Markdown).
	SkipText bool `yaml:"skip_text"`
}

// DefaultConfig lista todas as linguagens suportadas e nenhum ignore extra.
func DefaultConfig() Config {
	names := make([]string, 0, len(lang.All))
	for _, l := range lang.All {
		names = append(names, string(l))
	}
	return Config{Languages: names, Ignore: []string{}}
}

// Enabled diz se a linguagem está habilitada na config.
func (c Config) Enabled(l lang.Lang) bool {
	if len(c.Languages) == 0 {
		return true
	}
	for _, name := range c.Languages {
		if name == string(l) {
			return true
		}
	}
	return false
}

// FindRoot sobe a partir de start até encontrar um diretório com .mira/
// ou .git/. Sem marcador nenhum, devolve o próprio start absoluto.
func FindRoot(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", start, err)
	}
	for dir := abs; ; dir = filepath.Dir(dir) {
		if isDir(filepath.Join(dir, DirName)) || exists(filepath.Join(dir, ".git")) {
			return dir, nil
		}
		if filepath.Dir(dir) == dir {
			return abs, nil
		}
	}
}

func DataDir(root string) string    { return filepath.Join(root, DirName) }
func DBPath(root string) string     { return filepath.Join(DataDir(root), DBFile) }
func ConfigPath(root string) string { return filepath.Join(DataDir(root), ConfigFile) }

// Initialized diz se `mira init` já rodou nessa raiz.
func Initialized(root string) bool {
	return exists(DBPath(root))
}

// LoadConfig lê a config; sem arquivo devolve a default.
func LoadConfig(root string) (Config, error) {
	data, err := os.ReadFile(ConfigPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", ConfigPath(root), err)
	}
	for _, name := range cfg.Languages {
		if !lang.Valid(name) {
			return Config{}, fmt.Errorf("config: unsupported language %q", name)
		}
	}
	return cfg, nil
}

// WriteConfig grava a config, criando .mira/ se preciso.
func WriteConfig(root string, cfg Config) error {
	if err := os.MkdirAll(DataDir(root), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", DataDir(root), err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	header := []byte("# mira configuration. Languages: typescript, javascript, java, python, go.\n# skip_text: true disables indexing words of .properties/.yml/.html/.sql/.md/.xml/.json/.txt/.toml files.\n")
	if err := os.WriteFile(ConfigPath(root), append(header, data...), 0o644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

// GitignoreHint devolve uma sugestão para o usuário quando o .gitignore do
// repo alvo ainda não ignora .mira/. Nunca edita o arquivo.
func GitignoreHint(root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		if exists(filepath.Join(root, ".git")) {
			return "hint: add '.mira/' to your .gitignore"
		}
		return ""
	}
	if containsLine(data, DirName) || containsLine(data, DirName+"/") {
		return ""
	}
	return "hint: add '.mira/' to your .gitignore"
}

func containsLine(data []byte, want string) bool {
	start := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			if string(data[start:i]) == want {
				return true
			}
			start = i + 1
		}
	}
	return false
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
