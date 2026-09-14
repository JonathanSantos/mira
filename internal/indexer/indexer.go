// Package indexer orquestra o pipeline: walker -> hasher -> scanner/extract
// (paralelo) -> um único escritor SQLite -> remoção -> resolução.
package indexer

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/extract/golang"
	"github.com/JonathanSantos/mira/internal/extract/java"
	"github.com/JonathanSantos/mira/internal/extract/python"
	"github.com/JonathanSantos/mira/internal/extract/typescript"
	"github.com/JonathanSantos/mira/internal/hasher"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/parser"
	"github.com/JonathanSantos/mira/internal/repo"
	"github.com/JonathanSantos/mira/internal/resolve"
	"github.com/JonathanSantos/mira/internal/scanner"
	"github.com/JonathanSantos/mira/internal/store"
	"github.com/JonathanSantos/mira/internal/walker"
)

// Options controla uma rodada de indexação.
type Options struct {
	Full    bool // reindexa tudo, ignorando fingerprint e hash
	Workers int  // 0 = runtime.NumCPU()
}

// Phase é o tempo gasto numa fase do pipeline.
type Phase struct {
	Name     string        `json:"name"`
	Duration time.Duration `json:"duration_ms"`
}

// Report é o resultado de uma rodada.
type Report struct {
	Walked      int           `json:"walked"`
	New         int           `json:"new"`
	Changed     int           `json:"changed"`
	Rehashed    int           `json:"rehashed"` // mtime mudou, conteúdo não
	Unchanged   int           `json:"unchanged"`
	Removed     int           `json:"removed"`
	ParseErrors int           `json:"parse_errors"`
	Warnings    []string      `json:"warnings,omitempty"`
	Resolve     resolve.Stats `json:"resolve"`
	Phases      []Phase       `json:"phases"`
	Total       time.Duration `json:"total_ms"`
}

// Indexer segura as dependências de uma indexação.
type Indexer struct {
	root   string
	cfg    repo.Config
	store  *store.Store
	parser *parser.Parser
}

func New(root string, cfg repo.Config, s *store.Store) *Indexer {
	return &Indexer{root: root, cfg: cfg, store: s, parser: parser.New()}
}

// Run executa a indexação incremental (ou completa).
func (ix *Indexer) Run(ctx context.Context, opts Options) (Report, error) {
	start := time.Now()
	var report Report
	timer := phaseTimer{report: &report}

	files, err := walker.Walk(ix.root, ix.walkOptions())
	if err != nil {
		return report, fmt.Errorf("walking %s: %w", ix.root, err)
	}
	report.Walked = len(files)
	timer.mark("walk")

	if opts.Full {
		return ix.runFull(ctx, opts)
	}
	existing, err := ix.existingByPath()
	if err != nil {
		return report, err
	}
	jobs, removed := ix.plan(files, existing, opts.Full, &report)
	timer.mark("plan")

	out, err := ix.process(ctx, jobs, existing, opts, &report)
	if err != nil {
		return report, err
	}
	timer.mark("index")

	removedDeps, removedNames, err := ix.remove(removed, existing)
	if err != nil {
		return report, err
	}
	out.affected = append(out.affected, removedDeps...)
	for _, n := range removedNames {
		out.names[n] = true
	}
	for _, p := range removed {
		out.configChanged = out.configChanged || isResolverConfig(p)
	}
	report.Removed = len(removed)
	timer.mark("remove")

	if len(out.affected) == 0 && len(removed) == 0 {
		// Nada foi escrito nem removido: resolver de novo daria o mesmo resultado.
		timer.mark("resolve")
		report.Total = time.Since(start)
		return report, nil
	}
	toResolve, err := ix.resolutionSet(out)
	if err != nil {
		return report, err
	}
	report.Resolve, err = resolve.New(ix.root, ix.store).ResolveFiles(toResolve)
	if err != nil {
		return report, err
	}
	timer.mark("resolve")

	report.Total = time.Since(start)
	return report, nil
}

