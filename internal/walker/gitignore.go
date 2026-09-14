package walker

import (
	"bufio"
	"bytes"
	"path"
	"strings"
)

// pattern é uma regra de .gitignore já normalizada.
//
// Subconjunto implementado: comentários, negação com "!", sufixo "/" para
// diretórios, padrões ancorados (contêm "/" fora do final) casados contra o
// caminho relativo ao diretório do .gitignore, padrões simples casados contra
// qualquer segmento do caminho, e os curingas "*", "?" e "**".
type pattern struct {
	negate   bool
	dirOnly  bool
	anchored bool
	segs     []string
}

// matcher agrupa as regras de um .gitignore e o diretório em que ele vive
// (relativo à raiz, "" para a raiz).
type matcher struct {
	base     string
	patterns []pattern
}

func parseGitignore(base string, content []byte) matcher {
	m := matcher{base: base}
	sc := bufio.NewScanner(bytes.NewReader(content))
	for sc.Scan() {
		if p, ok := parsePattern(sc.Text()); ok {
			m.patterns = append(m.patterns, p)
		}
	}
	return m
}

func parsePattern(line string) (pattern, bool) {
	line = strings.TrimRight(line, " \t")
	if line == "" || strings.HasPrefix(line, "#") {
		return pattern{}, false
	}
	var p pattern
	if strings.HasPrefix(line, "!") {
		p.negate = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		p.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	// Uma barra no início ou no meio ancora o padrão ao diretório do .gitignore.
	if strings.Contains(line, "/") {
		p.anchored = true
		line = strings.TrimPrefix(line, "/")
	}
	if line == "" {
		return pattern{}, false
	}
	p.segs = strings.Split(line, "/")
	return p, true
}

// match avalia o caminho (relativo à raiz, com "/") contra as regras.
// Devolve (ignorado, alguma regra casou). A última regra que casa vence,
// como no git.
func (m matcher) match(rel string, isDir bool) (ignored, matched bool) {
	sub, ok := m.relativeTo(rel)
	if !ok {
		return false, false
	}
	segs := strings.Split(sub, "/")
	for _, p := range m.patterns {
		if p.matches(segs, isDir) {
			ignored, matched = !p.negate, true
		}
	}
	return ignored, matched
}

func (m matcher) relativeTo(rel string) (string, bool) {
	if m.base == "" {
		return rel, true
	}
	if rel == m.base {
		return "", false
	}
	if !strings.HasPrefix(rel, m.base+"/") {
		return "", false
	}
	return rel[len(m.base)+1:], true
}

func (p pattern) matches(segs []string, isDir bool) bool {
	if p.anchored {
		return p.matchAnchored(segs, isDir)
	}
	// Não ancorado: casa com o nome do arquivo ou de qualquer diretório pai.
	for i, seg := range segs {
		last := i == len(segs)-1
		if last && p.dirOnly && !isDir {
			continue
		}
		if matchSegs(p.segs, []string{seg}) {
			return true
		}
	}
	return false
}

func (p pattern) matchAnchored(segs []string, isDir bool) bool {
	// Um padrão ancorado que casa com um prefixo de diretório ignora tudo
	// abaixo dele; por isso testamos cada prefixo do caminho.
	for n := 1; n <= len(segs); n++ {
		last := n == len(segs)
		if last && p.dirOnly && !isDir {
			continue
		}
		if matchSegs(p.segs, segs[:n]) {
			return true
		}
	}
	return false
}

// matchSegs casa segmentos de padrão contra segmentos de caminho, com "**"
// consumindo zero ou mais segmentos.
func matchSegs(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for skip := 0; skip <= len(segs); skip++ {
			if matchSegs(pat[1:], segs[skip:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], segs[0])
	if err != nil || !ok {
		return false
	}
	return matchSegs(pat[1:], segs[1:])
}

// ignoreSet é a pilha de matchers ativos num ponto da caminhada, da raiz até
// o diretório atual. O mais profundo tem precedência.
type ignoreSet []matcher

func (s ignoreSet) ignored(rel string, isDir bool) bool {
	result := false
	for _, m := range s {
		if ign, matched := m.match(rel, isDir); matched {
			result = ign
		}
	}
	return result
}
