package publish

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// FR-36 negative authority: the broker may create a draft PR and close it, but
// it may never merge, review, dismiss, tag, dispatch workflows, or mark a PR
// ready for review. This test reads every production Go file rather than
// trusting a code review, so a later edit cannot quietly widen the authority.
//
// The scan deliberately inspects string literals and composite-literal fields,
// not comments: a comment that names a forbidden endpoint is documentation, not
// a call. The two allowed mutations are therefore absent from the forbidden set:
//   - creating a PR with draft:true, and
//   - PATCH ... {"state":"closed"} to close it.
var forbiddenLiteralSubstrings = []string{
	"/merge",
	"/reviews",
	"dismissals",
	`"draft":false`,
	"refs/tags",
	"/git/tags",
	"/actions/workflows",
	"/dispatches",
	"/ready_for_review",
}

func TestProductionCodeHasNoForbiddenMutatingCalls(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source file")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))

	scanned := 0
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++

		src, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			return nil
		}
		parsed, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.BasicLit:
				if n.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(n.Value)
				if err != nil {
					return true
				}
				for _, forbidden := range forbiddenLiteralSubstrings {
					if strings.Contains(value, forbidden) {
						t.Errorf("%s: string literal contains forbidden endpoint %q", rel, forbidden)
					}
				}
			case *ast.KeyValueExpr:
				key, ok := n.Key.(*ast.Ident)
				if !ok || key.Name != "Draft" {
					return true
				}
				value, ok := n.Value.(*ast.Ident)
				if ok && value.Name == "false" {
					t.Errorf("%s: sets Draft: false; the broker may only create drafts", rel)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk production sources: %v", err)
	}
	if scanned == 0 {
		t.Fatal("scanned no production Go files; the authority check would pass vacuously")
	}
}
