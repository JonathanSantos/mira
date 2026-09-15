package resolve

import "github.com/JonathanSantos/mira/internal/store"

// lookups guarda, durante uma rodada de resolução, o que o banco já
// respondeu sobre arquivos e símbolos. Nessa rodada só as colunas de
// resolução mudam, então as respostas valem até o fim dela. A mesma pergunta
// chega milhares de vezes (os exports de um barril, os membros de um tipo), e
// cada consulta paga um lock do WAL: no App.tsx do excalidraw eram três
// quartos do tempo de resolver.
type lookups struct {
	fileByID           map[int64]fileHit
	symbolsOfFile      map[int64][]store.Symbol
	importsOfFile      map[int64][]store.Import
	symbolsByName      map[string][]store.Symbol
	symbolsByQualified map[string][]store.Symbol
	symbolsInPackage   map[[2]string][]store.Symbol
	membersInPackage   map[[3]string][]store.Symbol
	symbolsInContainer map[containerKey][]store.Symbol
	filesInPackage     map[packageKey][]int64
	packageExists      map[string]bool
	defaultBuild       map[int64]bool // o arquivo Go entra no build padrão
}

type fileHit struct {
	file store.File
	ok   bool
}

type containerKey struct {
	fileID          int64
	container, name string
}

type packageKey struct {
	pkg    string
	except int64
}

func newLookups() lookups {
	return lookups{
		fileByID:           map[int64]fileHit{},
		symbolsOfFile:      map[int64][]store.Symbol{},
		importsOfFile:      map[int64][]store.Import{},
		symbolsByName:      map[string][]store.Symbol{},
		symbolsByQualified: map[string][]store.Symbol{},
		symbolsInPackage:   map[[2]string][]store.Symbol{},
		membersInPackage:   map[[3]string][]store.Symbol{},
		symbolsInContainer: map[containerKey][]store.Symbol{},
		filesInPackage:     map[packageKey][]int64{},
		packageExists:      map[string]bool{},
		defaultBuild:       map[int64]bool{},
	}
}

// remember devolve a resposta guardada ou consulta e guarda. Um erro não é
// guardado: a próxima chamada tenta de novo.
func remember[K comparable, V any](m map[K]V, key K, load func() (V, error)) (V, error) {
	if v, ok := m[key]; ok {
		return v, nil
	}
	v, err := load()
	if err != nil {
		return v, err
	}
	m[key] = v
	return v, nil
}

func (r *Resolver) fileByID(id int64) (store.File, bool, error) {
	hit, err := remember(r.lookups.fileByID, id, func() (fileHit, error) {
		f, ok, err := r.store.FileByID(id)
		return fileHit{file: f, ok: ok}, err
	})
	return hit.file, hit.ok, err
}

func (r *Resolver) symbolsOfFile(fileID int64) ([]store.Symbol, error) {
	return remember(r.lookups.symbolsOfFile, fileID, func() ([]store.Symbol, error) { return r.store.SymbolsOfFile(fileID) })
}

func (r *Resolver) importsOfFile(fileID int64) ([]store.Import, error) {
	return remember(r.lookups.importsOfFile, fileID, func() ([]store.Import, error) { return r.store.ImportsOfFile(fileID) })
}

// symbolsByExportName filtra os símbolos do arquivo já carregados em vez de
// perguntar ao banco nome por nome: um barril é consultado com centenas deles.
func (r *Resolver) symbolsByExportName(fileID int64, name string) ([]store.Symbol, error) {
	symbols, err := r.symbolsOfFile(fileID)
	if err != nil {
		return nil, err
	}
	var out []store.Symbol
	for _, s := range symbols {
		if s.Exported && s.ExportName == name {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *Resolver) symbolsByName(name string) ([]store.Symbol, error) {
	return remember(r.lookups.symbolsByName, name, func() ([]store.Symbol, error) { return r.store.SymbolsByName(name) })
}

func (r *Resolver) symbolsByQualifiedName(qualified string) ([]store.Symbol, error) {
	return remember(r.lookups.symbolsByQualified, qualified, func() ([]store.Symbol, error) {
		return r.store.SymbolsByQualifiedName(qualified)
	})
}

func (r *Resolver) symbolsInPackage(pkg, name string) ([]store.Symbol, error) {
	return remember(r.lookups.symbolsInPackage, [2]string{pkg, name}, func() ([]store.Symbol, error) {
		return r.store.SymbolsInPackage(pkg, name)
	})
}

func (r *Resolver) membersInPackage(pkg, container, name string) ([]store.Symbol, error) {
	return remember(r.lookups.membersInPackage, [3]string{pkg, container, name}, func() ([]store.Symbol, error) {
		return r.store.MembersInPackage(pkg, container, name)
	})
}

func (r *Resolver) symbolsInContainer(fileID int64, container, name string) ([]store.Symbol, error) {
	return remember(r.lookups.symbolsInContainer, containerKey{fileID, container, name}, func() ([]store.Symbol, error) {
		return r.store.SymbolsInContainer(fileID, container, name)
	})
}

func (r *Resolver) filesInPackage(pkg string, except int64) ([]int64, error) {
	return remember(r.lookups.filesInPackage, packageKey{pkg, except}, func() ([]int64, error) {
		return r.store.FilesInPackage(pkg, except)
	})
}

func (r *Resolver) packageExists(pkg string) (bool, error) {
	return remember(r.lookups.packageExists, pkg, func() (bool, error) { return r.store.PackageExists(pkg) })
}
