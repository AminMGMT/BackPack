// Command mutate runs mutation testing over a package.
//
// Coverage says a line ran. It does not say anything about whether a test would
// have noticed had that line been wrong, and a test that exercises code without
// asserting on the result raises coverage by exactly as much as one that checks
// everything. The packages most at risk of this are the ones already reported as
// well covered, because nothing else will ever look at them again.
//
// Mutation testing answers the question coverage cannot: change the source in a
// small, plausible way and see whether the suite fails. A mutant the tests kill
// proves an assertion exists. A mutant that survives is a statement about the
// tests, and it is nearly always one of three things — an assertion that checks
// the shape of a result rather than its value, a branch that is entered but
// whose effect is never observed, or a boundary nobody tested on both sides of.
//
//	go run ./tools/mutate ./internal/tunnel/chain
//	go run ./tools/mutate -max 40 -v ./internal/tunnel/l3
//
// The source tree is never written to. Each mutant is compiled through
// `go test -overlay`, which substitutes file contents for one run, so an
// interrupted run leaves nothing behind to clean up.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	var (
		maxMutants = flag.Int("max", 0, "stop after this many mutants (0 = all)")
		timeout    = flag.Duration("timeout", 90*time.Second, "per-mutant test timeout")
		verbose    = flag.Bool("v", false, "print every mutant, not only the survivors")
		seed       = flag.Int64("seed", 1, "shuffle order, so -max samples the whole package")
		minScore   = flag.Float64("min", 0, "exit non-zero if the mutation score is below this percentage")
	)
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mutate [flags] <package>")
		flag.PrintDefaults()
		os.Exit(2)
	}
	pkg := flag.Arg(0)

	if err := run(pkg, *maxMutants, *timeout, *verbose, *seed, *minScore); err != nil {
		fmt.Fprintln(os.Stderr, "mutate:", err)
		os.Exit(1)
	}
}

