package parser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SolidityAST represents the JSON AST output from solc (compact format)
// The root of solc --ast-compact-json is the SourceUnit directly
type SolidityAST struct {
	AbsolutePath    string            `json:"absolutePath"`
	ExportedSymbols map[string][]int  `json:"exportedSymbols"`
	Id              int               `json:"id"`
	License         string            `json:"license"`
	NodeType        string            `json:"nodeType"`
	Nodes           []SolidityASTNode `json:"nodes"`
}

// SolidityASTNode represents a node in the Solidity AST
type SolidityASTNode struct {
	NodeType              string                     `json:"nodeType"`
	Name                  string                     `json:"name,omitempty"`
	Children              []SolidityASTNode          `json:"children,omitempty"`
	Attributes            map[string]json.RawMessage `json:"attributes,omitempty"`
	Src                   string                     `json:"src"`
	Id                    int                        `json:"id"`
	Kind                  string                     `json:"kind,omitempty"`
	StateMutability       string                     `json:"stateMutability,omitempty"`
	Type                  string                     `json:"type,omitempty"`
	TypeName              *SolidityASTNode           `json:"typeName,omitempty"`
	KeyType               *SolidityASTNode           `json:"keyType,omitempty"`
	ValueType             *SolidityASTNode           `json:"valueType,omitempty"`
	PathNode              *SolidityASTNode           `json:"pathNode,omitempty"`
	TypeDescriptions      *TypeDescriptions          `json:"typeDescriptions,omitempty"`
	Visibility            string                     `json:"visibility,omitempty"`
	Constant              bool                       `json:"constant,omitempty"`
	Mutability            string                     `json:"mutability,omitempty"`
	Parameters            *SolidityParameterList     `json:"parameters,omitempty"`
	ReturnParameters      *SolidityParameterList     `json:"returnParameters,omitempty"`
	Modifiers             []SolidityASTNode          `json:"modifiers,omitempty"`
	ModifierName          *SolidityASTNode           `json:"modifierName,omitempty"`
	Body                  *SolidityASTNode           `json:"body,omitempty"`
	BaseContracts         []SolidityInheritance      `json:"baseContracts,omitempty"`
	SubContracts          []SolidityASTNode          `json:"subNodes,omitempty"`
	LibraryName           *SolidityASTNode           `json:"libraryName,omitempty"`
	Scope                 int                        `json:"scope,omitempty"`
	Nodes                 []SolidityASTNode          `json:"nodes,omitempty"`
	Statements            []SolidityASTNode          `json:"statements,omitempty"`
	Operator              string                     `json:"operator,omitempty"`
	Value                 interface{}                `json:"value,omitempty"`
	Expression            *SolidityASTNode           `json:"expression,omitempty"`
	LeftHandSide          *SolidityASTNode           `json:"leftHandSide,omitempty"`
	RightHandSide         *SolidityASTNode           `json:"rightHandSide,omitempty"`
	FunctionSelector      string                     `json:"functionSelector,omitempty"`
	ContractKind          string                     `json:"contractKind,omitempty"`
	ReferencedDeclaration int                        `json:"referencedDeclaration,omitempty"`
	MemberName            string                     `json:"memberName,omitempty"`
	EventCall             *SolidityASTNode           `json:"eventCall,omitempty"`
	Arguments             []SolidityASTNode          `json:"arguments,omitempty"`
	Options               []SolidityASTNode          `json:"options,omitempty"`
	Names                 []string                   `json:"names,omitempty"`
	ExternalCall          *SolidityASTNode           `json:"externalCall,omitempty"`
	Clauses               []SolidityASTNode          `json:"clauses,omitempty"`
	Block                 *SolidityASTNode           `json:"block,omitempty"`
	ErrorName             string                     `json:"errorName,omitempty"`
	IndexExpression       *SolidityASTNode           `json:"indexExpression,omitempty"`
	BaseExpression        *SolidityASTNode           `json:"baseExpression,omitempty"`
	HexValue              string                     `json:"hexValue,omitempty"`
	// ForStatement fields
	Init                     *SolidityASTNode `json:"init,omitempty"`
	InitializationExpression *SolidityASTNode `json:"initializationExpression,omitempty"`
	Condition                *SolidityASTNode `json:"condition,omitempty"`
	LoopExpression           *SolidityASTNode `json:"loopExpression,omitempty"`
	// Operation fields
	SubExpression   *SolidityASTNode `json:"subExpression,omitempty"`
	LeftExpression  *SolidityASTNode `json:"leftExpression,omitempty"`
	RightExpression *SolidityASTNode `json:"rightExpression,omitempty"`
	Prefix          bool             `json:"prefix,omitempty"`
	// TupleExpression fields
	Components []SolidityASTNode `json:"components,omitempty"`
	// VariableDeclarationStatement fields
	Declarations []SolidityASTNode `json:"declarations,omitempty"`
	InitialValue *SolidityASTNode  `json:"initialValue,omitempty"`
	Assignments  []int             `json:"assignments,omitempty"`
	// IfStatement fields
	TrueBody  *SolidityASTNode `json:"trueBody,omitempty"`
	FalseBody *SolidityASTNode `json:"falseBody,omitempty"`
	// Conditional (ternary) fields
	TrueExpression  *SolidityASTNode `json:"trueExpression,omitempty"`
	FalseExpression *SolidityASTNode `json:"falseExpression,omitempty"`
	// FunctionReturnParameters for return statements
	FunctionReturnParameters int `json:"functionReturnParameters,omitempty"`
	// HasReturnValue for return statements
	HasReturnValue bool `json:"hasReturnValue,omitempty"`
	// Members for struct/enum definitions
	Members       []SolidityASTNode `json:"members,omitempty"`
	CanonicalName string            `json:"canonicalName,omitempty"`
}