// written é o que o escritor passa para a resolução.
type written struct {
	affected      []int64         // arquivos gravados e quem usava o que mudou neles
	names         map[string]bool // nomes de definições que apareceram, sumiram ou mudaram
	packageFiles  []int64         // arquivos novos ou com definições mudadas: os do mesmo pacote podem depender deles
	newFiles      bool            // um arquivo novo pode ser o módulo que um import pendente procurava
	configChanged bool            // um tsconfig mudou: qualquer import pode ter outro destino
}

// isResolverConfig reconhece os arquivos que o resolvedor lê além do código:
// o tsconfig.json da raiz e os tsconfig*.json que ele estende.
func isResolverConfig(path string) bool {
	base := filepath.Base(path)
	return strings.HasPrefix(base, "tsconfig") && strings.HasSuffix(base, ".json")
}

// runFull constrói o índice inteiro num banco à parte, como um índice novo, e
// troca o conteúdo do banco em uso de uma vez: quem lê vê o índice antigo até
// o fim e o novo depois, nunca vazio ou pela metade. O banco à parte some no
// fim, mesmo com erro.
func (ix *Indexer) runFull(ctx context.Context, opts Options) (Report, error) {
	start := time.Now()
	tmpPath := ix.store.Path() + ".full"
	if err := store.RemoveFiles(tmpPath); err != nil {
		return Report{}, err
	}
	defer func() { _ = store.RemoveFiles(tmpPath) }()
	tmp, err := store.Open(tmpPath)
	if err != nil {
		return Report{}, err
	}
	report, err := New(ix.root, ix.cfg, tmp).Run(ctx, Options{Workers: opts.Workers})
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return report, err
	}
	swap := time.Now()
	if err := ix.store.ReplaceWith(tmpPath); err != nil {
		return report, err
	}
	report.Phases = append(report.Phases, Phase{Name: "swap", Duration: time.Since(swap)})
	report.Total = time.Since(start)
	return report, nil
}

func (ix *Indexer) walkOptions() walker.Options {
	var langs []lang.Lang
	for _, l := range lang.All {
		if ix.cfg.Enabled(l) {
			langs = append(langs, l)
		}
	}
	exts := parser.Extensions(langs...)
	if !ix.cfg.SkipText {
		for ext := range lang.TextExtensions {
			exts[ext] = true
		}
	}
	return walker.Options{Extensions: exts, Ignore: ix.cfg.Ignore}
}

func (ix *Indexer) existingByPath() (map[string]store.File, error) {
	files, err := ix.store.Files()
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]store.File, len(files))
	for _, f := range files {
		byPath[f.Path] = f
	}
	return byPath, nil
}

// job é um arquivo que precisa ser lido; se o hash não mudou, só o
// fingerprint é atualizado.
type job struct {
	file walker.File
	lang lang.Lang
}

// plan separa o que mudou (pelo fingerprint) do que sumiu do disco.
func (ix *Indexer) plan(files []walker.File, existing map[string]store.File, full bool, report *Report) ([]job, []string) {
	var jobs []job
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		l, ok := parser.Detect(f.Path)
		if !ok && !ix.cfg.SkipText && lang.IsTextFile(f.Path, f.Size) {
			l, ok = lang.Text, true
		}
		if !ok || l != lang.Text && !ix.cfg.Enabled(l) {
			continue
		}
		seen[f.Path] = true
		prev, known := existing[f.Path]
		if known && !full && prev.Size == f.Size && prev.ModTime == f.ModTime.UnixNano() {
			report.Unchanged++
			continue
		}
		jobs = append(jobs, job{file: f, lang: l})
	}
	var removed []string
	for path := range existing {
		if !seen[path] {
			removed = append(removed, path)
		}
	}
	sort.Strings(removed)
	return jobs, removed
}

