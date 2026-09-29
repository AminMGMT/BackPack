package webui

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The contract between the panel and the server, checked from both sides.
//
// api.js is the only file that talks to the server, and most of what went
// wrong between the two was a request the server did not read the way it was
// sent: JSON posted to a handler that reads a form, so every field arrived
// empty and the save reported success (telegramSave, setPassword); a parameter
// the handler does not read (setChannel sent beta=1 to a handler that reads
// channel); a query the handler requires and nobody sent (every Random port
// button was a 400 without what=port). Each was found by hand, one screen at a
// time. TestEveryPanelAPIFunctionIsCalled proves every call is made; this
// proves every call is heard.
//
// paneltest/apicalls.mjs runs api.js under node with fetch replaced, calls every
// export with the arguments the views pass, and prints the requests. Each one
// is then held against the route table as Serve wires it and against the
// source of the handler that route reaches, followed through the functions and
// tables it refers to in this package and one step into the packages it calls:
//
//   - the path is a route of its own, not the catch-all that serves the panel;
//   - a JSON body reaches a handler that decodes JSON, and a form body one that
//     reads a form;
//   - every query parameter and body field is a name that handler reads, and a
//     JSON field has a type its Go field can take (a number into an int64, not
//     a string);
//   - the choices that pick a branch (action, what, channel, mode, end) are
//     choices it has a branch for;
//   - a method other than GET is one it accepts, when it checks at all.
//
// It reads source rather than running the handlers, because the handlers
// restart services, reach managed servers over SSH and install updates, and a
// test that did any of that to find out whether a field is read would be worse
// than the bug.
func TestEveryPanelCallReachesAHandlerThatReadsWhatItSends(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed, so api.js cannot be run here")
	}
	out, err := exec.Command(node, filepath.Join("paneltest", "apicalls.mjs")).Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("running api.js: %v\n%s", err, stderr)
	}
	var calls []panelCall
	if err := json.Unmarshal(out, &calls); err != nil {
		t.Fatalf("reading the requests api.js made: %v\n%s", err, out)
	}
	if len(calls) < 40 {
		t.Fatalf("api.js made only %d requests — the recorder is not reaching it", len(calls))
	}

	ix := newSourceIndex(t)
	home := ix.pkg(t, ".")
	handlers := home.routeHandlers(t)
	mux := newServer().routes()

	for _, c := range calls {
		what := c.Fn + ": " + c.Method + " " + c.Path
		_, pattern := mux.Handler(httptest.NewRequest(c.Method, c.Path, nil))
		if pattern != c.Path {
			t.Errorf("%s is not a route; the panel's catch-all %q answers it", what, pattern)
			continue
		}
		h, ok := handlers[c.Path]
		if !ok {
			t.Errorf("%s: could not find the handler registered for it in routes()", what)
			continue
		}
		reads := ix.closure(t, home, h)

		switch c.Body {
		case "json":
			if !reads.json {
				t.Errorf("%s posts JSON, but %s never decodes JSON — every field would arrive empty", what, h)
			}
		case "form":
			if !reads.form {
				t.Errorf("%s posts a form, but %s never reads one", what, h)
			}
		case "multipart":
			if !reads.multipart {
				t.Errorf("%s uploads a file, but %s never reads one", what, h)
			}
		case "none":
		default:
			t.Errorf("%s sends a body the recorder does not recognise (%s)", what, c.Body)
		}

		sent := c.Fields
		for _, q := range c.Query {
			sent = append(sent, [3]string{q[0], q[1], "string"})
		}
		for _, f := range sent {
			key, val, jsType := f[0], f[1], f[2]
			if c.Body == "json" {
				if goTypes, ok := reads.fields[strings.ToLower(key)]; !ok {
					t.Errorf("%s sends %q, which no struct %s decodes into has", what, key, h)
				} else if !fits(jsType, goTypes) {
					t.Errorf("%s sends %q as a %s, and %s decodes it into %v", what, key, jsType, h, sortedNames(goTypes))
				}
			} else if !reads.keys[key] {
				t.Errorf("%s sends %q, which %s does not read", what, key, h)
			}
			if branchKeys[key] && val != "" && !reads.names[val] {
				t.Errorf("%s sends %s=%q, and %s has no branch for it", what, key, val, h)
			}
		}

		if c.Method != http.MethodGet && len(reads.methods) > 0 && !reads.methods[c.Method] {
			t.Errorf("%s: %s checks the method and accepts only %v", what, h, sortedNames(reads.methods))
		}
	}
}

// panelCall is one request api.js made. A field is its name, its value when
// the value is a string, and its JavaScript type.
type panelCall struct {
	Fn     string      `json:"fn"`
	Method string      `json:"method"`
	Path   string      `json:"path"`
	Query  [][2]string `json:"query"`
	Body   string      `json:"body"`
	Fields [][3]string `json:"fields"`
}

