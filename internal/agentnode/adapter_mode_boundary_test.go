package agentnode

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestAdapterModeCompatibilityBoundary(t *testing.T) {
	supported := []string{"http", "openclaw", "command", "a2a", "codex"}
	explicitlyRejected := map[string]string{
		"module": "module adapter is not supported by the Go agent node; use http, command, openclaw, a2a, or codex",
	}

	get := func(string) string { return "" }
	for _, mode := range supported {
		t.Run("supported/"+mode, func(t *testing.T) {
			adapter, err := adapterFromEnv(get, mode)
			if err != nil {
				t.Fatalf("adapterFromEnv(%q) returned an error: %v", mode, err)
			}
			if adapter == nil {
				t.Fatalf("adapterFromEnv(%q) returned a nil adapter without an error", mode)
			}
		})
	}
	for mode, errorFragment := range explicitlyRejected {
		t.Run("explicitly_rejected/"+mode, func(t *testing.T) {
			adapter, err := adapterFromEnv(get, mode)
			if err == nil {
				t.Fatalf("adapterFromEnv(%q) returned adapter %T without an error", mode, adapter)
			}
			if adapter != nil {
				t.Fatalf("adapterFromEnv(%q) returned adapter %T on rejection", mode, adapter)
			}
			if got := err.Error(); got != errorFragment {
				t.Fatalf("adapterFromEnv(%q) error = %q, want %q", mode, got, errorFragment)
			}
			if strings.Contains(err.Error(), "unsupported OPENLINKER_AGENT_NODE_ADAPTER=") {
				t.Fatalf("adapterFromEnv(%q) used the generic unsupported-mode error: %v", mode, err)
			}
		})
	}
	t.Run("unknown_rejected", func(t *testing.T) {
		const mode = "architecture-boundary-unknown"
		adapter, err := adapterFromEnv(get, mode)
		if err == nil {
			t.Fatalf("adapterFromEnv(%q) returned adapter %T without an error", mode, adapter)
		}
		if adapter != nil {
			t.Fatalf("adapterFromEnv(%q) returned adapter %T on rejection", mode, adapter)
		}
		if got, want := err.Error(), "unsupported OPENLINKER_AGENT_NODE_ADAPTER="+mode; got != want {
			t.Fatalf("adapterFromEnv(%q) error = %q, want %q", mode, got, want)
		}
	})

	wantSwitchModes := make([]string, 0, len(supported)+len(explicitlyRejected))
	wantSwitchModes = append(wantSwitchModes, supported...)
	for mode := range explicitlyRejected {
		wantSwitchModes = append(wantSwitchModes, mode)
	}
	sort.Strings(wantSwitchModes)

	gotSwitchModes, hasDefault := adapterModeSwitchCases(t)
	if !reflect.DeepEqual(gotSwitchModes, wantSwitchModes) {
		t.Fatalf("adapterFromEnv switch modes = %v, want exact compatibility surface %v; adding an adapter mode requires an explicit architecture decision", gotSwitchModes, wantSwitchModes)
	}
	if !hasDefault {
		t.Fatal("adapterFromEnv switch must retain its default rejection branch")
	}
}