// TypeDescriptions contains type information
type TypeDescriptions struct {
	TypeIdentifier string `json:"typeIdentifier,omitempty"`
	TypeString     string `json:"typeString,omitempty"`
}

// SolidityInheritance represents inheritance specification
type SolidityInheritance struct {
	BaseName *SolidityASTNode `json:"baseName,omitempty"`
}

// SolidityParameterList represents function parameters
type SolidityParameterList struct {
	Parameters []SolidityASTNode `json:"parameters"`
}

// CheckSolc checks if solc is available in PATH
func CheckSolc() bool {
	_, err := exec.LookPath("solc")
	return err == nil
}

// ParseSolidityAST parses a Solidity file using solc --ast-compact-json
func ParseSolidityAST(filename string) (*SolidityAST, error) {
	// Verify file exists
	if _, err := os.Stat(filename); err != nil {
		return nil, fmt.Errorf("file not found: %s", filename)
	}

	// First try full semantic analysis.
	output, err := runSolcAST(filename, false)
	if err != nil {
		// Fallback to parser-only mode for contracts that depend on external
		// declarations (devpacks, custom builtins) not available locally.
		fallbackOutput, fallbackErr := runSolcAST(filename, true)
		if fallbackErr != nil {
			return nil, fmt.Errorf("solc error: %s", err)
		}
		output = fallbackOutput
	}

	// Parse output - may contain header lines like "JSON AST (compact format):"
	lines := strings.Split(output, "\n")
	var jsonLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "=======") && !strings.HasPrefix(trimmed, "JSON") {
			jsonLines = append(jsonLines, line)
		}
	}
	jsonStr := strings.Join(jsonLines, "")

	var ast SolidityAST
	if err := json.Unmarshal([]byte(jsonStr), &ast); err != nil {
		return nil, fmt.Errorf("failed to parse AST JSON: %v", err)
	}

	return &ast, nil
}

func runSolcAST(filename string, parseOnly bool) (string, error) {
	args := []string{"--ast-compact-json"}
	if parseOnly {
		args = append(args, "--stop-after", "parsing")
	}
	args = append(args, filename)

	cmd := exec.Command("solc", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}

	return stdout.String(), nil
}

// GetContracts extracts contract definitions from AST
func GetContracts(ast *SolidityAST) []SolidityASTNode {
	var contracts []SolidityASTNode

	for _, node := range ast.Nodes {
		if node.NodeType == "ContractDefinition" {
			contracts = append(contracts, node)
		}
	}

	return contracts
}

// GetContractFunctions extracts function definitions from a contract
func GetContractFunctions(contract *SolidityASTNode) []SolidityASTNode {
	var functions []SolidityASTNode

	for _, node := range contract.Nodes {
		if node.NodeType == "FunctionDefinition" {
			functions = append(functions, node)
		}
	}

	return functions
}

// GetContractModifiers extracts modifier definitions from a contract.
func GetContractModifiers(contract *SolidityASTNode) []SolidityASTNode {
	var modifiers []SolidityASTNode

	for _, node := range contract.Nodes {
		if node.NodeType == "ModifierDefinition" {
			modifiers = append(modifiers, node)
		}
	}

	return modifiers
}

// GetContractVariables extracts state variable declarations from a contract
func GetContractVariables(contract *SolidityASTNode) []SolidityASTNode {
	var variables []SolidityASTNode

	for _, node := range contract.Nodes {
		if node.NodeType == "VariableDeclaration" {
			variables = append(variables, node)
		}
	}

	return variables
}

