package agentnode

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentNodeRuntimeBoundary(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbiddenFiles := []string{
		"assignment_journal", "assignment_payload", "runtime_attempt",
		"runtime_cancel", "runtime_client", "runtime_discovery",
		"runtime_identity", "runtime_spool", "runtime_transport", "spool",
	}
	forbiddenDeclarations := []string{
		"RuntimeClient", "RuntimeSession", "AttemptIdentity",
		"AssignmentJournal", "TransportSupervisor", "RuntimeSpool",
	}

	files := token.NewFileSet()
	usesSDKWorker := false
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		lowerName := strings.ToLower(name)
		for _, forbidden := range forbiddenFiles {
			if strings.Contains(lowerName, forbidden) {
				t.Errorf("Agent Node owns forbidden Runtime implementation file %q", name)
			}
		}
		parsed, parseErr := parser.ParseFile(files, name, nil, 0)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.TypeSpec:
				for _, forbidden := range forbiddenDeclarations {
					if strings.Contains(value.Name.Name, forbidden) {
						t.Errorf("Agent Node declares SDK-owned Runtime type %s (%s)", value.Name.Name, files.Position(value.Pos()))
					}
				}
			case *ast.CallExpr:
				selector, ok := value.Fun.(*ast.SelectorExpr)
				if ok && selector.Sel.Name == "NewRuntimeWorker" {
					usesSDKWorker = true
				}
			}
			return true
		})
	}
	if !usesSDKWorker {
		t.Fatal("Agent Node must start through openlinker.NewRuntimeWorker")
	}
}
