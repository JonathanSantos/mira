package indexer

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/store"
)

// batchSize é o número de arquivos por transação. Grande o bastante para
// amortizar o commit, pequeno o bastante para não segurar memória à toa.
const batchSize = 64

// writer é o único goroutine que toca o SQLite durante a indexação.
type writer struct {
	store    *store.Store
	existing map[string]store.File
	report   *Report
	pending  []result
	plans    map[string]replacement // por caminho, para os arquivos alterados do lote
	out      written
}

func (w *writer) run(results <-chan result) error {
	for res := range results {
		if res.warning != "" {
			w.report.Warnings = append(w.report.Warnings, res.warning)
			continue
		}
		w.pending = append(w.pending, res)
		if len(w.pending) >= batchSize {
			if err := w.flush(); err != nil {
				return err
			}
		}
	}
	return w.flush()
}

// flush grava um lote: primeiro compara cada arquivo alterado com o que está
// indexado e acha quem depende do que mudou (leituras, fora da transação),
// depois grava tudo numa transação.
func (w *writer) flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	defer func() { w.pending = w.pending[:0] }()

	w.plans = map[string]replacement{}
	for i := range w.pending {
		res := &w.pending[i]
		if res.sameHash {
			continue
		}
		res.symbols = toStoreSymbols(res.job.file.Path, res.extract.Symbols)
		w.out.configChanged = w.out.configChanged || isResolverConfig(res.job.file.Path)
		prev, known := w.existing[res.job.file.Path]
		if !known {
			w.out.newFiles = true
			w.addNames(definitionNames(res.symbols...))
			continue
		}
		plan, err := w.plan(prev, *res)
		if err != nil {
			return err
		}
		w.plans[res.job.file.Path] = plan
	}

	tx, err := w.store.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixNano()
	for _, res := range w.pending {
		if err := w.write(tx, res, now); err != nil {
			return fmt.Errorf("writing %s: %w", res.job.file.Path, err)
		}
	}
	return tx.Commit()
}

// plan compara o arquivo indexado com a extração nova e junta aos afetados
// quem usava um símbolo que mudou ou sumiu e, se os imports mudaram, quem
// importa o arquivo.
func (w *writer) plan(prev store.File, res result) (replacement, error) {
	oldSymbols, err := w.store.SymbolsOfFile(prev.ID)
	if err != nil {
		return replacement{}, err
	}
	oldImports, err := w.store.ImportsOfFile(prev.ID)
	if err != nil {
		return replacement{}, err
	}
	plan := planReplacement(oldSymbols, res.symbols, oldImports, toStoreImports(res.extract.Imports))
	if prev.Package != packageOf(res) || prev.Lang != res.job.lang {
		plan = replaceAll(oldSymbols, res.symbols)
	}
	w.addNames(plan.names)
	if ids := append(append([]int64{}, plan.changed...), plan.removed...); len(ids) > 0 {
		deps, err := w.store.FilesReferencing(ids, prev.ID)
		if err != nil {
			return replacement{}, err
		}
		w.out.affected = append(w.out.affected, deps...)
	}
	if plan.importsChanged {
		deps, err := w.store.Importers(prev.ID)
		if err != nil {
			return replacement{}, err
		}
		w.out.affected = append(w.out.affected, deps...)
	}
	return plan, nil
}

func (w *writer) addNames(names []string) {
	for _, n := range names {
		if n != "" {
			w.out.names[n] = true
		}
	}
}

func (w *writer) write(tx *store.Tx, res result, now int64) error {
	prev, known := w.existing[res.job.file.Path]
	if res.sameHash {
		w.report.Rehashed++
		return tx.UpdateFingerprint(prev.ID, res.job.file.Size, res.job.file.ModTime.UnixNano())
	}
	if res.parseErr {
		w.report.ParseErrors++
	}
	file := store.File{
		Path: res.job.file.Path, Hash: res.hash, Size: res.job.file.Size,
		ModTime: res.job.file.ModTime.UnixNano(), Lang: res.job.lang,
		Package: packageOf(res), ParseError: res.parseErr, IndexedAt: now,
	}
	fileID, symbolIDs, err := w.writeFile(tx, file, res.symbols, prev, known)
	if err != nil {
		return err
	}
	if err := tx.InsertWords(fileID, res.words); err != nil {
		return err
	}
	if err := tx.InsertRefs(fileID, toStoreRefs(res.extract.Refs), symbolIDs); err != nil {
		return err
	}
	if err := tx.InsertImports(fileID, toStoreImports(res.extract.Imports)); err != nil {
		return err
	}
	w.out.affected = append(w.out.affected, fileID)
	return nil
}

