package agentnode

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

func TestAgentNodeRuntimeBoundary(t *testing.T) {
	forbiddenFiles := []string{
		"a2a_push", "a2a_state", "a2a_task", "assignment_journal", "assignment_payload", "push_store", "task_store",
		"runtime_attempt",
		"runtime_cancel", "runtime_client", "runtime_discovery",
		"runtime_identity", "runtime_spool", "runtime_transport", "spool",
		"runtime_lease", "runtime_presence", "runtime_protocol", "runtime_resume",
		"runtime_session", "runtime_websocket",
	}
	forbiddenDeclarations := []string{
		"RuntimeClient", "RuntimeSession", "AttemptIdentity",
		"AssignmentJournal", "TransportSupervisor", "RuntimeSpool",
		"RuntimeLeaseService", "RuntimePresence", "RuntimeResumeService",
		"publicA2ATask", "publicA2APushConfig",
	}
	forbiddenPublicA2AFields := map[string]struct{}{
		"Adapter": {}, "AllowLocalPushURLs": {}, "RunTimeout": {},
		"tasks": {}, "pushes": {},
	}
	forbiddenPublicA2AFieldFragments := []string{
		"assignment", "event", "history", "message", "push", "run", "session", "state", "task", "transport",
	}
	forbiddenPublicA2AFunctions := map[string]struct{}{
		"cancelTask":               {},
		"createPushConfig":         {},
		"deletePushConfig":         {},
		"deliverPush":              {},
		"handleMessageSend":        {},
		"handleMessageStream":      {},
		"handleTaskPath":           {},
		"handleTaskPushConfigPath": {},
		"handleTasks":              {},
		"pushConfig":               {},
		"pushConfigList":           {},
		"runMessage":               {},
		"task":                     {},
		"taskList":                 {},
	}
	forbiddenSDKCalls := map[string]struct{}{
		"AckRuntimeAssignment":    {},
		"AckRuntimeCancel":        {},
		"AppendRuntimeEvent":      {},
		"ClaimRuntimeRun":         {},
		"CreateRuntimeSession":    {},
		"DialRuntimeWebSocket":    {},
		"FinalizeRuntimeResult":   {},
		"HeartbeatRuntimeSession": {},
		"PollRuntimeCommands":     {},
		"RejectRuntimeAssignment": {},
		"RenewRuntimeLease":       {},
		"ResumeRuntimeRuns":       {},
	}
	forbiddenImportPrefixes := []string{
		"database/sql",
		"github.com/dgraph-io/badger",
		"github.com/gorilla/websocket",
		"github.com/jackc/pgx",
		"github.com/redis/go-redis",
		"go.etcd.io/bbolt",
		"gorm.io/",
	}

	files := token.NewFileSet()
	usesSDKWorker := false
	usesSDKA2AProxy := false
	scansCLIEntrypoint := false
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var sourcePaths []string
	if err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".go" && !strings.HasSuffix(path, "_test.go") {
			sourcePaths = append(sourcePaths, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range sourcePaths {
		relativePath, err := filepath.Rel(repositoryRoot, path)
		if err != nil {
			t.Fatal(err)
		}
		relativePath = filepath.ToSlash(relativePath)
		if relativePath == "cmd/openlinker-agent-node/main.go" {
			scansCLIEntrypoint = true
		}
		lowerName := strings.ToLower(relativePath)
		for _, forbidden := range forbiddenFiles {
			if strings.Contains(lowerName, forbidden) {
				t.Errorf("Agent Node owns forbidden Runtime/A2A authority implementation file %q", relativePath)
			}
		}
		parsed, parseErr := parser.ParseFile(files, path, nil, 0)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.ImportSpec:
				importPath, err := strconv.Unquote(value.Path.Value)
				if err == nil {
					for _, forbiddenPrefix := range forbiddenImportPrefixes {
						if importPath == strings.TrimSuffix(forbiddenPrefix, "/") || strings.HasPrefix(importPath, forbiddenPrefix) {
							t.Errorf("Agent Node imports forbidden Runtime persistence/transport dependency %s (%s)", importPath, files.Position(value.Pos()))
						}
					}
				}
			case *ast.TypeSpec:
				for _, forbidden := range forbiddenDeclarations {
					if strings.Contains(strings.ToLower(value.Name.Name), strings.ToLower(forbidden)) {
						t.Errorf("Agent Node declares SDK-owned Runtime type %s (%s)", value.Name.Name, files.Position(value.Pos()))
					}
				}
				lowerTypeName := strings.ToLower(value.Name.Name)
				if strings.HasPrefix(lowerTypeName, "publica2a") && (strings.Contains(lowerTypeName, "task") || strings.Contains(lowerTypeName, "push")) {
					t.Errorf("Agent Node declares forbidden local Public A2A Task/Push type %s (%s)", value.Name.Name, files.Position(value.Pos()))
				}
				if value.Name.Name == "PublicA2AServer" {
					if declaration, ok := value.Type.(*ast.StructType); ok {
						for _, field := range declaration.Fields.List {
							for _, fieldName := range field.Names {
								if _, forbidden := forbiddenPublicA2AFields[fieldName.Name]; forbidden {
									t.Errorf("Agent Node PublicA2AServer owns forbidden A2A authority field %s (%s)", fieldName.Name, files.Position(fieldName.Pos()))
								}
								lowerFieldName := strings.ToLower(fieldName.Name)
								for _, fragment := range forbiddenPublicA2AFieldFragments {
									if strings.Contains(lowerFieldName, fragment) {
										t.Errorf("Agent Node PublicA2AServer owns forbidden A2A/Runtime state field %s (%s)", fieldName.Name, files.Position(fieldName.Pos()))
									}
								}
							}
						}
					}
				}
			case *ast.FuncDecl:
				if _, forbidden := forbiddenPublicA2AFunctions[value.Name.Name]; forbidden {
					t.Errorf("Agent Node declares forbidden local A2A authority function %s (%s)", value.Name.Name, files.Position(value.Name.Pos()))
				}
				lowerFunctionName := strings.ToLower(value.Name.Name)
				if strings.HasPrefix(lowerFunctionName, "publica2a") && (strings.Contains(lowerFunctionName, "task") || strings.Contains(lowerFunctionName, "push")) {
					t.Errorf("Agent Node declares forbidden local Public A2A Task/Push function %s (%s)", value.Name.Name, files.Position(value.Name.Pos()))
				}
			case *ast.CallExpr:
				selector, ok := value.Fun.(*ast.SelectorExpr)
				if ok {
					if selector.Sel.Name == "NewRuntimeWorker" {
						usesSDKWorker = true
					}
					if selector.Sel.Name == "NewRuntimeA2AProxy" {
						usesSDKA2AProxy = true
					}
					if _, forbidden := forbiddenSDKCalls[selector.Sel.Name]; forbidden {
						t.Errorf("Agent Node calls SDK-owned Runtime operation %s directly (%s)", selector.Sel.Name, files.Position(value.Pos()))
					}
				}
			case *ast.BasicLit:
				if value.Kind == token.STRING {
					literal, err := strconv.Unquote(value.Value)
					if err == nil && strings.Contains(literal, "/api/v1/agent-runtime") {
						t.Errorf("Agent Node embeds forbidden Core Runtime wire path (%s)", files.Position(value.Pos()))
					}
					if err == nil && (strings.Contains(literal, "/message:") || strings.Contains(literal, "/tasks")) {
						t.Errorf("Agent Node declares forbidden local A2A operation route %q (%s)", literal, files.Position(value.Pos()))
					}
				}
			}
			return true
		})
	}
	if !usesSDKWorker {
		t.Fatal("Agent Node must start through openlinker.NewRuntimeWorker")
	}
	if !usesSDKA2AProxy {
		t.Fatal("Agent Node public A2A compatibility listener must use openlinker.NewRuntimeA2AProxy")
	}
	if !scansCLIEntrypoint {
		t.Fatal("Agent Node Runtime boundary must scan first-party Go sources outside internal/agentnode")
	}
}