// result é o que um worker devolve ao escritor.
type result struct {
	job      job
	hash     string
	sameHash bool // conteúdo igual ao indexado: só atualizar fingerprint
	words    []store.Word
	symbols  []store.Symbol // a extração convertida, preenchida pelo escritor
	extract  extract.Result
	parseErr bool
	warning  string
}

// process roda os workers em paralelo e um único escritor; devolve os ids
// dos arquivos escritos mais os dependentes dos substituídos, e se alguma
// definição apareceu, sumiu ou mudou de assinatura.
func (ix *Indexer) process(ctx context.Context, jobs []job, existing map[string]store.File, opts Options, report *Report) (written, error) {
	workers := opts.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	jobCh := make(chan job)
	resultCh := make(chan result, workers*2)

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		defer close(jobCh)
		for _, j := range jobs {
			select {
			case jobCh <- j:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	var workersGroup errgroup.Group
	for i := 0; i < workers; i++ {
		workersGroup.Go(func() error {
			for j := range jobCh {
				resultCh <- ix.processOne(j, existing[j.file.Path], opts.Full)
			}
			return nil
		})
	}
	g.Go(func() error {
		err := workersGroup.Wait()
		close(resultCh)
		return err
	})

	w := &writer{store: ix.store, existing: existing, report: report, out: written{names: map[string]bool{}}}
	g.Go(func() error { return w.run(resultCh) })
	if err := g.Wait(); err != nil {
		return written{}, err
	}
	return w.out, nil
}

func (ix *Indexer) processOne(j job, prev store.File, full bool) result {
	res := result{job: j}
	abs := filepath.Join(ix.root, filepath.FromSlash(j.file.Path))
	data, hash, err := hasher.ReadAndHash(abs)
	if err != nil {
		res.warning = err.Error()
		return res
	}
	res.hash = hash
	if !full && prev.ID != 0 && prev.Hash == hash {
		res.sameHash = true
		return res
	}
	words := scanner.Scan(data, j.lang)
	if j.lang == lang.Text {
		words = scanner.ScanText(data)
	}
	for _, w := range scanner.WithParts(words) {
		res.words = append(res.words, store.Word{Text: w.Text, Line: w.Line, Kind: w.Kind})
	}
	if j.lang == lang.Text {
		return res // sem árvore: só o índice lexical
	}
	tree, err := ix.parser.ParseFile(j.file.Path, data)
	if err != nil {
		res.warning = fmt.Sprintf("%s: %v", j.file.Path, err)
		return res
	}
	defer tree.Release()
	res.parseErr = tree.HasError
	res.extract = extractorFor(j.lang).Extract(tree)
	return res
}

func extractorFor(l lang.Lang) extract.Extractor {
	switch l {
	case lang.Java:
		return java.New()
	case lang.Go:
		return golang.New()
	case lang.Python:
		return python.New()
	}
	return typescript.New()
}

// remove apaga do índice os arquivos que sumiram do disco e devolve os
// dependentes deles, que precisam ser re-resolvidos, e os nomes que eles
// definiam, que podem desempatar refs ambíguas.
func (ix *Indexer) remove(paths []string, existing map[string]store.File) ([]int64, []string, error) {
	if len(paths) == 0 {
		return nil, nil, nil
	}
	var deps []int64
	var names []string
	for _, p := range paths {
		d, err := ix.store.Dependents(existing[p].ID)
		if err != nil {
			return nil, nil, err
		}
		deps = append(deps, d...)
		symbols, err := ix.store.SymbolsOfFile(existing[p].ID)
		if err != nil {
			return nil, nil, err
		}
		names = append(names, definitionNames(symbols...)...)
	}
	tx, err := ix.store.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	for _, p := range paths {
		if err := tx.DeleteFile(p); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return deps, names, nil
}

// resolutionSet é o que precisa ser resolvido de novo: os arquivos afetados;
// com arquivo novo, os que têm imports pendentes (ele pode ser o módulo que
// faltava); refs ambíguas e imports pendentes com um nome que mudou; e os do
// mesmo pacote de um arquivo novo ou com definições mudadas (resolução
// implícita, sem import). Um tsconfig alterado muda o destino de imports já
// resolvidos: resolve tudo.
func (ix *Indexer) resolutionSet(out written) ([]int64, error) {
	if out.configChanged {
		return ix.allFileIDs()
	}
	set := map[int64]bool{}
	add := func(ids []int64) {
		for _, id := range ids {
			set[id] = true
		}
	}
	add(out.affected)
	if out.newFiles {
		pending, err := ix.store.FilesWithPendingImports()
		if err != nil {
			return nil, err
		}
		add(pending)
	}
	if len(out.names) > 0 {
		names := make([]string, 0, len(out.names))
		for n := range out.names {
			names = append(names, n)
		}
		sort.Strings(names)
		ambiguous, err := ix.store.FilesWithAmbiguousRefsNamed(names)
		if err != nil {
			return nil, err
		}
		pending, err := ix.store.FilesWithPendingImportsNamed(names)
		if err != nil {
			return nil, err
		}
		add(ambiguous)
		add(pending)
	}
	for _, id := range out.packageFiles {
		f, ok, err := ix.store.FileByID(id)
		if err != nil {
			return nil, err
		}
		if !ok || f.Package == "" {
			continue
		}
		same, err := ix.store.FilesInPackage(f.Package, id)
		if err != nil {
			return nil, err
		}
		add(same)
	}
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func (ix *Indexer) allFileIDs() ([]int64, error) {
	files, err := ix.store.Files()
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(files))
	for _, f := range files {
		ids = append(ids, f.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// Status descreve o índice sem reindexar.
type Status struct {
	Root     string                      `json:"root"`
	Files    int                         `json:"files"`
	Symbols  int                         `json:"symbols"`
	Refs     int                         `json:"refs"`
	Imports  int                         `json:"imports"`
	Words    int                         `json:"words"`
	Edges    int                         `json:"edges"`
	ByLang   map[string]store.LangCounts `json:"by_language"`
	Outdated int                         `json:"outdated"` // arquivos novos/alterados/removidos desde a última indexação
}

func (ix *Indexer) Status() (Status, error) {
	total, edges, byLang, err := ix.store.Counts()
	if err != nil {
		return Status{}, err
	}
	files, err := walker.Walk(ix.root, ix.walkOptions())
	if err != nil {
		return Status{}, fmt.Errorf("walking %s: %w", ix.root, err)
	}
	existing, err := ix.existingByPath()
	if err != nil {
		return Status{}, err
	}
	var scratch Report
	jobs, removed := ix.plan(files, existing, false, &scratch)
	return Status{
		Root: ix.root, Files: total.Files, Symbols: total.Symbols, Refs: total.Refs,
		Imports: total.Imports, Words: total.Words, Edges: edges, ByLang: byLang,
		Outdated: len(jobs) + len(removed),
	}, nil
}

type phaseTimer struct {
	report *Report
	last   time.Time
}

func (p *phaseTimer) mark(name string) {
	if p.last.IsZero() {
		p.last = time.Now()
	}
	now := time.Now()
	p.report.Phases = append(p.report.Phases, Phase{Name: name, Duration: now.Sub(p.last)})
	p.last = now
}

// Refresh roda uma indexação incremental antes de uma consulta e diz se
// algo mudou. É o que mantém o índice fiel ao disco depois que o agente
// edita um arquivo: sem isso ele tenta verificar a própria edição num
// índice velho e gasta chamadas à toa.
func (ix *Indexer) Refresh(ctx context.Context) (Report, bool, error) {
	report, err := ix.Run(ctx, Options{})
	if err != nil {
		return report, false, err
	}
	return report, report.New+report.Changed+report.Removed > 0, nil
}