// writeFile grava o arquivo e os símbolos e devolve o id de cada símbolo por
// índice. Um arquivo novo é inserido. Um alterado é regravado no lugar:
// metadados e símbolos mantidos com o mesmo id, removidos apagados, novos
// inseridos; palavras, refs e imports dele saem para serem gravados de novo.
func (w *writer) writeFile(tx *store.Tx, file store.File, symbols []store.Symbol, prev store.File, known bool) (int64, []int64, error) {
	if !known {
		w.report.New++
		fileID, err := tx.InsertFile(file)
		if err != nil {
			return 0, nil, err
		}
		w.out.packageFiles = append(w.out.packageFiles, fileID)
		ids, err := tx.InsertSymbols(fileID, symbols)
		return fileID, ids, err
	}
	w.report.Changed++
	plan := w.plans[file.Path]
	if len(plan.names) > 0 {
		w.out.packageFiles = append(w.out.packageFiles, prev.ID)
	}
	if err := tx.UpdateFile(prev.ID, file); err != nil {
		return 0, nil, err
	}
	if err := tx.ClearFileContents(prev.ID); err != nil {
		return 0, nil, err
	}
	for _, id := range plan.removed {
		if err := tx.DeleteSymbol(id); err != nil {
			return 0, nil, err
		}
	}
	ids := make([]int64, len(symbols))
	var added []store.Symbol
	var addedAt []int
	for i, s := range symbols {
		if plan.keep[i] == 0 {
			added, addedAt = append(added, s), append(addedAt, i)
			continue
		}
		if err := tx.UpdateSymbol(plan.keep[i], s); err != nil {
			return 0, nil, err
		}
		ids[i] = plan.keep[i]
	}
	inserted, err := tx.InsertSymbols(prev.ID, added)
	if err != nil {
		return 0, nil, err
	}
	for j, id := range inserted {
		ids[addedAt[j]] = id
	}
	return prev.ID, ids, nil
}

// packageOf é o pacote do arquivo: o que o extrator leu, ou o diretório em Go.
func packageOf(res result) string {
	if res.job.lang == lang.Go {
		return path.Dir(res.job.file.Path)
	}
	return res.extract.Package
}

// toStoreSymbols converte para linhas do store. Um export default anônimo
// ganha o nome do arquivo (`validateField` para src/logic/validateField.ts):
// é como o resto do código se refere a ele, e "default" não diz nada em
// callers/callees.
func toStoreSymbols(path string, in []extract.Symbol) []store.Symbol {
	out := make([]store.Symbol, 0, len(in))
	module := moduleName(path)
	hasDefault := false
	for _, s := range in {
		hasDefault = hasDefault || s.Name == "default" && s.ExportName == "default"
	}
	for _, s := range in {
		if s.Name == "default" && s.ExportName == "default" {
			s.Name = module
			s.QualifiedName = s.Name
		}
		// Closures do default anônimo seguem o mesmo rename (`default.helper`
		// vira `validateField.helper`).
		if hasDefault && s.Container == "default" {
			s.Container = module
			s.QualifiedName = module + strings.TrimPrefix(s.QualifiedName, "default")
		}
		out = append(out, store.Symbol{
			Name: s.Name, QualifiedName: s.QualifiedName, Kind: s.Kind, Container: s.Container,
			Exported: s.Exported, ExportName: s.ExportName, JSX: s.JSX,
			StartLine: s.StartLine, EndLine: s.EndLine, StartByte: s.StartByte, EndByte: s.EndByte,
			Signature: s.Signature, NameLine: s.NameLine, Annotations: s.Annotations, ReturnHint: s.ReturnHint,
		})
	}
	return out
}

// moduleName é o nome base do arquivo sem extensão; para index.* usa o
// diretório, que é como o módulo é importado.
func moduleName(path string) string {
	base := filepath.Base(path)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	if name == "index" {
		if dir := filepath.Base(filepath.Dir(path)); dir != "." && dir != "/" {
			return dir
		}
	}
	return name
}

func toStoreRefs(in []extract.Ref) []store.Ref {
	out := make([]store.Ref, 0, len(in))
	for _, r := range in {
		out = append(out, store.Ref{
			Name: r.Name, Kind: r.Kind, Line: r.Line, Col: r.Col,
			Receiver: r.Receiver, ReceiverType: r.ReceiverType, ContainerIndex: r.Container, Arity: r.Arity, ArgTypes: r.ArgTypes,
			ReceiverPath: r.ReceiverPath,
		})
	}
	return out
}

func toStoreImports(in []extract.Import) []store.Import {
	out := make([]store.Import, 0, len(in))
	for _, im := range in {
		out = append(out, store.Import{
			Kind: im.Kind, Module: im.Module, ImportedName: im.ImportedName, LocalName: im.LocalName,
			IsReexport: im.IsReexport, IsWildcard: im.IsWildcard, Line: im.Line,
		})
	}
	return out
}