// GetContractEvents extracts event definitions from a contract
func GetContractEvents(contract *SolidityASTNode) []SolidityASTNode {
	var events []SolidityASTNode

	for _, node := range contract.Nodes {
		if node.NodeType == "EventDefinition" {
			events = append(events, node)
		}
	}

	return events
}

// GetContractStructs returns all struct definitions from a contract
func GetContractStructs(contract *SolidityASTNode) []SolidityASTNode {
	var structs []SolidityASTNode
	for _, node := range contract.Nodes {
		if node.NodeType == "StructDefinition" {
			structs = append(structs, node)
		}
	}
	return structs
}

// GetContractEnums returns all enum definitions from a contract
func GetContractEnums(contract *SolidityASTNode) []SolidityASTNode {
	var enums []SolidityASTNode
	for _, node := range contract.Nodes {
		if node.NodeType == "EnumDefinition" {
			enums = append(enums, node)
		}
	}
	return enums
}

// GetTypeString extracts the type information from a node
func GetTypeString(node *SolidityASTNode) string {
	// First try TypeDescriptions (new solc format)
	if node.TypeDescriptions != nil && node.TypeDescriptions.TypeString != "" {
		return node.TypeDescriptions.TypeString
	}

	// Try TypeName (for variable declarations)
	if node.TypeName != nil && node.TypeName.Name != "" {
		return node.TypeName.Name
	}

	// Fallback to attributes
	if node.Attributes != nil {
		if typeAttr, ok := node.Attributes["type"]; ok {
			var typeStr string
			if err := json.Unmarshal(typeAttr, &typeStr); err == nil {
				return typeStr
			}
		}
	}

	return node.Type
}

// GetNodeTypeString extracts type string from any node (including nested)
func GetNodeTypeString(node *SolidityASTNode) string {
	if node == nil {
		return ""
	}

	// Check TypeDescriptions first
	if node.TypeDescriptions != nil {
		return node.TypeDescriptions.TypeString
	}

	// Check TypeName
	if node.TypeName != nil {
		return node.TypeName.Name
	}

	// Fall back to Type field
	return node.Type
}

// GetNodeById finds a node in the AST by its ID
func GetNodeById(ast *SolidityAST, id int) *SolidityASTNode {
	// Search through top-level nodes
	for i := range ast.Nodes {
		if found := findNodeById(&ast.Nodes[i], id); found != nil {
			return found
		}
	}
	return nil
}

func findNodeById(node *SolidityASTNode, id int) *SolidityASTNode {
	if node.Id == id {
		return node
	}
	for i := range node.Children {
		if found := findNodeById(&node.Children[i], id); found != nil {
			return found
		}
	}
	for i := range node.Nodes {
		if found := findNodeById(&node.Nodes[i], id); found != nil {
			return found
		}
	}
	return nil
}

// GetSourceLocation parses the src attribute to get file, start, end positions
func GetSourceLocation(src string) (filename string, startLine, startCol, endLine, endCol int) {
	parts := strings.Split(src, ":")
	if len(parts) >= 3 {
		// Format: "filename:start:length"
		filename = parts[0]
		// Parse start position
		pos := strings.Split(parts[1], "-")
		if len(pos) >= 2 {
			// Could be line:col-line:col format
			startParts := strings.Split(pos[0], ":")
			if len(startParts) >= 2 {
				fmt.Sscanf(startParts[0], "%d", &startLine)
				fmt.Sscanf(startParts[1], "%d", &startCol)
			}
		}
	}
	return
}

// ExtractImports extracts import directives from AST
func ExtractImports(ast *SolidityAST) []string {
	var imports []string

	for _, node := range ast.Nodes {
		if node.NodeType == "ImportDirective" {
			if node.Attributes != nil {
				if fileAttr, ok := node.Attributes["file"]; ok {
					var filePath string
					if err := json.Unmarshal(fileAttr, &filePath); err == nil {
						imports = append(imports, filePath)
					}
				}
			}
		}
	}

	return imports
}

// GetPragmas extracts version pragma from AST
func GetPragmas(ast *SolidityAST) []string {
	var pragmas []string

	for _, node := range ast.Nodes {
		if node.NodeType == "PragmaDirective" {
			if node.Attributes != nil {
				if identAttr, ok := node.Attributes["identifiers"]; ok {
					var ident string
					if err := json.Unmarshal(identAttr, &ident); err == nil {
						pragmas = append(pragmas, ident)
					}
				}
			}
		}
	}

	return pragmas
}

// ResolvePath resolves a Solidity import path
func ResolvePath(basePath, importPath string) string {
	if filepath.IsAbs(importPath) {
		return importPath
	}
	return filepath.Join(filepath.Dir(basePath), importPath)
}
