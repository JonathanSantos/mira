package indexer

import (
	"sort"
	"strconv"
	"strings"

	"github.com/JonathanSantos/mira/internal/store"
)

// replacement é o plano para regravar um arquivo alterado mantendo o id dos
// símbolos que continuam nele. Refs de outros arquivos apontam para esses ids
// e seguem válidas: só quem usava um símbolo que mudou de definição ou sumiu
// precisa ser resolvido de novo. Numa edição só no corpo, ninguém precisa.
type replacement struct {
	keep           []int64  // id mantido por índice do símbolo novo; 0 = símbolo novo
	removed        []int64  // ids que sumiram do arquivo
	changed        []int64  // ids mantidos cuja definição mudou
	names          []string // nomes de definições que apareceram, sumiram ou mudaram
	importsChanged bool     // re-exports e imports mudam o que outros arquivos alcançam por aqui
}

// planReplacement casa os símbolos antigos com os novos pela identidade (nome
// qualificado e kind), na ordem em que aparecem no arquivo, e compara os
// imports sem olhar a linha.
func planReplacement(prev, next []store.Symbol, prevImports, nextImports []store.Import) replacement {
	plan := replacement{keep: make([]int64, len(next))}
	queues := map[string][]store.Symbol{}
	for _, s := range prev {
		queues[identity(s)] = append(queues[identity(s)], s)
	}
	order := make([]int, len(next))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return next[order[a]].StartByte < next[order[b]].StartByte })
	for _, i := range order {
		s := next[i]
		queue := queues[identity(s)]
		if len(queue) == 0 {
			plan.names = append(plan.names, definitionNames(s)...)
			continue
		}
		old := queue[0]
		queues[identity(s)] = queue[1:]
		plan.keep[i] = old.ID
		if definitionKey(old) != definitionKey(s) {
			plan.changed = append(plan.changed, old.ID)
			plan.names = append(plan.names, definitionNames(old, s)...)
		}
	}
	for _, queue := range queues {
		for _, s := range queue {
			plan.removed = append(plan.removed, s.ID)
			plan.names = append(plan.names, definitionNames(s)...)
		}
	}
	sort.Slice(plan.removed, func(a, b int) bool { return plan.removed[a] < plan.removed[b] })
	for _, im := range importDiff(prevImports, nextImports) {
		plan.importsChanged = true
		plan.names = append(plan.names, im.LocalName)
	}
	return plan
}

// replaceAll é o plano quando nada do que estava indexado vale mais (o
// arquivo mudou de pacote ou de linguagem): tudo sai, tudo entra.
func replaceAll(prev, next []store.Symbol) replacement {
	plan := replacement{keep: make([]int64, len(next)), importsChanged: true}
	for _, s := range prev {
		plan.removed = append(plan.removed, s.ID)
	}
	plan.names = append(definitionNames(prev...), definitionNames(next...)...)
	return plan
}

func identity(s store.Symbol) string {
	return s.QualifiedName + "\x00" + s.Kind
}

// definitionKey junta o que outro arquivo usa de uma definição para resolver:
// o nome e o container (no qualificado), o kind, a exportação, a assinatura
// (aridade das sobrecargas) e o tipo de retorno inferido.
func definitionKey(s store.Symbol) string {
	return strings.Join([]string{s.QualifiedName, s.Kind, strconv.FormatBool(s.Exported), s.ExportName, s.Signature, s.ReturnHint}, "\x00")
}

// definitionNames são os nomes pelos quais outro arquivo chega às definições.
func definitionNames(symbols ...store.Symbol) []string {
	var out []string
	for _, s := range symbols {
		out = append(out, s.Name)
		if s.ExportName != "" && s.ExportName != s.Name {
			out = append(out, s.ExportName)
		}
	}
	return out
}

// importDiff devolve os imports que estão só de um dos lados.
func importDiff(prev, next []store.Import) []store.Import {
	count := map[string]int{}
	for _, im := range prev {
		count[importKey(im)]++
	}
	var diff []store.Import
	for _, im := range next {
		key := importKey(im)
		if count[key] == 0 {
			diff = append(diff, im)
			continue
		}
		count[key]--
	}
	for _, im := range prev {
		if key := importKey(im); count[key] > 0 {
			count[key]--
			diff = append(diff, im)
		}
	}
	return diff
}

func importKey(im store.Import) string {
	return strings.Join([]string{im.Kind, im.Module, im.ImportedName, im.LocalName,
		strconv.FormatBool(im.IsReexport), strconv.FormatBool(im.IsWildcard)}, "\x00")
}
