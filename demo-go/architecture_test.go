package demo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Demo applications own TUI state and protocols, never a display connection.
func TestNoDisplayBackendDependencies(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			name, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(name, "github.com/BurntSushi/xgb") || strings.Contains(name, "/x11") || strings.Contains(name, "/wayland") {
				t.Errorf("%s imports display backend %s", path, name)
			}
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if selector.Sel.Name != "Getenv" && selector.Sel.Name != "LookupEnv" {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				return true
			}
			name, _ := strconv.Unquote(literal.Value)
			switch name {
			case "DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "WINDOWID":
				t.Errorf("%s assumes a display backend via %s", path, name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