// branchKeys are the parameters whose value selects what the handler does, so
// the value has to be one it knows as well as the name.
var branchKeys = map[string]bool{"action": true, "what": true, "channel": true, "mode": true, "end": true}

// fits reports whether a JSON value of jsType decodes into one of goTypes. A
// type that is not a plain Go one (a named type, a map, an interface) is given
// the benefit of the doubt.
func fits(jsType string, goTypes map[string]bool) bool {
	for g := range goTypes {
		switch {
		case g == "string":
			if jsType == "string" {
				return true
			}
		case g == "bool":
			if jsType == "boolean" {
				return true
			}
		case strings.HasPrefix(g, "int"), strings.HasPrefix(g, "uint"), strings.HasPrefix(g, "float"):
			if jsType == "number" {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// sourceIndex parses the module's packages as the closure reaches them.
type sourceIndex struct {
	root, module string
	pkgs         map[string]*goPackage
}

// goPackage is one package's non-test source, indexed by name: functions,
// methods and package-level variables share one namespace here. A clash only
// makes a closure larger, which can hide an unread name but never invent one.
type goPackage struct {
	decls   map[string][]goDecl
	types   map[string]*ast.StructType
	helpers map[string]int // see keyHelpers
}

// goDecl is a function body or a package-level variable's value, with the
// imports of the file it is in.
type goDecl struct {
	node    ast.Node
	fn      *ast.FuncDecl // nil for a variable
	imports map[string]string
}

func newSourceIndex(t *testing.T) *sourceIndex {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(mod), "\n")
	return &sourceIndex{root: root, module: strings.TrimSpace(strings.TrimPrefix(first, "module")), pkgs: map[string]*goPackage{}}
}

// pkg parses the package in dir, relative to this one, once.
func (ix *sourceIndex) pkg(t *testing.T, dir string) *goPackage {
	t.Helper()
	if p, ok := ix.pkgs[dir]; ok {
		return p
	}
	p := &goPackage{decls: map[string][]goDecl{}, types: map[string]*ast.StructType{}}
	ix.pkgs[dir] = p
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		imports := map[string]string{}
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			local := path[strings.LastIndex(path, "/")+1:]
			if im.Name != nil {
				local = im.Name.Name
			}
			imports[local] = path
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Body != nil {
					p.decls[d.Name.Name] = append(p.decls[d.Name.Name], goDecl{d.Body, d, imports})
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						if st, ok := s.Type.(*ast.StructType); ok {
							p.types[s.Name.Name] = st
						}
					case *ast.ValueSpec:
						for i, n := range s.Names {
							if i < len(s.Values) {
								p.decls[n.Name] = append(p.decls[n.Name], goDecl{s.Values[i], nil, imports})
							}
						}
					}
				}
			}
		}
	}
	p.helpers = p.keyHelpers()
	return p
}

// guards are the wrappers in routes(); the handler is what they wrap.
var guards = map[string]bool{"requireAuth": true, "requireReadAuth": true, "requireAdmin": true, "guard": true}

// routeHandlers reads routes() and maps each path to the handler it wraps.
func (p *goPackage) routeHandlers(t *testing.T) map[string]string {
	t.Helper()
	decls := p.decls["routes"]
	if len(decls) != 1 {
		t.Fatalf("expected one routes(), found %d", len(decls))
	}
	out := map[string]string{}
	ast.Inspect(decls[0].node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		path, _ := strconv.Unquote(lit.Value)
		ast.Inspect(call.Args[1], func(n ast.Node) bool {
			var name string
			switch n := n.(type) {
			case *ast.SelectorExpr:
				name = n.Sel.Name
			case *ast.Ident:
				name = n.Name
			}
			if name != "" && !guards[name] && p.decls[name] != nil {
				out[path] = name
			}
			return true
		})
		return false
	})
	return out
}

// handlerReads is what a handler and everything it reaches could read from a
// request.
type handlerReads struct {
	names                 map[string]bool            // string literals
	keys                  map[string]bool            // names read from the form or the query
	fields                map[string]map[string]bool // json field name (lower case) → Go types
	methods               map[string]bool            // the HTTP methods it names
	json, form, multipart bool
}