func run(pkg string, maxMutants int, timeout time.Duration, verbose bool, seed int64, minScore float64) error {
	dir, err := packageDir(pkg)
	if err != nil {
		return err
	}

	// A suite that is already failing makes every mutant look dead, which is the
	// one result that is worse than no result: it reports a perfect score for a
	// package nobody is testing.
	fmt.Printf("checking the suite passes unmutated… ")
	if err := goTest(pkg, "", timeout); err != nil {
		fmt.Println("no")
		return fmt.Errorf("the tests fail before anything is mutated: %w", err)
	}
	fmt.Println("yes")

	mutants, err := plan(dir)
	if err != nil {
		return err
	}
	if len(mutants) == 0 {
		return fmt.Errorf("no mutable expressions found in %s", dir)
	}

	total := len(mutants)
	rand.New(rand.NewSource(seed)).Shuffle(len(mutants), func(i, j int) {
		mutants[i], mutants[j] = mutants[j], mutants[i]
	})
	if maxMutants > 0 && maxMutants < len(mutants) {
		mutants = mutants[:maxMutants]
	}

	work, err := os.MkdirTemp("", "mutate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	fmt.Printf("%d mutants in %s", len(mutants), pkg)
	if len(mutants) < total {
		fmt.Printf(" (sampled from %d)", total)
	}
	fmt.Println()

	var survivors []mutant
	killed, errored := 0, 0
	for i, m := range mutants {
		overlay, err := writeMutant(work, i, m)
		if err != nil {
			return err
		}
		err = goTest(pkg, overlay, timeout)
		switch {
		case err == nil:
			survivors = append(survivors, m)
			fmt.Printf("SURVIVED  %s\n", m)
		case isBuildFailure(err):
			// A mutation that does not compile says nothing about the tests. It
			// is not a survivor and it is not a kill.
			errored++
			if verbose {
				fmt.Printf("uncompilable %s\n", m)
			}
		default:
			killed++
			if verbose {
				fmt.Printf("killed    %s\n", m)
			}
		}
	}

	graded := killed + len(survivors)
	fmt.Println()
	fmt.Printf("%d killed, %d survived", killed, len(survivors))
	if errored > 0 {
		fmt.Printf(", %d did not compile and were not counted", errored)
	}
	fmt.Println()
	if graded == 0 {
		return fmt.Errorf("every mutant failed to compile — nothing was measured")
	}
	score := 100 * float64(killed) / float64(graded)
	fmt.Printf("mutation score: %.1f%%\n", score)

	if len(survivors) > 0 {
		fmt.Println("\nSurvivors are the interesting part — each one is a change the tests")
		fmt.Println("did not notice:")
		sort.Slice(survivors, func(i, j int) bool { return survivors[i].pos < survivors[j].pos })
		for _, m := range survivors {
			fmt.Printf("  %s\n", m)
		}
	}

	if minScore > 0 && score < minScore {
		return fmt.Errorf("mutation score %.1f%% is below the floor of %.1f%%", score, minScore)
	}
	return nil
}

// mutant is one source change: a file, the expression replaced, and the bytes
// that replace the whole file when it is under test.
type mutant struct {
	file    string // absolute path of the original
	pos     string // file:line:col, for the report
	op      string // what was changed, e.g. "< -> <="
	content []byte // the entire mutated file
}

func (m mutant) String() string { return fmt.Sprintf("%s: %s", m.pos, m.op) }

// packageDir resolves a package pattern to the directory holding its files.
func packageDir(pkg string) (string, error) {
	out, err := exec.Command("go", "list", "-f", "{{.Dir}}", pkg).Output()
	if err != nil {
		return "", fmt.Errorf("go list %s: %w", pkg, err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", fmt.Errorf("%s resolved to no directory", pkg)
	}
	return dir, nil
}

// plan parses every non-test file in dir and enumerates the mutations.
//
// Test files are deliberately excluded: mutating a test measures nothing, and
// mutating a test helper measures the wrong thing.
func plan(dir string) ([]mutant, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var out []mutant
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		ms, err := planFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		out = append(out, ms...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pos < out[j].pos })
	return out, nil
}

// binaryOps are the substitutions applied to a binary expression. Each one is a
// mistake somebody could actually make: an off-by-one on a boundary, an
// inverted comparison, the wrong connective, the wrong sign.
var binaryOps = map[token.Token][]token.Token{
	token.LSS:  {token.LEQ, token.GTR},
	token.LEQ:  {token.LSS, token.GEQ},
	token.GTR:  {token.GEQ, token.LSS},
	token.GEQ:  {token.GTR, token.LEQ},
	token.EQL:  {token.NEQ},
	token.NEQ:  {token.EQL},
	token.LAND: {token.LOR},
	token.LOR:  {token.LAND},
	token.ADD:  {token.SUB},
	token.SUB:  {token.ADD},
	token.MUL:  {token.QUO},
	token.QUO:  {token.MUL},
}

func planFile(path string) ([]mutant, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	// Collect the edits first, then apply them one at a time: mutating the tree
	// while walking it would compound.
	type edit struct {
		apply  func()
		revert func()
		pos    token.Pos
		op     string
	}
	var edits []edit

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.BinaryExpr:
			for _, to := range binaryOps[node.Op] {
				from, to := node.Op, to
				edits = append(edits, edit{
					apply:  func() { node.Op = to },
					revert: func() { node.Op = from },
					pos:    node.OpPos,
					op:     fmt.Sprintf("%s -> %s", from, to),
				})
			}
		case *ast.BasicLit:
			// Integer boundaries: 0 <-> 1 and n -> n+1 catch the fencepost that
			// a single-value test cannot.
			if node.Kind != token.INT {
				return true
			}
			v, err := strconv.Atoi(node.Value)
			if err != nil {
				return true
			}
			old := node.Value
			next := strconv.Itoa(v + 1)
			edits = append(edits, edit{
				apply:  func() { node.Value = next },
				revert: func() { node.Value = old },
				pos:    node.ValuePos,
				op:     fmt.Sprintf("%s -> %s", old, next),
			})
		}
		return true
	})

	var out []mutant
	for _, e := range edits {
		e.apply()
		var buf strings.Builder
		err := printer.Fprint(&buf, fset, file)
		e.revert()
		if err != nil {
			return nil, err
		}
		out = append(out, mutant{
			file:    path,
			pos:     fset.Position(e.pos).String(),
			op:      e.op,
			content: []byte(buf.String()),
		})
	}
	return out, nil
}

// writeMutant materialises one mutant and the overlay that points the compiler
// at it, and returns the overlay path.
func writeMutant(work string, i int, m mutant) (string, error) {
	src := filepath.Join(work, fmt.Sprintf("m%d_%s", i, filepath.Base(m.file)))
	if err := os.WriteFile(src, m.content, 0o600); err != nil {
		return "", err
	}
	overlay := filepath.Join(work, fmt.Sprintf("m%d.json", i))
	doc := struct {
		Replace map[string]string
	}{Replace: map[string]string{m.file: src}}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return overlay, os.WriteFile(overlay, b, 0o600)
}

// goTest runs the package's tests, optionally through an overlay. Caching is off
// because every run has different source behind the same package path.
func goTest(pkg, overlay string, timeout time.Duration) error {
	args := []string{"test", "-count=1", "-timeout", timeout.String()}
	if overlay != "" {
		args = append(args, "-overlay", overlay)
	}
	args = append(args, pkg)
	cmd := exec.Command("go", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return testFailure{out: string(out), err: err}
	}
	return nil
}

type testFailure struct {
	out string
	err error
}

func (f testFailure) Error() string { return f.err.Error() + "\n" + f.out }

// isBuildFailure reports whether the run died compiling rather than asserting.
// A comparison between types that only supports equality, an integer literal
// used as an array size, a constant that overflows: all of these produce a
// mutant that says nothing about the tests.
func isBuildFailure(err error) bool {
	f, ok := err.(testFailure)
	if !ok {
		return false
	}
	return strings.Contains(f.out, "[build failed]") ||
		strings.Contains(f.out, "cannot use") ||
		strings.Contains(f.out, "invalid operation") ||
		strings.Contains(f.out, "syntax error") ||
		strings.Contains(f.out, "declared and not used") ||
		strings.Contains(f.out, "overflows")
}
