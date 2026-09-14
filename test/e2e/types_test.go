package e2e

// Espelhos mínimos dos tipos JSON da CLI: os testes decodificam só o que
// verificam, para não depender dos structs internos.

type symbolInfo struct {
	Name          string   `json:"name"`
	QualifiedName string   `json:"qualified_name"`
	Kind          string   `json:"kind"`
	Exported      bool     `json:"exported"`
	JSX           bool     `json:"jsx"`
	Signature     string   `json:"signature"`
	Annotations   []string `json:"annotations"`
	File          string   `json:"file"`
	StartLine     int      `json:"start_line"`
	EndLine       int      `json:"end_line"`
}

type related struct {
	symbolRef
	Callers []related `json:"callers"`
	Callees []related `json:"callees"`
}

type definition struct {
	symbolInfo
	Snippet       string      `json:"snippet"`
	Body          string      `json:"body"`
	Callers       []related   `json:"callers"`
	Callees       []related   `json:"callees"`
	Implements    []symbolRef `json:"implements"`
	ImplementedBy []symbolRef `json:"implemented_by"`
}

type resolveResult struct {
	Query       string       `json:"query"`
	Definitions []definition `json:"definitions"`
}

type symbolRef struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

type reference struct {
	Line       int    `json:"line"`
	Kind       string `json:"kind"`
	Resolution string `json:"resolution"` // vazio = resolvido
	Target     string `json:"target"`
	Container  string `json:"container"`
	Text       string `json:"text"`
}

type refFile struct {
	File string      `json:"file"`
	Refs []reference `json:"refs"`
}

type refsResult struct {
	Name       string         `json:"name"`
	Total      int            `json:"total"`
	Targets    []symbolRef    `json:"targets"`
	Candidates []symbolRef    `json:"candidates"`
	Files      []refFile      `json:"files"`
	Summary    map[string]int `json:"summary"`
	ByKind     map[string]int `json:"by_kind"`
}

// flat devolve as refs com o arquivo em cada uma, para os testes antigos.
func (r refsResult) flat() []flatRef {
	var out []flatRef
	for _, f := range r.Files {
		for _, ref := range f.Refs {
			res := ref.Resolution
			if res == "" {
				res = "resolved"
			}
			out = append(out, flatRef{File: f.File, Line: ref.Line, Kind: ref.Kind, Resolution: res, Target: ref.Target})
		}
	}
	return out
}

type flatRef struct {
	File, Kind, Resolution, Target string
	Line                           int
}

type langCounts struct {
	Files   int `json:"files"`
	Symbols int `json:"symbols"`
	Refs    int `json:"refs"`
	Imports int `json:"imports"`
}

type status struct {
	Files    int                   `json:"files"`
	Symbols  int                   `json:"symbols"`
	Refs     int                   `json:"refs"`
	Edges    int                   `json:"edges"`
	ByLang   map[string]langCounts `json:"by_language"`
	Outdated int                   `json:"outdated"`
}

type indexReport struct {
	Walked   int `json:"walked"`
	New      int `json:"new"`
	Changed  int `json:"changed"`
	Rehashed int `json:"rehashed"`
	Removed  int `json:"removed"`
	Phases   []struct {
		Name string `json:"name"`
	} `json:"phases"`
}

type searchResult struct {
	Total int `json:"total"`
	Files []struct {
		File  string `json:"file"`
		Count int    `json:"count"`
		Hits  []struct {
			Line       int    `json:"line"`
			Text       string `json:"text"`
			Definition bool   `json:"definition"`
			Kind       string `json:"kind"`
		} `json:"hits"`
	} `json:"files"`
}

type outline struct {
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	StartLine   int       `json:"start_line"`
	EndLine     int       `json:"end_line"`
	Annotations []string  `json:"annotations"`
	Signature   string    `json:"signature"`
	Children    []outline `json:"children"`
}

type symbolsResult struct {
	File       string    `json:"file"`
	Count      int       `json:"count"`
	ParseError bool      `json:"parse_error"`
	Symbols    []outline `json:"symbols"`
}

type dirEntry struct {
	Path    string      `json:"path"`
	Files   int         `json:"files"`
	Symbols int         `json:"symbols"`
	Dirs    []dirEntry  `json:"dirs"`
	Entries []fileEntry `json:"entries"`
}

type fileEntry struct {
	Path    string `json:"path"`
	Lang    string `json:"lang"`
	Symbols int    `json:"symbols"`
}

type filesResult struct {
	Root    string   `json:"root"`
	Files   int      `json:"files"`
	Symbols int      `json:"symbols"`
	Tree    dirEntry `json:"tree"`
}

type snippetResult struct {
	Text      string `json:"text"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}