// closure follows name through what it refers to: everything in its own
// package, and one step into another package of this module.
func (ix *sourceIndex) closure(t *testing.T, home *goPackage, name string) handlerReads {
	r := handlerReads{names: map[string]bool{}, keys: map[string]bool{}, fields: map[string]map[string]bool{}, methods: map[string]bool{}}
	type item struct {
		p       *goPackage
		name    string
		foreign bool
	}
	seen := map[item]bool{}
	queue := []item{{home, name, false}}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		if seen[it] {
			continue
		}
		seen[it] = true
		for _, d := range it.p.decls[it.name] {
			other := func(x ast.Expr) *goPackage {
				id, ok := x.(*ast.Ident)
				if !ok || it.foreign {
					return nil
				}
				path, ok := d.imports[id.Name]
				if !ok || !strings.HasPrefix(path, ix.module+"/") {
					return nil
				}
				return ix.pkg(t, filepath.Join(ix.root, strings.TrimPrefix(path, ix.module+"/")))
			}
			// JSON counts only where the request body is in the same
			// function: reading a state file is decoding JSON too.
			decodes, body := false, false
			ast.Inspect(d.node, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					arg := -1
					switch f := n.Fun.(type) {
					case *ast.SelectorExpr:
						if formReaders[f.Sel.Name] {
							arg = 0
						} else if i, ok := it.p.helpers[f.Sel.Name]; ok {
							arg = i
						}
					case *ast.Ident:
						if i, ok := it.p.helpers[f.Name]; ok {
							arg = i
						}
					}
					if arg >= 0 && arg < len(n.Args) {
						if lit, ok := n.Args[arg].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							k, _ := strconv.Unquote(lit.Value)
							r.keys[k] = true
						}
					}
				case *ast.IndexExpr:
					if sel, ok := n.X.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Form" || sel.Sel.Name == "PostForm") {
						if lit, ok := n.Index.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							k, _ := strconv.Unquote(lit.Value)
							r.keys[k] = true
						}
					}
				case *ast.BasicLit:
					if n.Kind == token.STRING {
						if s, err := strconv.Unquote(n.Value); err == nil {
							r.names[s] = true
						}
					}
				case *ast.StructType:
					addFields(n, r.fields)
				case *ast.SelectorExpr:
					sel := n.Sel.Name
					if x, ok := n.X.(*ast.Ident); ok {
						switch {
						case x.Name == "http" && strings.HasPrefix(sel, "Method"):
							r.methods[strings.ToUpper(strings.TrimPrefix(sel, "Method"))] = true
						case x.Name == "json" && (sel == "NewDecoder" || sel == "Unmarshal"):
							decodes = true
						}
					}
					if sel == "Body" {
						body = true
					}
					switch sel {
					case "FormValue", "PostFormValue", "ParseForm", "Form", "PostForm":
						r.form = true
					case "FormFile", "ParseMultipartForm", "MultipartForm", "MultipartReader":
						r.multipart = true
					}
					if op := other(n.X); op != nil {
						if op.decls[sel] != nil {
							queue = append(queue, item{op, sel, true})
						}
						if st := op.types[sel]; st != nil {
							addFields(st, r.fields)
						}
					} else if it.p.decls[sel] != nil {
						queue = append(queue, item{it.p, sel, it.foreign})
					}
				case *ast.Ident:
					if it.p.decls[n.Name] != nil {
						queue = append(queue, item{it.p, n.Name, it.foreign})
					}
					if st := it.p.types[n.Name]; st != nil {
						addFields(st, r.fields)
					}
				}
				return true
			})
			if decodes && body {
				r.json = true
			}
		}
	}
	return r
}

// formReaders are the calls whose first argument names a form or query field:
// r.FormValue("k"), r.URL.Query().Get("k"), r.Form.Get("k"), and the like.
var formReaders = map[string]bool{"FormValue": true, "PostFormValue": true, "Get": true, "Has": true, "FormFile": true}

// keyHelpers finds the functions of a package that take a field's name and
// read it — formBool(r, "alertsEnabled", …) — and which argument the name is.
func (p *goPackage) keyHelpers() map[string]int {
	out := map[string]int{}
	for name, ds := range p.decls {
		for _, d := range ds {
			if d.fn == nil {
				continue
			}
			var params []string
			for _, f := range d.fn.Type.Params.List {
				for _, n := range f.Names {
					params = append(params, n.Name)
				}
			}
			ast.Inspect(d.node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !formReaders[sel.Sel.Name] {
					return true
				}
				if id, ok := call.Args[0].(*ast.Ident); ok {
					for i, pn := range params {
						if pn == id.Name {
							out[name] = i
						}
					}
				}
				return true
			})
		}
	}
	return out
}

// addFields records the JSON name and Go type of each field of a struct.
// encoding/json matches names ignoring case, so they are kept in lower case.
func addFields(st *ast.StructType, fields map[string]map[string]bool) {
	for _, f := range st.Fields.List {
		goType := ""
		if id, ok := f.Type.(*ast.Ident); ok {
			goType = id.Name
		}
		var names []string
		if f.Tag != nil {
			tag, _ := strconv.Unquote(f.Tag.Value)
			if name, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ","); name != "" && name != "-" {
				names = append(names, name)
			}
		}
		if names == nil {
			for _, n := range f.Names {
				names = append(names, n.Name)
			}
		}
		for _, n := range names {
			n = strings.ToLower(n)
			if fields[n] == nil {
				fields[n] = map[string]bool{}
			}
			fields[n][goType] = true
		}
	}
}

func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