func adapterModeSwitchCases(t *testing.T) ([]string, bool) {
	t.Helper()

	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, "config.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	var adapterFunction *ast.FuncDecl
	var modeSwitch *ast.SwitchStmt
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "adapterFromEnv" || function.Body == nil {
			continue
		}
		if adapterFunction != nil {
			t.Fatal("config.go contains more than one adapterFromEnv function")
		}
		adapterFunction = function
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switchStatement, ok := node.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			identifier, ok := switchStatement.Tag.(*ast.Ident)
			if ok && identifier.Name == "mode" {
				if modeSwitch != nil {
					t.Fatalf("adapterFromEnv contains more than one switch over mode")
				}
				modeSwitch = switchStatement
				return false
			}
			return true
		})
	}
	if adapterFunction == nil {
		t.Fatal("config.go is missing adapterFromEnv")
	}
	if modeSwitch == nil {
		t.Fatal("adapterFromEnv must dispatch through a switch over mode")
	}
	if modeSwitch.Init != nil {
		t.Fatal("adapterFromEnv mode switch must not normalize or rewrite mode in an init statement")
	}
	if len(adapterFunction.Body.List) == 0 || adapterFunction.Body.List[len(adapterFunction.Body.List)-1] != modeSwitch {
		t.Fatal("adapterFromEnv switch over mode must be its final top-level dispatch statement")
	}
	for _, statement := range adapterFunction.Body.List[:len(adapterFunction.Body.List)-1] {
		ast.Inspect(statement, func(node ast.Node) bool {
			identifier, ok := node.(*ast.Ident)
			if ok && identifier.Name == "mode" {
				t.Fatalf("adapterFromEnv must not inspect or rewrite mode outside its final switch (found at %s)", files.Position(identifier.Pos()))
			}
			return true
		})
	}

	var modes []string
	hasDefault := false
	var defaultClause *ast.CaseClause
	var moduleClause *ast.CaseClause
	var httpOpenClawClause []string
	for _, statement := range modeSwitch.Body.List {
		clause, ok := statement.(*ast.CaseClause)
		if !ok {
			t.Fatalf("adapterFromEnv mode switch contains unexpected statement %T", statement)
		}
		if clause.List == nil {
			if defaultClause != nil {
				t.Fatal("adapterFromEnv mode switch contains more than one default clause")
			}
			hasDefault = true
			defaultClause = clause
			continue
		}
		var clauseModes []string
		for _, expression := range clause.List {
			literal, ok := expression.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				t.Fatalf("adapterFromEnv mode case at %s must be a string literal", files.Position(expression.Pos()))
			}
			mode, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatalf("invalid adapter mode literal at %s: %v", files.Position(literal.Pos()), err)
			}
			modes = append(modes, mode)
			clauseModes = append(clauseModes, mode)
		}
		if containsMode(clauseModes, "http") || containsMode(clauseModes, "openclaw") {
			if len(httpOpenClawClause) > 0 {
				t.Fatal("http/openclaw compatibility modes must share one case clause")
			}
			httpOpenClawClause = append(httpOpenClawClause, clauseModes...)
		}
		if containsMode(clauseModes, "module") {
			if moduleClause != nil {
				t.Fatal("module compatibility mode must have exactly one rejection clause")
			}
			moduleClause = clause
		}
	}
	sort.Strings(httpOpenClawClause)
	if !reflect.DeepEqual(httpOpenClawClause, []string{"http", "openclaw"}) {
		t.Fatalf("http/openclaw case grouping = %v, want [http openclaw]", httpOpenClawClause)
	}
	assertExactModuleRejection(t, moduleClause)
	assertExactUnknownModeDefault(t, defaultClause)
	sort.Strings(modes)
	return modes, hasDefault
}

func assertExactModuleRejection(t *testing.T, clause *ast.CaseClause) {
	t.Helper()
	if clause == nil || len(clause.Body) != 1 {
		t.Fatal("module case must contain exactly one unconditional rejection return")
	}
	result, ok := clause.Body[0].(*ast.ReturnStmt)
	if !ok || len(result.Results) != 2 {
		t.Fatal("module case must return exactly nil and an explicit error")
	}
	first, ok := result.Results[0].(*ast.Ident)
	if !ok || first.Name != "nil" {
		t.Fatal("module case must return a nil Adapter")
	}
	call, ok := result.Results[1].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		t.Fatal("module case must construct one unconditional literal error")
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Errorf" {
		t.Fatal("module case must construct its rejection with fmt.Errorf")
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok || packageName.Name != "fmt" {
		t.Fatal("module case must construct its rejection with fmt.Errorf")
	}
	message, ok := call.Args[0].(*ast.BasicLit)
	if !ok || message.Kind != token.STRING {
		t.Fatal("module case must use a literal rejection message")
	}
	messageValue, err := strconv.Unquote(message.Value)
	if err != nil || !strings.Contains(messageValue, "module adapter is not supported") {
		t.Fatalf("module rejection message = %q, want an explicit unsupported error", message.Value)
	}
}

func containsMode(modes []string, target string) bool {
	for _, mode := range modes {
		if mode == target {
			return true
		}
	}
	return false
}

func assertExactUnknownModeDefault(t *testing.T, clause *ast.CaseClause) {
	t.Helper()
	if clause == nil || len(clause.Body) != 1 {
		t.Fatal("adapterFromEnv default must contain exactly one rejection return")
	}
	result, ok := clause.Body[0].(*ast.ReturnStmt)
	if !ok || len(result.Results) != 2 {
		t.Fatal("adapterFromEnv default must return exactly nil and the unsupported-mode error")
	}
	first, ok := result.Results[0].(*ast.Ident)
	if !ok || first.Name != "nil" {
		t.Fatal("adapterFromEnv default must return a nil Adapter")
	}
	call, ok := result.Results[1].(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		t.Fatal("adapterFromEnv default must call fmt.Errorf with the exact mode error")
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Errorf" {
		t.Fatal("adapterFromEnv default must call fmt.Errorf")
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok || packageName.Name != "fmt" {
		t.Fatal("adapterFromEnv default must call fmt.Errorf")
	}
	format, ok := call.Args[0].(*ast.BasicLit)
	if !ok || format.Kind != token.STRING {
		t.Fatal("adapterFromEnv default must use a literal unsupported-mode format")
	}
	formatValue, err := strconv.Unquote(format.Value)
	if err != nil || formatValue != "unsupported OPENLINKER_AGENT_NODE_ADAPTER=%s" {
		t.Fatalf("adapterFromEnv default format = %q, want exact unsupported-mode format", format.Value)
	}
	mode, ok := call.Args[1].(*ast.Ident)
	if !ok || mode.Name != "mode" {
		t.Fatal("adapterFromEnv default must report the rejected mode")
	}
}
