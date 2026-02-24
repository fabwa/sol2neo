package transformer

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"sol2neo/parser"
)

var currentContractStructs []string
var currentContractStorageArrays []string // Track state arrays that need storage-based access
var currentContractEnumNames = make(map[string]bool)
var currentContractFunctionNames = make(map[string]string)
var currentContractVariableTypes = make(map[string]string)
var currentContractMappingValueTypes = make(map[string]string)
var currentContractModifiers = make(map[string]parser.SolidityASTNode)
var currentContractHasStorageContext bool
var storageReferences = make(map[string]string) // Track local variables that reference storage: varName -> storageKey
var storageArrayElementReferences = make(map[string]storageArrayElementReference)
var currentFunctionMultiReturn = false // Track if current function has multiple return values
var currentFunctionMultiReturnNames []string

type storageArrayElementReference struct {
	ArrayStorageKey string
	ElementIndex    string
	ArrayType       string
}

// WarningsCollector collects warnings for unsupported features
type WarningsCollector struct {
	Warnings     []string
	ShowWarnings bool
}

// TransformResult contains the result of transformation
type TransformResult struct {
	GoSource     string
	PackageName  string
	ContractName string
	Functions    []FunctionInfo
	Events       []EventInfo
	Variables    []VariableInfo
	HasStorage   bool
	HasEvents    bool
}

// FunctionInfo contains metadata about a function
type FunctionInfo struct {
	Name       string
	Parameters []ParameterInfo
	ReturnType string
}

// ParameterInfo contains parameter metadata
type ParameterInfo struct {
	Name string
	Type string
}

// EventInfo contains metadata about an event
type EventInfo struct {
	Name       string
	Parameters []ParameterInfo
}

// VariableInfo contains metadata about a state variable
type VariableInfo struct {
	Name         string
	Type         string
	Visibility   string
	OriginalType string
	IsMapping    bool
	IsArray      bool
	ElementType  string
}

// Transform transforms Solidity AST to NeoGo-compatible Go source
func Transform(ast *parser.SolidityAST, warnings *WarningsCollector) (*TransformResult, error) {
	contracts := parser.GetContracts(ast)
	if len(contracts) == 0 {
		return nil, fmt.Errorf("no contract definitions found")
	}

	// Transform the first contract (can be extended for multiple)
	contract := contracts[0]
	return transformContract(&contract, warnings)
}

func transformContract(contract *parser.SolidityASTNode, warnings *WarningsCollector) (*TransformResult, error) {
	result := &TransformResult{
		ContractName: contract.Name,
		PackageName:  toGoPackageName(contract.Name),
	}

	var sb strings.Builder

	// Write package
	sb.WriteString(fmt.Sprintf("package %s\n\n", result.PackageName))

	// Imports will be added at the end based on usage

	// Check contract kind
	kind := contract.Kind // "contract", "library", "interface"
	if kind == "library" {
		warnings.AddWarning("Libraries are not fully supported - functions will be inlined")
	}

	variables := parser.GetContractVariables(contract)
	hasMappings := false
	hasArrays := false
	for _, v := range variables {
		if v.TypeDescriptions != nil {
			if strings.Contains(v.TypeDescriptions.TypeString, "mapping") {
				hasMappings = true
			}
			if strings.Contains(v.TypeDescriptions.TypeString, "[]") {
				hasArrays = true
			}
		}
		if v.TypeName != nil {
			if v.TypeName.NodeType == "Mapping" {
				hasMappings = true
			}
			if v.TypeName.NodeType == "ArrayTypeName" || strings.Contains(v.TypeName.Name, "[]") {
				hasArrays = true
			}
		}
	}

	if hasMappings || hasArrays {
		sb.WriteString("// Storage context\n")
		sb.WriteString("var ctx storage.Context\n\n")
		sb.WriteString("func init() {\n")
		sb.WriteString("\tctx = storage.GetContext()\n")
		sb.WriteString("}\n\n")
		sb.WriteString("func getIntFromCtx(storageCtx storage.Context, key []byte) int {\n")
		sb.WriteString("\tval := storage.Get(storageCtx, key)\n")
		sb.WriteString("\tif val == nil { return 0 }\n")
		sb.WriteString("\treturn val.(int)\n")
		sb.WriteString("}\n\n")
		sb.WriteString("func getIntFromDB(key []byte) int {\n")
		sb.WriteString("\treturn getIntFromCtx(ctx, key)\n")
		sb.WriteString("}\n\n")
		sb.WriteString("func checkOwner() bool {\n")
		sb.WriteString("\treturn runtime.CheckWitness(runtime.GetCallingScriptHash())\n")
		sb.WriteString("}\n\n")
	}
	currentContractHasStorageContext = hasMappings || hasArrays

	result.HasStorage = hasMappings || hasArrays

	// Process enums first (needed for struct fields)
	enums := parser.GetContractEnums(contract)
	currentContractEnumNames = make(map[string]bool)
	if len(enums) > 0 {
		sb.WriteString("// Enums\n")
		for _, enumNode := range enums {
			currentContractEnumNames[enumNode.Name] = true
			enumCode := transformEnum(&enumNode, warnings)
			sb.WriteString(enumCode)
		}
		sb.WriteString("\n")
	}

	// Process structs
	structs := parser.GetContractStructs(contract)
	currentContractStructs = []string{}
	currentContractStorageArrays = []string{}
	currentContractVariableTypes = make(map[string]string)
	currentContractMappingValueTypes = make(map[string]string)
	if len(structs) > 0 {
		sb.WriteString("// Structs\n")
		for _, structNode := range structs {
			currentContractStructs = append(currentContractStructs, structNode.Name)
			structCode := transformStruct(&structNode, warnings)
			sb.WriteString(structCode)
		}
		sb.WriteString("\n")
	}

	// Process state variables
	if len(variables) > 0 {
		result.HasStorage = true
		sb.WriteString("// State variables\n")
		for _, varNode := range variables {
			varInfo := transformVariable(&varNode, warnings)
			if varInfo != nil {
				currentContractVariableTypes[varInfo.Name] = varInfo.Type
				if varInfo.IsMapping {
					if valueType := inferMappingValueType(&varNode); valueType != "" {
						currentContractMappingValueTypes[varInfo.Name] = valueType
					}
				}

				// Skip mapping declarations - we use storage directly
				if varInfo.IsMapping {
					// For mappings, we just keep track of the name for storage keys
					// Don't declare a variable - storage is used directly
					result.Variables = append(result.Variables, *varInfo)
				} else if varInfo.IsArray {
					// For state arrays, track them but don't declare as Go variable
					// They use storage-based access with counter
					currentContractStorageArrays = append(currentContractStorageArrays, varInfo.Name)
					result.Variables = append(result.Variables, *varInfo)
					// Output a length counter variable
					sb.WriteString(fmt.Sprintf("var %sCount int\n", varInfo.Name))
				} else {
					result.Variables = append(result.Variables, *varInfo)
					// Handle constants with initial values
					isConstant := varNode.Constant || varNode.Mutability == "constant"
					if isConstant && varNode.Value != nil {
						initVal := formatNodeValue(varNode.Value)
						if canUseConstInitializer(initVal) {
							sb.WriteString(fmt.Sprintf("const %s = %s\n", varInfo.Name, initVal))
						} else {
							sb.WriteString(fmt.Sprintf("var %s %s = %s\n", varInfo.Name, varInfo.Type, initVal))
						}
					} else if varNode.Value != nil {
						// Keep simple literal-style initializers for non-constant state vars.
						initVal := formatNodeValue(varNode.Value)
						if canUseConstInitializer(initVal) {
							sb.WriteString(fmt.Sprintf("var %s %s = %s\n", varInfo.Name, varInfo.Type, initVal))
						} else {
							warnings.AddWarning(fmt.Sprintf("State variable '%s' initializer not lowered; using zero value", varInfo.Name))
							sb.WriteString(fmt.Sprintf("var %s %s\n", varInfo.Name, varInfo.Type))
						}
					} else {
						sb.WriteString(fmt.Sprintf("var %s %s\n", varInfo.Name, varInfo.Type))
					}
				}
			}
		}
		sb.WriteString("\n")
	}

	// Process events
	events := parser.GetContractEvents(contract)
	if len(events) > 0 {
		result.HasEvents = true
		sb.WriteString("// Events\n")
		for _, eventNode := range events {
			eventInfo := transformEvent(&eventNode, warnings)
			result.Events = append(result.Events, eventInfo)
			sb.WriteString(fmt.Sprintf("const Event%s = \"%s\"\n", eventInfo.Name, eventInfo.Name))
		}
		sb.WriteString("\n")
	}

	// Process functions
	functions := parser.GetContractFunctions(contract)
	modifiers := parser.GetContractModifiers(contract)
	currentContractModifiers = make(map[string]parser.SolidityASTNode)
	for _, modifierNode := range modifiers {
		if modifierNode.Name != "" {
			currentContractModifiers[modifierNode.Name] = modifierNode
		}
	}
	currentContractFunctionNames = make(map[string]string)
	for _, fn := range functions {
		if fn.Name != "" {
			currentContractFunctionNames[fn.Name] = toGoFunctionName(fn.Name, fn.Kind, fn.Visibility)
		}
	}

	for _, funcNode := range functions {
		funcInfo, funcCode := transformFunction(&funcNode, warnings)
		if funcInfo != nil {
			result.Functions = append(result.Functions, *funcInfo)
			sb.WriteString(funcCode)
			sb.WriteString("\n")
		}
	}

	result.GoSource = sb.String()

	needsLowLevelCallTupleHelper := strings.Contains(result.GoSource, "__lowLevelCallWithData(")
	needsLowLevelCallHelper := strings.Contains(result.GoSource, "__lowLevelCall(") || needsLowLevelCallTupleHelper
	needsSysContractCallHelper := strings.Contains(result.GoSource, "__sysContractCall(")
	needsKeccakHelper := strings.Contains(result.GoSource, "__keccak256(")
	needsFixedBytesHelper := strings.Contains(result.GoSource, "__toFixedBytes(")
	needsBytesToIntHelper := strings.Contains(result.GoSource, "__bytesToInt(")
	needsABIPackedHelper := strings.Contains(result.GoSource, "__abiEncodePacked(")
	needsBoolToUintHelper := strings.Contains(result.GoSource, "__boolToUint(")
	needsAddressCodeHelper := strings.Contains(result.GoSource, "__addressCode(")
	needsTryAnyHelper := strings.Contains(result.GoSource, "__toAnySlice(") || strings.Contains(result.GoSource, "__fromAnyInt(") || strings.Contains(result.GoSource, "__fromAnyBytes(") || strings.Contains(result.GoSource, "__fromAnyString(") || strings.Contains(result.GoSource, "__fromAnyBool(") || strings.Contains(result.GoSource, "__tryErrToString(") || strings.Contains(result.GoSource, "__tryErrToBytes(") || strings.Contains(result.GoSource, "__tryErrToInt(") || strings.Contains(result.GoSource, "__tryErrToBool(")

	hasInterop := strings.Contains(result.GoSource, "interop.Hash160") || strings.Contains(result.GoSource, "interop.Hash256") || needsLowLevelCallHelper || needsAddressCodeHelper || needsSysContractCallHelper
	hasStorage := strings.Contains(result.GoSource, "storage.")
	hasUtil := strings.Contains(result.GoSource, "util.Equals")
	hasLedger := strings.Contains(result.GoSource, "ledger.")
	hasConvert := strings.Contains(result.GoSource, "convert.") || needsKeccakHelper || needsFixedBytesHelper || needsABIPackedHelper || needsTryAnyHelper || needsSysContractCallHelper
	hasGas := strings.Contains(result.GoSource, "gas.") || needsLowLevelCallHelper
	needsPow := strings.Contains(result.GoSource, "pow(")
	needsCrypto := strings.Contains(result.GoSource, "crypto.") || needsKeccakHelper
	needsContract := strings.Contains(result.GoSource, "contract.") || needsSysContractCallHelper
	needsManagement := strings.Contains(result.GoSource, "management.") || needsAddressCodeHelper
	needsStd := strings.Contains(result.GoSource, "std.") || needsSysContractCallHelper
	needsIterator := strings.Contains(result.GoSource, "iterator.")
	hasRuntime := strings.Contains(result.GoSource, "runtime.") || needsLowLevelCallHelper

	var imports []string
	if hasInterop {
		imports = append(imports, "\"github.com/nspcc-dev/neo-go/pkg/interop\"")
	}
	if hasRuntime {
		imports = append(imports, "\"github.com/nspcc-dev/neo-go/pkg/interop/runtime\"")
	}
	if hasStorage {
		imports = append(imports, "\"github.com/nspcc-dev/neo-go/pkg/interop/storage\"")
	}
	if hasUtil {
		imports = append(imports, "\"github.com/nspcc-dev/neo-go/pkg/interop/util\"")
	}
	if hasLedger {
		imports = append(imports, "\"github.com/nspcc-dev/neo-go/pkg/interop/native/ledger\"")
	}
	if hasConvert {
		imports = append(imports, "\"github.com/nspcc-dev/neo-go/pkg/interop/convert\"")
	}
	if hasGas {
		imports = append(imports, "\"github.com/nspcc-dev/neo-go/pkg/interop/native/gas\"")
	}
	if needsCrypto {
		imports = append(imports, "\"github.com/nspcc-dev/neo-go/pkg/interop/native/crypto\"")
	}
	if needsContract {
		imports = append(imports, "\"github.com/nspcc-dev/neo-go/pkg/interop/contract\"")
	}
	if needsManagement {
		imports = append(imports, "\"github.com/nspcc-dev/neo-go/pkg/interop/native/management\"")
	}
	if needsStd {
		imports = append(imports, "\"github.com/nspcc-dev/neo-go/pkg/interop/native/std\"")
	}
	if needsIterator {
		imports = append(imports, "\"github.com/nspcc-dev/neo-go/pkg/interop/iterator\"")
	}

	importBlock := "import (\n"
	for _, imp := range imports {
		importBlock += "\t" + imp + "\n"
	}
	importBlock += ")\n\n"

	pkgEnd := strings.Index(result.GoSource, "\n\n")
	if pkgEnd > 0 {
		result.GoSource = result.GoSource[:pkgEnd+2] + importBlock + result.GoSource[pkgEnd+2:]
	}

	if needsPow {
		powHelper := `
func pow(base, exp int) int {
	result := 1
	for exp > 0 {
		if exp%2 == 1 {
			result *= base
		}
		base *= base
		exp /= 2
	}
	return result
}

`
		importEnd := strings.Index(result.GoSource, ")\n\n")
		if importEnd > 0 {
			result.GoSource = result.GoSource[:importEnd+3] + powHelper + result.GoSource[importEnd+3:]
		}
	}

	if needsBoolToUintHelper {
		boolToUintHelper := `
func __boolToUint(v bool) int {
	if v {
		return 1
	}
	return 0
}

`
		importEnd := strings.Index(result.GoSource, ")\n\n")
		if importEnd > 0 {
			result.GoSource = result.GoSource[:importEnd+3] + boolToUintHelper + result.GoSource[importEnd+3:]
		}
	}

	if needsAddressCodeHelper {
		addressCodeHelper := `
func __addressCode(addr interop.Hash160) []byte {
	if addr == nil {
		return []byte{}
	}
	if management.GetContract(addr) == nil {
		return []byte{}
	}
	// NeoVM doesn't expose EVM bytecode; use contract existence as code-presence marker.
	return []byte{1}
}

`
		importEnd := strings.Index(result.GoSource, ")\n\n")
		if importEnd > 0 {
			result.GoSource = result.GoSource[:importEnd+3] + addressCodeHelper + result.GoSource[importEnd+3:]
		}
	}

	if needsSysContractCallHelper {
		sysContractCallHelper := `
func __sysContractCall(scriptHash interop.Hash160, method string, params any) []byte {
	if scriptHash == nil {
		return []byte{}
	}

	callArgs := []any{}
	raw := convert.ToBytes(params)
	if len(raw) > 0 {
		decoded := std.Deserialize(raw)
		isTuple := false
		tuple := []any{}
		func() {
			defer func() {
				if recover() != nil {
					isTuple = false
				}
			}()
			tuple = decoded.([]any)
			isTuple = true
		}()
		if isTuple {
			callArgs = tuple
		} else {
			callArgs = []any{decoded}
		}
	}

	result := contract.Call(scriptHash, method, contract.All, callArgs...)
	if result == nil {
		return []byte{}
	}
	return convert.ToBytes(result)
}

`
		importEnd := strings.Index(result.GoSource, ")\n\n")
		if importEnd > 0 {
			result.GoSource = result.GoSource[:importEnd+3] + sysContractCallHelper + result.GoSource[importEnd+3:]
		}
	}

	if needsLowLevelCallHelper {
		lowLevelHelper := `
func __lowLevelCall(target interop.Hash160, value int, data any) bool {
	if target == nil {
		return false
	}
	if value < 0 {
		return false
	}
	if value > 0 {
		if !gas.Transfer(runtime.GetExecutingScriptHash(), target, value, data) {
			return false
		}
	}
	runtime.Notify("LowLevelCall", target, value)
	return true
}

`
		importEnd := strings.Index(result.GoSource, ")\n\n")
		if importEnd > 0 {
			result.GoSource = result.GoSource[:importEnd+3] + lowLevelHelper + result.GoSource[importEnd+3:]
		}
	}

	if needsLowLevelCallTupleHelper {
		lowLevelTupleHelper := `
func __lowLevelCallWithData(target interop.Hash160, value int, data any) (bool, []byte) {
	ok := __lowLevelCall(target, value, data)
	if !ok {
		return false, []byte{}
	}
	return true, []byte{}
}

`
		importEnd := strings.Index(result.GoSource, ")\n\n")
		if importEnd > 0 {
			result.GoSource = result.GoSource[:importEnd+3] + lowLevelTupleHelper + result.GoSource[importEnd+3:]
		}
	}

	if needsTryAnyHelper {
		tryAnyHelper := `
func __toAnySlice(v any) (res []any) {
	defer func() {
		if recover() != nil {
			res = []any{}
		}
	}()
	if v == nil {
		return []any{}
	}
	return v.([]any)
}

func __fromAnyInt(v any) (res int) {
	defer func() {
		if recover() != nil {
			res = 0
		}
	}()
	return convert.ToInteger(v)
}

func __fromAnyBool(v any) (res bool) {
	defer func() {
		if recover() != nil {
			res = false
		}
	}()
	return convert.ToBool(v)
}

func __fromAnyString(v any) (res string) {
	defer func() {
		if recover() != nil {
			res = ""
		}
	}()
	return convert.ToString(v)
}

func __fromAnyBytes(v any) (res []byte) {
	defer func() {
		if recover() != nil {
			res = []byte{}
		}
	}()
	return convert.ToBytes(v)
}

func __tryErrToString(v any) string {
	s := __fromAnyString(v)
	if s != "" {
		return s
	}
	return string(__fromAnyBytes(v))
}

func __tryErrToBytes(v any) []byte {
	b := __fromAnyBytes(v)
	if len(b) > 0 {
		return b
	}
	return []byte(__fromAnyString(v))
}

func __tryErrToInt(v any) int {
	return __fromAnyInt(v)
}

func __tryErrToBool(v any) bool {
	return __fromAnyBool(v)
}

`
		importEnd := strings.Index(result.GoSource, ")\n\n")
		if importEnd > 0 {
			result.GoSource = result.GoSource[:importEnd+3] + tryAnyHelper + result.GoSource[importEnd+3:]
		}
	}

	if needsKeccakHelper {
		keccakHelper := `
func __keccak256(data any) []byte {
	if data == nil {
		return convert.ToBytes(crypto.Keccak256([]byte{}))
	}
	return convert.ToBytes(crypto.Keccak256(convert.ToBytes(data)))
}

`
		importEnd := strings.Index(result.GoSource, ")\n\n")
		if importEnd > 0 {
			result.GoSource = result.GoSource[:importEnd+3] + keccakHelper + result.GoSource[importEnd+3:]
		}
	}

	if needsFixedBytesHelper {
		fixedBytesHelper := `
func __toFixedBytes(v any, size int) []byte {
	if size <= 0 {
		return []byte{}
	}
	out := make([]byte, size)
	if v == nil {
		return out
	}
	raw := convert.ToBytes(v)

	if len(raw) >= size {
		copy(out, raw[:size])
		return out
	}

	copy(out, raw)
	return out
}

`
		importEnd := strings.Index(result.GoSource, ")\n\n")
		if importEnd > 0 {
			result.GoSource = result.GoSource[:importEnd+3] + fixedBytesHelper + result.GoSource[importEnd+3:]
		}
	}

	if needsBytesToIntHelper {
		bytesToIntHelper := `
func __bytesToInt(data []byte) int {
	result := 0
	limit := len(data)
	if limit > 8 {
		limit = 8
	}
	for i := 0; i < limit; i++ {
		result = (result << 8) + int(data[i])
	}
	return result
}

`
		importEnd := strings.Index(result.GoSource, ")\n\n")
		if importEnd > 0 {
			result.GoSource = result.GoSource[:importEnd+3] + bytesToIntHelper + result.GoSource[importEnd+3:]
		}
	}

	if needsABIPackedHelper {
		abiPackedHelper := `
func __abiEncodePacked(values ...any) []byte {
	out := []byte{}
	for i := 0; i < len(values); i++ {
		if values[i] == nil {
			continue
		}
		out = append(out, convert.ToBytes(values[i])...)
	}
	return out
}

`
		importEnd := strings.Index(result.GoSource, ")\n\n")
		if importEnd > 0 {
			result.GoSource = result.GoSource[:importEnd+3] + abiPackedHelper + result.GoSource[importEnd+3:]
		}
	}

	return result, nil
}

func toGoFunctionName(name, kind, visibility string) string {
	if name == "" {
		if kind == "constructor" {
			name = "_deploy"
		} else if kind == "fallback" || kind == "receive" {
			name = "_call"
		}
	}

	if name == "" {
		return ""
	}

	candidate := name

	if name == "_deploy" || name == "_call" || name == "init" || name == "Verify" || name == "_initialize" {
		return candidate
	}

	if visibility == "public" || visibility == "external" {
		candidate = strings.ToUpper(name[:1]) + name[1:]
	} else {
		candidate = strings.ToLower(name[:1]) + name[1:]
	}

	return resolveFunctionNameCollision(candidate)
}

func resolveFunctionNameCollision(name string) string {
	for _, structName := range currentContractStructs {
		if structName == name {
			return name + "Func"
		}
	}
	return name
}

func toGoPackageName(name string) string {
	// Convert contract name to valid Go package name
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, " ", "_")
	return name
}

// AddWarning adds a warning message
func (w *WarningsCollector) AddWarning(msg string) {
	if w.ShowWarnings {
		w.Warnings = append(w.Warnings, msg)
	}
}

func transformVariable(varNode *parser.SolidityASTNode, warnings *WarningsCollector) *VariableInfo {
	// Try to get full type string from TypeDescriptions (includes mapping info)
	typeStr := ""
	if varNode.TypeDescriptions != nil && varNode.TypeDescriptions.TypeString != "" {
		typeStr = varNode.TypeDescriptions.TypeString
	}

	// Fallback to TypeName for simple types
	if typeStr == "" && varNode.TypeName != nil {
		typeStr = varNode.TypeName.Name
		if typeStr == "" {
			switch varNode.TypeName.NodeType {
			case "Mapping":
				typeStr = "mapping"
			case "ArrayTypeName":
				typeStr = "[]"
			}
		}
	}

	name := varNode.Name

	visibility := varNode.Visibility

	goType := MapType(typeStr)
	isMapping := strings.Contains(typeStr, "mapping")
	isArray := strings.Contains(typeStr, "[]") && !isMapping
	elementType := ""

	// Extract element type for arrays
	if isArray {
		// Extract element type from type string like "struct Lottery.Ticket[] storage ref"
		// or "uint256[]"
		parts := strings.Fields(typeStr)
		for _, part := range parts {
			if strings.HasSuffix(part, "[]") {
				elementType = part[:len(part)-2]
				break
			}
		}
		warnings.AddWarning(fmt.Sprintf("Variable '%s' is a state array - will use storage-based access", name))
	}

	// Check if it's a mapping (requires storage handling)
	if isMapping {
		warnings.AddWarning(fmt.Sprintf("Variable '%s' uses mapping - will use storage interop", name))
	}

	// Check visibility - but keep original case for constants
	isConstant := varNode.Constant || varNode.Mutability == "constant"
	if (visibility == "private" || visibility == "") && !isConstant {
		name = strings.ToLower(name[:1]) + name[1:]
	}

	return &VariableInfo{
		Name:         name,
		Type:         goType,
		Visibility:   visibility,
		OriginalType: typeStr,
		IsMapping:    isMapping,
		IsArray:      isArray,
		ElementType:  elementType,
	}
}

func inferMappingValueType(varNode *parser.SolidityASTNode) string {
	if varNode == nil {
		return ""
	}

	if varNode.TypeDescriptions != nil {
		typeStr := varNode.TypeDescriptions.TypeString
		if strings.Contains(typeStr, "mapping(") {
			if valueType := extractMappingValueType(typeStr); valueType != "" {
				return mapTypeFromASTType(valueType)
			}
		}
	}

	if varNode.TypeName != nil && varNode.TypeName.NodeType == "Mapping" {
		return inferValueTypeFromTypeName(varNode.TypeName.ValueType)
	}

	return ""
}

func inferValueTypeFromTypeName(node *parser.SolidityASTNode) string {
	if node == nil {
		return ""
	}

	if node.NodeType == "Mapping" {
		return inferValueTypeFromTypeName(node.ValueType)
	}

	if node.NodeType == "UserDefinedTypeName" {
		if node.Name != "" {
			return node.Name
		}
		if node.PathNode != nil && node.PathNode.Name != "" {
			return node.PathNode.Name
		}
	}

	if node.Name != "" {
		return MapType(node.Name)
	}

	if node.TypeDescriptions != nil && node.TypeDescriptions.TypeString != "" {
		return mapTypeFromASTType(node.TypeDescriptions.TypeString)
	}

	return ""
}

func extractDeclaredType(node *parser.SolidityASTNode) string {
	if node == nil {
		return ""
	}

	if node.TypeDescriptions != nil && node.TypeDescriptions.TypeString != "" {
		return node.TypeDescriptions.TypeString
	}

	if node.TypeName != nil {
		if node.TypeName.Name != "" {
			return node.TypeName.Name
		}
		if node.TypeName.PathNode != nil && node.TypeName.PathNode.Name != "" {
			return node.TypeName.PathNode.Name
		}
		if node.TypeName.TypeDescriptions != nil && node.TypeName.TypeDescriptions.TypeString != "" {
			return node.TypeName.TypeDescriptions.TypeString
		}
	}

	return ""
}

func transformEvent(eventNode *parser.SolidityASTNode, warnings *WarningsCollector) EventInfo {
	name := eventNode.Name

	var params []ParameterInfo

	if eventNode.Parameters != nil {
		for _, p := range eventNode.Parameters.Parameters {
			params = append(params, ParameterInfo{
				Name: p.Name,
				Type: MapType(p.Type),
			})
		}
	}

	return EventInfo{
		Name:       name,
		Parameters: params,
	}
}

func transformEnum(enumNode *parser.SolidityASTNode, warnings *WarningsCollector) string {
	var sb strings.Builder
	name := enumNode.Name

	// In Go, we represent enums as constants
	sb.WriteString(fmt.Sprintf("type %s int\n\n", name))
	sb.WriteString("const (\n")
	for i, member := range enumNode.Members {
		sb.WriteString(fmt.Sprintf("\t%s_%s %s = %d\n", name, member.Name, name, i))
	}
	sb.WriteString(")\n\n")

	return sb.String()
}

func transformStruct(structNode *parser.SolidityASTNode, warnings *WarningsCollector) string {
	var sb strings.Builder
	name := structNode.Name

	sb.WriteString(fmt.Sprintf("type %s struct {\n", name))
	for _, member := range structNode.Members {
		memberType := ""
		if member.TypeDescriptions != nil && member.TypeDescriptions.TypeString != "" {
			memberType = MapStructMemberType(member.TypeDescriptions.TypeString, structNode.Name, warnings)
		} else if member.TypeName != nil && member.TypeName.Name != "" {
			memberType = MapType(member.TypeName.Name)
		}
		if memberType == "" {
			memberType = "int"
		}
		sb.WriteString(fmt.Sprintf("\t%s %s\n", member.Name, memberType))
	}
	sb.WriteString("}\n\n")

	return sb.String()
}

func MapStructMemberType(typeStr string, contractName string, warnings *WarningsCollector) string {
	if strings.HasPrefix(typeStr, "enum ") {
		parts := strings.Fields(typeStr)
		if len(parts) >= 2 {
			enumName := parts[1]
			if dotIdx := strings.Index(enumName, "."); dotIdx >= 0 {
				enumName = enumName[dotIdx+1:]
			}
			return enumName
		}
	}
	return MapType(typeStr)
}

func transformFunction(funcNode *parser.SolidityASTNode, warnings *WarningsCollector) (*FunctionInfo, string) {
	name := funcNode.Name
	kind := funcNode.Kind
	visibility := funcNode.Visibility

	// Clear storage references for this function scope
	storageReferences = make(map[string]string)
	storageArrayElementReferences = make(map[string]storageArrayElementReference)
	currentFunctionMultiReturn = false
	currentFunctionMultiReturnNames = nil

	if name == "" && (kind == "fallback" || kind == "receive") {
		warnings.AddWarning("Fallback/receive function mapped to _call - may have limited functionality")
	}
	name = toGoFunctionName(name, kind, visibility)

	var params []ParameterInfo
	if funcNode.Parameters != nil {
		for i, p := range funcNode.Parameters.Parameters {
			pType := extractDeclaredType(&p)
			pName := p.Name
			if pName == "" {
				pName = fmt.Sprintf("_arg%d", i)
			}
			params = append(params, ParameterInfo{
				Name: pName,
				Type: MapType(pType),
			})
		}
	}

	constructorParams := []ParameterInfo{}
	// NeoGo _deploy must have exactly (data any, isUpdate bool) signature.
	// Constructor parameters are not supported as extra args.
	if name == "_deploy" {
		// Constructor args are read from `data` at runtime.
		constructorParams = append(constructorParams, params...)
		params = []ParameterInfo{
			{Name: "data", Type: "any"},
			{Name: "isUpdate", Type: "bool"},
		}
	}

	returnType := ""
	var returnParams []ParameterInfo
	var namedMultiReturnLocals []ParameterInfo
	if funcNode.ReturnParameters != nil && len(funcNode.ReturnParameters.Parameters) > 0 {
		retParams := funcNode.ReturnParameters.Parameters
		retTypes := make([]string, len(retParams))
		for i, p := range retParams {
			pType := extractDeclaredType(&p)
			retTypes[i] = MapType(pType)
			returnParams = append(returnParams, ParameterInfo{
				Name: p.Name,
				Type: MapType(pType),
			})
		}

		// NeoVM doesn't support multiple return values for exported methods
		// For public functions with multiple returns, use []interface{}
		isPublic := visibility == "public" || visibility == "external"
		if len(retParams) > 1 && isPublic && name != "_deploy" && name != "_call" && name != "init" && name != "Verify" && name != "_initialize" {
			returnType = "[]interface{}"
			currentFunctionMultiReturn = true
			for _, p := range returnParams {
				if p.Name != "" {
					currentFunctionMultiReturnNames = append(currentFunctionMultiReturnNames, p.Name)
					namedMultiReturnLocals = append(namedMultiReturnLocals, p)
				}
			}
		} else if len(retParams) > 1 {
			// For named return values, include names in signature
			if len(retParams) > 0 && retParams[0].Name != "" {
				retWithNames := make([]string, len(retParams))
				for i, p := range retParams {
					pTypeLocal := extractDeclaredType(&p)
					if p.Name != "" {
						retWithNames[i] = fmt.Sprintf("%s %s", p.Name, MapType(pTypeLocal))
					} else {
						retWithNames[i] = MapType(pTypeLocal)
					}
				}
				// Always use parentheses for named returns
				returnType = "(" + strings.Join(retWithNames, ", ") + ")"
			} else {
				returnType = strings.Join(retTypes, ", ")
				if len(retParams) > 1 {
					returnType = "(" + returnType + ")"
				}
			}
		} else {
			// For named return values, include names in signature
			if len(retParams) > 0 && retParams[0].Name != "" {
				retWithNames := make([]string, len(retParams))
				for i, p := range retParams {
					pTypeLocal := extractDeclaredType(&p)
					if p.Name != "" {
						retWithNames[i] = fmt.Sprintf("%s %s", p.Name, MapType(pTypeLocal))
					} else {
						retWithNames[i] = MapType(pTypeLocal)
					}
				}
				// Always use parentheses for named returns
				returnType = "(" + strings.Join(retWithNames, ", ") + ")"
			} else {
				returnType = strings.Join(retTypes, ", ")
			}
		}
	}

	funcInfo := &FunctionInfo{
		Name:       name,
		Parameters: params,
		ReturnType: returnType,
	}

	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("func %s(", name))
	for i, p := range params {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(fmt.Sprintf("%s %s", p.Name, p.Type))
	}
	sb.WriteString(")")
	if returnType != "" {
		sb.WriteString(" " + returnType)
	}
	sb.WriteString(" {\n")

	if funcNode.Body != nil {
		body := transformFunctionBody(funcNode, warnings)
		useReadOnlyCtx := currentContractHasStorageContext &&
			(funcNode.StateMutability == "view" || funcNode.StateMutability == "pure") &&
			functionBodyNeedsStorageContext(body)
		if useReadOnlyCtx {
			sb.WriteString("\tctxRO := storage.GetReadOnlyContext()\n")
			body = rewriteReadOnlyBody(body)
		}
		if len(constructorParams) > 0 {
			sb.WriteString(buildDeployParamBootstrap(constructorParams))
		}
		if len(namedMultiReturnLocals) > 0 {
			for _, p := range namedMultiReturnLocals {
				sb.WriteString(fmt.Sprintf("\tvar %s %s\n", p.Name, p.Type))
				sb.WriteString(fmt.Sprintf("\t_ = %s\n", p.Name))
			}
		}

		sb.WriteString(body)

		// Add implicit return for named return values if body doesn't end with a return
		if len(returnParams) > 0 && returnParams[0].Name != "" {
			// Check if the last statement is a return
			lines := strings.Split(strings.TrimSpace(body), "\n")
			lastLine := ""
			for i := len(lines) - 1; i >= 0; i-- {
				if strings.TrimSpace(lines[i]) != "" {
					lastLine = strings.TrimSpace(lines[i])
					break
				}
			}
			if !strings.HasPrefix(lastLine, "return") {
				if currentFunctionMultiReturn && len(currentFunctionMultiReturnNames) > 0 {
					sb.WriteString(fmt.Sprintf("\treturn []interface{}{%s}\n", strings.Join(currentFunctionMultiReturnNames, ", ")))
				} else {
					sb.WriteString("\treturn\n")
				}
			}
		}
	} else {
		sb.WriteString("\t// Auto-generated stub\n")
	}

	sb.WriteString("}\n")

	return funcInfo, sb.String()
}

func functionBodyNeedsStorageContext(body string) bool {
	if body == "" {
		return false
	}
	return strings.Contains(body, "storage.Get(ctx,") ||
		strings.Contains(body, "storage.Put(ctx,") ||
		strings.Contains(body, "storage.Delete(ctx,") ||
		strings.Contains(body, "storage.Find(ctx,") ||
		strings.Contains(body, "getIntFromDB(")
}

func rewriteReadOnlyBody(body string) string {
	if body == "" {
		return body
	}

	out := body
	out = strings.ReplaceAll(out, "storage.Get(ctx,", "storage.Get(ctxRO,")
	out = strings.ReplaceAll(out, "storage.Put(ctx,", "storage.Put(ctxRO,")
	out = strings.ReplaceAll(out, "storage.Delete(ctx,", "storage.Delete(ctxRO,")
	out = strings.ReplaceAll(out, "storage.Find(ctx,", "storage.Find(ctxRO,")
	out = strings.ReplaceAll(out, "getIntFromDB(", "getIntFromCtx(ctxRO, ")
	return out
}

func transformFunctionBody(funcNode *parser.SolidityASTNode, warnings *WarningsCollector) string {
	if funcNode == nil || funcNode.Body == nil {
		return "\t// Empty block\n"
	}

	body := transformBlock(funcNode.Body, warnings)
	if len(funcNode.Modifiers) == 0 {
		return body
	}

	wrapped := body
	// Modifiers wrap function body from right to left.
	for i := len(funcNode.Modifiers) - 1; i >= 0; i-- {
		wrapped = wrapWithModifier(&funcNode.Modifiers[i], wrapped, warnings)
	}
	return wrapped
}

func wrapWithModifier(invocation *parser.SolidityASTNode, innerBody string, warnings *WarningsCollector) string {
	if invocation == nil {
		return innerBody
	}

	modName := ""
	if invocation.ModifierName != nil {
		modName = invocation.ModifierName.Name
		if modName == "" {
			modName = transformExpression(invocation.ModifierName, warnings)
		}
	}
	if modName == "" {
		modName = invocation.Name
	}
	if modName == "" {
		warnings.AddWarning("Unable to resolve modifier invocation; body emitted without modifier guard")
		return innerBody
	}

	modifierDef, ok := currentContractModifiers[modName]
	if !ok || modifierDef.Body == nil {
		warnings.AddWarning(fmt.Sprintf("Modifier '%s' definition not found; body emitted without modifier guard", modName))
		return innerBody
	}

	var sb strings.Builder
	sb.WriteString("\t{\n")

	if modifierDef.Parameters != nil {
		for i, p := range modifierDef.Parameters.Parameters {
			pName := p.Name
			if pName == "" {
				pName = fmt.Sprintf("_modArg%d", i)
			}

			pType := "int"
			if declared := extractDeclaredType(&p); declared != "" {
				pType = MapType(declared)
			}

			argExpr := zeroValueForType(pType)
			if i < len(invocation.Arguments) {
				argExpr = transformExpression(&invocation.Arguments[i], warnings)
			}
			sb.WriteString(fmt.Sprintf("\t%s := %s\n", pName, argExpr))
		}
	}

	statements := modifierDef.Body.Statements
	if len(statements) == 0 {
		statements = modifierDef.Body.Nodes
	}

	insertedPlaceholder := false
	for _, stmt := range statements {
		if stmt.NodeType == "PlaceholderStatement" {
			sb.WriteString(innerBody)
			insertedPlaceholder = true
			continue
		}
		sb.WriteString(transformStatement(&stmt, warnings))
	}

	if !insertedPlaceholder {
		sb.WriteString(innerBody)
	}

	sb.WriteString("\t}\n")
	return sb.String()
}

func buildDeployParamBootstrap(params []ParameterInfo) string {
	if len(params) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\t_constructorArgs := []any{}\n")
	sb.WriteString("\tif data != nil {\n")
	sb.WriteString("\t\t_constructorArgs = data.([]any)\n")
	sb.WriteString("\t}\n")

	for i, p := range params {
		name := p.Name
		if name == "" {
			name = fmt.Sprintf("_arg%d", i)
		}
		zero := zeroValueForType(p.Type)
		sb.WriteString(fmt.Sprintf("\t%s := %s\n", name, zero))
		sb.WriteString(fmt.Sprintf("\tif len(_constructorArgs) > %d {\n", i))
		sb.WriteString(fmt.Sprintf("\t\t%s = _constructorArgs[%d].(%s)\n", name, i, p.Type))
		sb.WriteString("\t}\n")
	}

	return sb.String()
}

func zeroValueForType(goType string) string {
	switch goType {
	case "bool":
		return "false"
	case "string":
		return "\"\""
	case "[]byte":
		return "[]byte{}"
	case "any":
		return "nil"
	case "interop.Hash160":
		return "interop.Hash160(nil)"
	case "interop.Hash256":
		return "interop.Hash256(nil)"
	case "[]interface{}":
		return "[]interface{}(nil)"
	default:
		if strings.HasPrefix(goType, "[]") || strings.HasPrefix(goType, "map[") {
			return fmt.Sprintf("%s(nil)", goType)
		}
		return "0"
	}
}

func transformBlock(block *parser.SolidityASTNode, warnings *WarningsCollector) string {
	if block == nil {
		return ""
	}

	var sb strings.Builder

	statements := block.Statements
	if len(statements) == 0 {
		statements = block.Nodes
	}

	if len(statements) == 0 {
		return "\t// Empty block\n"
	}

	for _, stmt := range statements {
		sb.WriteString(transformStatement(&stmt, warnings))
	}

	result := sb.String()
	if strings.TrimSpace(result) == "" {
		return "\t// Empty block\n"
	}

	return result
}

func transformStatement(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	switch stmt.NodeType {
	case "ExpressionStatement":
		return transformExpressionStatement(stmt, warnings)
	case "VariableDeclarationStatement":
		return transformVariableDeclaration(stmt, warnings)
	case "IfStatement":
		return transformIfStatement(stmt, warnings)
	case "ReturnStatement":
		return transformReturnStatement(stmt, warnings)
	case "Return":
		// Handle Return node type (new format)
		return transformReturnStatement(stmt, warnings)
	case "EmitStatement":
		return transformEmitStatement(stmt, warnings)
	case "FunctionCall":
		return transformFunctionCall(stmt, warnings)
	case "UnaryOperation":
		return transformUnaryOperation(stmt, warnings)
	case "BinaryOperation":
		return transformBinaryOperation(stmt, warnings)
	case "MemberAccess":
		return transformMemberAccess(stmt, warnings)
	case "Identifier":
		return transformIdentifier(stmt, warnings)
	case "Literal":
		return transformLiteral(stmt, warnings)
	case "IndexAccess":
		return transformIndexAccess(stmt, warnings)
	case "WhileStatement":
		return transformWhileStatement(stmt, warnings)
	case "ForStatement":
		return transformForStatement(stmt, warnings)
	case "RevertStatement":
		return transformRevertStatement(stmt, warnings)
	case "TryStatement":
		return transformTryStatement(stmt, warnings)
	case "UncheckedBlock":
		return transformUncheckedBlock(stmt, warnings)
	case "Break", "BreakStatement":
		return "\tbreak\n"
	case "Continue", "ContinueStatement":
		return "\tcontinue\n"
	case "PlaceholderStatement":
		return ""
	default:
		warnings.AddWarning(fmt.Sprintf("Unsupported statement type: %s", stmt.NodeType))
		return fmt.Sprintf("\t// Unsupported: %s\n", stmt.NodeType)
	}
}

func transformUncheckedBlock(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	if stmt == nil {
		return "\t// Empty unchecked block\n"
	}

	warnings.AddWarning("UncheckedBlock lowered to regular Go block semantics")

	stmts := stmt.Statements
	if len(stmts) == 0 {
		stmts = stmt.Nodes
	}
	if len(stmts) == 0 {
		return "\t// Empty unchecked block\n"
	}

	var sb strings.Builder
	sb.WriteString("\t{\n")
	for i := range stmts {
		sb.WriteString(transformStatement(&stmts[i], warnings))
	}
	sb.WriteString("\t}\n")
	return sb.String()
}

func transformExpressionStatement(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	if stmt.Expression != nil {
		expr := stmt.Expression
		if expr.NodeType == "Assignment" {
			return transformAssignment(expr, warnings) + "\n"
		}
		code := transformExpression(expr, warnings)
		if strings.HasPrefix(code, "\t") {
			return code + "\n"
		}
		return "\t" + code + "\n"
	}
	if len(stmt.Children) > 0 {
		expr := &stmt.Children[0]
		if expr.NodeType == "Assignment" {
			return transformAssignment(expr, warnings) + "\n"
		}
		code := transformExpression(expr, warnings)
		return "\t" + code + "\n"
	}
	return ""
}

func transformVariableDeclaration(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	var sb strings.Builder

	// New AST format: declarations and initialValue fields
	if len(stmt.Declarations) > 0 {
		if len(stmt.Declarations) > 1 {
			var names []string
			types := make(map[string]string)
			for _, decl := range stmt.Declarations {
				if decl.Name == "" {
					continue
				}
				names = append(names, decl.Name)
				declType := ""
				if decl.TypeName != nil && decl.TypeName.Name != "" {
					declType = MapType(decl.TypeName.Name)
				} else if decl.TypeDescriptions != nil && decl.TypeDescriptions.TypeString != "" {
					declType = MapType(decl.TypeDescriptions.TypeString)
				}
				if declType == "" {
					declType = "int"
				}
				types[decl.Name] = declType
			}

			if len(names) > 0 && stmt.InitialValue != nil && isLowLevelCallExpression(stmt.InitialValue) {
				if len(names) == 1 {
					callExpr := transformExpression(stmt.InitialValue, warnings)
					sb.WriteString(fmt.Sprintf("\t%s := %s\n", names[0], callExpr))
					return sb.String()
				}

				target, value, data := extractLowLevelCallParts(stmt.InitialValue, warnings)
				sb.WriteString(fmt.Sprintf("\t__llSuccess, __llRetData := __lowLevelCallWithData(%s, %s, %s)\n", target, value, data))
				sb.WriteString(fmt.Sprintf("\t%s := __llSuccess\n", names[0]))

				secondName := names[1]
				secondType := types[secondName]
				switch secondType {
				case "[]byte":
					sb.WriteString(fmt.Sprintf("\t%s := __llRetData\n", secondName))
				case "string":
					sb.WriteString(fmt.Sprintf("\t%s := string(__llRetData)\n", secondName))
				default:
					sb.WriteString(fmt.Sprintf("\t%s := %s\n", secondName, zeroValueForType(secondType)))
				}

				for i := 2; i < len(names); i++ {
					name := names[i]
					defaultVal := zeroValueForType(types[name])
					sb.WriteString(fmt.Sprintf("\t%s := %s\n", name, defaultVal))
				}

				warnings.AddWarning("Low-level .call tuple declaration lowered to (bool, bytes) stub return data")
				return sb.String()
			}

			if len(names) > 0 && stmt.InitialValue != nil {
				initVal := transformExpression(stmt.InitialValue, warnings)
				if len(names) > 1 {
					sb.WriteString(fmt.Sprintf("\t%s := %s\n", strings.Join(names, ", "), initVal))
				} else {
					sb.WriteString(fmt.Sprintf("\t%s := %s\n", names[0], initVal))
				}
				return sb.String()
			}
		}

		decl := stmt.Declarations[0]
		varName := decl.Name
		varType := ""

		// Get type from TypeName
		if decl.TypeName != nil && decl.TypeName.Name != "" {
			varType = MapType(decl.TypeName.Name)
		} else if decl.TypeDescriptions != nil && decl.TypeDescriptions.TypeString != "" {
			varType = MapType(decl.TypeDescriptions.TypeString)
		}

		if stmt.InitialValue != nil {
			initVal := transformExpression(stmt.InitialValue, warnings)
			sb.WriteString(fmt.Sprintf("\t%s := %s\n", varName, initVal))

			// Track if this is a storage reference (for structs loaded from storage)
			if stmt.InitialValue.NodeType == "IndexAccess" {
				if arrayRef, ok := getStorageArrayElementReference(stmt.InitialValue, warnings); ok {
					storageArrayElementReferences[varName] = arrayRef
				} else {
					storageKey := buildStorageKey(stmt.InitialValue, warnings)
					storageReferences[varName] = storageKey
				}
			}
		} else if varType != "" {
			sb.WriteString(fmt.Sprintf("\tvar %s %s\n", varName, varType))
		} else {
			sb.WriteString(fmt.Sprintf("\tvar %s int\n", varName))
		}
		return sb.String()
	}

	// Fallback: old format with children
	if len(stmt.Children) >= 2 {
		varName := stmt.Children[0].Name
		_ = MapType(stmt.Children[0].Type)

		sb.WriteString(fmt.Sprintf("\t%s := ", varName))
		value := &stmt.Children[1]
		sb.WriteString(transformExpression(value, warnings))
		sb.WriteString("\n")
	}

	return sb.String()
}

func transformIfStatement(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	var sb strings.Builder

	var condition string
	var trueBody *parser.SolidityASTNode
	var falseBody *parser.SolidityASTNode

	// New format: condition, trueBody, falseBody fields
	if stmt.Condition != nil {
		condition = transformExpression(stmt.Condition, warnings)
		trueBody = stmt.TrueBody
		falseBody = stmt.FalseBody
	} else if len(stmt.Children) > 0 {
		// Fallback: Children array format
		condition = transformExpression(&stmt.Children[0], warnings)
		if len(stmt.Children) > 1 {
			trueBody = &stmt.Children[1]
		}
		if len(stmt.Children) > 2 {
			falseBody = &stmt.Children[2]
		}
	}

	if condition == "" {
		return "\t// Empty if statement\n"
	}

	sb.WriteString(fmt.Sprintf("\tif %s {\n", condition))

	if trueBody != nil {
		// Check if trueBody is a Block or a single statement
		if trueBody.NodeType == "Block" {
			trueCode := transformBlock(trueBody, warnings)
			sb.WriteString(trueCode)
		} else {
			// Single statement (like Return)
			sb.WriteString(transformStatement(trueBody, warnings))
		}
	}

	sb.WriteString("\t}")

	if falseBody != nil {
		sb.WriteString(" else {\n")
		// Check if falseBody is a Block or a single statement
		if falseBody.NodeType == "Block" {
			falseCode := transformBlock(falseBody, warnings)
			sb.WriteString(falseCode)
		} else {
			// Single statement
			sb.WriteString(transformStatement(falseBody, warnings))
		}
		sb.WriteString("\t}")
	}

	sb.WriteString("\n")

	return sb.String()
}

func transformReturnStatement(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	if stmt.Expression != nil {
		ret := transformExpression(stmt.Expression, warnings)
		// Check if this is a tuple expression (multiple return values)
		if stmt.Expression.NodeType == "TupleExpression" && currentFunctionMultiReturn {
			return fmt.Sprintf("\treturn []interface{}{%s}\n", ret)
		}
		return fmt.Sprintf("\treturn %s\n", ret)
	}
	if currentFunctionMultiReturn && len(currentFunctionMultiReturnNames) > 0 {
		return fmt.Sprintf("\treturn []interface{}{%s}\n", strings.Join(currentFunctionMultiReturnNames, ", "))
	}
	return "\treturn\n"
}

func transformRevertStatement(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	if stmt.Expression != nil {
		msg := transformExpression(stmt.Expression, warnings)
		return fmt.Sprintf("\tpanic(%s)\n", msg)
	}
	if len(stmt.Arguments) > 0 {
		arg := transformExpression(&stmt.Arguments[0], warnings)
		return fmt.Sprintf("\tpanic(%s)\n", arg)
	}
	return "\tpanic(\"reverted\")\n"
}

func transformTryStatement(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	if stmt == nil {
		return "\t// Empty try statement\n"
	}

	clauses := stmt.Clauses
	if len(clauses) == 0 {
		warnings.AddWarning("TryStatement without clauses emitted as comment")
		return "\t// Unsupported: TryStatement\n"
	}

	successClause := &clauses[0]
	catchClauses := []parser.SolidityASTNode{}
	if len(clauses) > 1 {
		catchClauses = clauses[1:]
	}

	var sb strings.Builder
	sb.WriteString("\t{\n")
	successParamNames := []string{}
	successParamTypes := []string{}

	if successClause.Parameters != nil {
		for i, p := range successClause.Parameters.Parameters {
			name := p.Name
			if name == "" {
				name = fmt.Sprintf("_tryRet%d", i)
			}
			pType := mapTryCatchParameterType(&p)
			successParamNames = append(successParamNames, name)
			successParamTypes = append(successParamTypes, pType)
			sb.WriteString(fmt.Sprintf("\tvar %s %s\n", name, pType))
			sb.WriteString(fmt.Sprintf("\t_ = %s\n", name))
		}
	}

	sb.WriteString("\t__tryOk := false\n")
	sb.WriteString("\tvar __tryErr any\n")
	sb.WriteString("\tfunc() {\n")
	sb.WriteString("\t\tdefer func() {\n")
	sb.WriteString("\t\t\tif r := recover(); r != nil {\n")
	sb.WriteString("\t\t\t\t__tryErr = r\n")
	sb.WriteString("\t\t\t}\n")
	sb.WriteString("\t\t}()\n")

	if stmt.ExternalCall != nil {
		callExpr := transformExpression(stmt.ExternalCall, warnings)
		if callExpr != "" {
			if len(successParamNames) > 0 {
				sb.WriteString(fmt.Sprintf("\t\t__tryRetTuple := __toAnySlice(any(%s))\n", callExpr))
				for i := range successParamNames {
					sb.WriteString(fmt.Sprintf("\t\t\tif len(__tryRetTuple) > %d {\n", i))
					sb.WriteString(fmt.Sprintf("\t\t\t\t%s = %s\n", successParamNames[i], castAnyToTypeExpr(fmt.Sprintf("__tryRetTuple[%d]", i), successParamTypes[i])))
					sb.WriteString("\t\t\t}\n")
				}
			} else {
				sb.WriteString(fmt.Sprintf("\t\t%s\n", callExpr))
			}
		}
	}

	sb.WriteString("\t\t__tryOk = true\n")
	sb.WriteString("\t}()\n")

	sb.WriteString("\tif __tryOk {\n")
	if successClause.Block != nil {
		sb.WriteString(transformBlock(successClause.Block, warnings))
	} else if successClause.Body != nil {
		sb.WriteString(transformBlock(successClause.Body, warnings))
	}
	sb.WriteString("\t} else {\n")
	if len(catchClauses) == 0 {
		sb.WriteString("\t\tpanic(__tryErr)\n")
	} else {
		catchClause := &catchClauses[0]
		for i := range catchClauses {
			if catchClauses[i].ErrorName == "Error" {
				catchClause = &catchClauses[i]
				break
			}
		}
		if catchClause.Parameters != nil {
			for pIdx, p := range catchClause.Parameters.Parameters {
				pName := p.Name
				if pName == "" {
					pName = fmt.Sprintf("_catchArg%d", pIdx)
				}
				pType := mapTryCatchParameterType(&p)
				sb.WriteString(bindTryCatchParameterFromError(pName, pType))
			}
		}
		if catchClause.Block != nil {
			sb.WriteString(transformBlock(catchClause.Block, warnings))
		} else if catchClause.Body != nil {
			sb.WriteString(transformBlock(catchClause.Body, warnings))
		}
	}
	sb.WriteString("\t}\n")

	sb.WriteString("\t}\n")
	warnings.AddWarning("TryStatement lowered with recover-based catch dispatch (approximate semantics)")
	return sb.String()
}

func mapTryCatchParameterType(p *parser.SolidityASTNode) string {
	if p == nil {
		return "any"
	}
	if p.TypeDescriptions != nil && p.TypeDescriptions.TypeString != "" {
		return MapType(p.TypeDescriptions.TypeString)
	}
	if p.TypeName != nil && p.TypeName.Name != "" {
		return MapType(p.TypeName.Name)
	}
	return "any"
}

func castAnyToTypeExpr(sourceExpr, goType string) string {
	switch goType {
	case "[]byte":
		return fmt.Sprintf("__fromAnyBytes(%s)", sourceExpr)
	case "string":
		return fmt.Sprintf("__fromAnyString(%s)", sourceExpr)
	case "int":
		return fmt.Sprintf("__fromAnyInt(%s)", sourceExpr)
	case "bool":
		return fmt.Sprintf("__fromAnyBool(%s)", sourceExpr)
	default:
		return fmt.Sprintf("(%s).(%s)", sourceExpr, goType)
	}
}

func bindTryCatchParameterFromError(name string, goType string) string {
	switch goType {
	case "[]byte":
		return fmt.Sprintf("\t\t\t%s := __tryErrToBytes(__tryErr)\n", name)
	case "string":
		return fmt.Sprintf("\t\t\t%s := __tryErrToString(__tryErr)\n", name)
	case "int":
		return fmt.Sprintf("\t\t\t%s := __tryErrToInt(__tryErr)\n", name)
	case "bool":
		return fmt.Sprintf("\t\t\t%s := __tryErrToBool(__tryErr)\n", name)
	default:
		return fmt.Sprintf("\t\t\t%s := __tryErr\n", name)
	}
}

func transformEmitStatement(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	// New format: has EventCall field
	if stmt.EventCall != nil {
		eventCall := stmt.EventCall
		// Get event name from expression.Name
		eventName := ""
		if eventCall.Expression != nil {
			eventName = eventCall.Expression.Name
		}
		if eventName == "" {
			eventName = "UnknownEvent"
		}

		// Get arguments from Arguments array
		var args []string
		if len(eventCall.Arguments) > 0 {
			for _, arg := range eventCall.Arguments {
				argStr := transformExpression(&arg, warnings)
				args = append(args, argStr)
			}
		}

		if len(args) == 0 {
			return fmt.Sprintf("\truntime.Notify(\"%s\")\n", eventName)
		}
		return fmt.Sprintf("\truntime.Notify(\"%s\", %s)\n", eventName, strings.Join(args, ", "))
	}

	// Fallback: try expression (old format)
	if stmt.Expression != nil {
		expr := stmt.Expression
		if expr.NodeType == "FunctionCall" && len(expr.Children) > 0 {
			eventNode := &expr.Children[0]
			eventName := eventNode.Name

			var args []string
			if len(expr.Children) > 1 {
				for i := 1; i < len(expr.Children); i++ {
					args = append(args, transformExpression(&expr.Children[i], warnings))
				}
			}

			if len(args) == 0 {
				return fmt.Sprintf("\truntime.Notify(\"%s\")\n", eventName)
			}
			return fmt.Sprintf("\truntime.Notify(\"%s\", %s)\n", eventName, strings.Join(args, ", "))
		}
	}

	warnings.AddWarning("Emit statement could not be fully transformed")
	return "\t// Emit statement\n"
}

func transformFunctionCall(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	// Check for Arguments field first (new solc format)
	var funcName string
	var args []string
	var argNodes []parser.SolidityASTNode // Keep original nodes for type info

	// Try to get function name from expression (Identifier)
	if stmt.Expression != nil && stmt.Expression.NodeType == "Identifier" {
		funcName = stmt.Expression.Name
	} else if stmt.Expression != nil && stmt.Expression.NodeType == "MemberAccess" {
		funcName = transformExpression(stmt.Expression, warnings)
	} else if stmt.Expression != nil && stmt.Expression.NodeType == "ElementaryTypeNameExpression" && stmt.Expression.TypeName != nil {
		funcName = stmt.Expression.TypeName.Name
		if funcName == "address" && stmt.Expression.TypeName.StateMutability == "payable" {
			funcName = "address payable"
		}
	} else if stmt.Expression != nil && stmt.Expression.NodeType == "FunctionCallOptions" && stmt.Expression.Expression != nil {
		if stmt.Expression.Expression.NodeType == "MemberAccess" {
			funcName = transformExpression(stmt.Expression.Expression, warnings)
		} else if stmt.Expression.Expression.NodeType == "Identifier" {
			funcName = stmt.Expression.Expression.Name
		}
	}

	// Get arguments from Arguments array (new format)
	if len(stmt.Arguments) > 0 {
		for _, arg := range stmt.Arguments {
			argStr := transformExpression(&arg, warnings)
			args = append(args, argStr)
			argNodes = append(argNodes, arg)
		}
	} else if len(stmt.Children) > 0 {
		// Fallback to Children
		if stmt.Children[0].NodeType == "Identifier" {
			funcName = stmt.Children[0].Name
		} else if stmt.Children[0].NodeType == "MemberAccess" {
			funcName = transformExpression(&stmt.Children[0], warnings)
		}
		for i := 1; i < len(stmt.Children); i++ {
			args = append(args, transformExpression(&stmt.Children[i], warnings))
			argNodes = append(argNodes, stmt.Children[i])
		}
	}

	if transferCall, ok := tryTransformAddressTransferCall(stmt, warnings); ok {
		return transferCall
	}

	if mappedName, ok := currentContractFunctionNames[funcName]; ok && !isBuiltinFunction(funcName) {
		funcName = mappedName
	}

	if isLowLevelCallExpression(stmt) {
		return transformLowLevelCall(stmt, warnings)
	}

	if ns, member, ok := getStaticNamespaceCall(stmt); ok && isPotentialLibraryNamespace(ns) && !isSpecialNamespace(ns) {
		if member == "toUint" && len(args) == 1 {
			return fmt.Sprintf("__boolToUint(%s)", args[0])
		}
		if mappedName, ok := currentContractFunctionNames[member]; ok {
			return fmt.Sprintf("%s(%s)", mappedName, strings.Join(args, ", "))
		}
		return fmt.Sprintf("%s(%s)", member, strings.Join(args, ", "))
	}

	// Handle built-in Solidity functions
	// First check for array.push pattern - this returns a special marker
	if strings.HasPrefix(funcName, "__storagePush__") {
		// State array push - need to store element and increment counter
		arrayName := funcName[len("__storagePush__"):]
		if len(args) > 0 {
			return fmt.Sprintf("storage.Put(ctx, append([]byte(%q), convert.ToBytes(%sCount)...), %s); %sCount++", arrayName+":", arrayName, args[0], arrayName)
		}
		return fmt.Sprintf("%sCount++", arrayName)
	}

	if strings.HasPrefix(funcName, "__append__") {
		// Extract the storage key expression - this is for arrays in mappings
		// e.g., userTickets[roundId][msg.sender].push(ticketNumber)
		storageKeyExpr := funcName[len("__append__"):]
		if len(args) > 0 {
			elemType := "int"
			if len(argNodes) > 0 {
				if inferred := mapTypeFromASTType(getExpressionType(&argNodes[0])); inferred != "" {
					elemType = inferred
				}
			}
			// For storage-backed arrays in mappings, we need:
			// 1. Get current array (or empty if nil)
			// 2. Append element
			// 3. Put back
			// Generate inline code for this
			return fmt.Sprintf("_arr := storage.Get(ctx, %s); if _arr == nil { _arr = []%s{} }; storage.Put(ctx, %s, append(_arr.([]%s), %s))", storageKeyExpr, elemType, storageKeyExpr, elemType, args[0])
		}
		return fmt.Sprintf("/* TODO: append to array at %s */", storageKeyExpr)
	}

	switch funcName {
	case "Syscalls.getCallingScriptHash":
		return "runtime.GetCallingScriptHash()"
	case "Syscalls.contractCall", "Syscalls.contractCallWithFlags":
		if len(args) < 2 {
			return "[]byte{}"
		}
		params := "nil"
		if len(args) > 2 {
			params = args[2]
		}
		if funcName == "Syscalls.contractCallWithFlags" && len(args) > 3 {
			warnings.AddWarning("Syscalls.contractCallWithFlags lowered to contract.Call with contract.All (flags ignored)")
		}
		return fmt.Sprintf("__sysContractCall(%s, %s, %s)", args[0], args[1], params)
	case "NativeCalls.gasTransfer":
		if len(args) >= 3 {
			data := "nil"
			if len(args) > 3 {
				data = args[3]
			}
			return fmt.Sprintf("gas.Transfer(%s, %s, %s, %s)", args[0], args[1], args[2], data)
		}
		return "false"
	case "NativeCalls.gasBalanceOf":
		if len(args) > 0 {
			return fmt.Sprintf("gas.BalanceOf(%s)", args[0])
		}
		return "0"
	case "int", "int8", "int16", "int32", "int64", "int256", "uint", "uint8", "uint16", "uint32", "uint64", "uint256":
		if len(args) == 0 {
			return "0"
		}
		if len(argNodes) > 0 {
			argNode := &argNodes[0]
			if argNode.NodeType == "FunctionCall" && argNode.Expression != nil && argNode.Expression.NodeType == "Identifier" && argNode.Expression.Name == "keccak256" {
				warnings.AddWarning("int(keccak256(...)) lowered to __bytesToInt(__keccak256(...))")
				if len(argNode.Arguments) > 0 {
					inner := transformExpression(&argNode.Arguments[0], warnings)
					return fmt.Sprintf("__bytesToInt(__keccak256(%s))", inner)
				}
				return "__bytesToInt(__keccak256(nil))"
			}
			argType := getExpressionType(argNode)
			if strings.Contains(argType, "Hash256") || isBytesType(argType) {
				return fmt.Sprintf("len(%s)", args[0])
			}
		}
		return fmt.Sprintf("int(%s)", args[0])
	case "abi.encodePacked":
		if len(args) == 0 {
			return "[]byte{}"
		}
		return fmt.Sprintf("__abiEncodePacked(%s)", strings.Join(args, ", "))
	case "abi.encode":
		if len(args) == 0 {
			return "[]byte{}"
		}
		if len(args) == 1 {
			return fmt.Sprintf("std.Serialize(%s)", args[0])
		}
		return fmt.Sprintf("std.Serialize([]any{%s})", strings.Join(args, ", "))
	case "abi.encodeWithSignature", "abi.encodeWithSelector", "abi.encodeCall":
		if len(args) == 0 {
			return "[]byte{}"
		}
		return fmt.Sprintf("std.Serialize([]any{%s})", strings.Join(args, ", "))
	case "abi.decode":
		if len(args) == 0 {
			return "nil"
		}
		return args[0]
	case "require":
		// require(condition) or require(condition, message)
		cond := ""
		msg := ""
		if len(args) > 0 {
			cond = args[0]
		}
		if len(args) > 1 {
			msg = args[1]
		}

		// Check if condition is a UnaryOperation (negation)
		// require(!initialized, msg) should become: if initialized { panic(msg) }
		if len(argNodes) > 0 {
			argNode := argNodes[0]
			if argNode.NodeType == "UnaryOperation" && argNode.Operator == "!" {
				// Get the sub-expression (e.g., "initialized" from "!initialized")
				var subExpr string
				if argNode.SubExpression != nil {
					subExpr = transformExpression(argNode.SubExpression, warnings)
				} else if len(argNode.Children) > 0 {
					subExpr = transformExpression(&argNode.Children[0], warnings)
				}
				if subExpr != "" {
					if msg != "" {
						return fmt.Sprintf("if %s { panic(%s) }", subExpr, msg)
					}
					return fmt.Sprintf("if %s { panic(\"require failed\") }", subExpr)
				}
			}

			// Check if condition is a BinaryOperation with != and address(0)
			// require(_tokenA != address(0), msg) should become: if _tokenA == nil { panic(msg) }
			if argNode.NodeType == "BinaryOperation" {
				if argNode.Operator == "!=" {
					// Check for address(0) comparison - check both AST node and transformed string
					leftIsZero := isAddressZeroCall(argNode.LeftExpression)
					rightIsZero := isAddressZeroCall(argNode.RightExpression)

					// Also check if transformed value is "(0)" which indicates address(0)
					leftStr := ""
					rightStr := ""
					if argNode.LeftExpression != nil {
						leftStr = transformExpression(argNode.LeftExpression, warnings)
					}
					if argNode.RightExpression != nil {
						rightStr = transformExpression(argNode.RightExpression, warnings)
					}

					// Check if either side transformed to "(0)" which is address(0) literal
					if leftStr == "(0)" {
						leftIsZero = true
					}
					if rightStr == "(0)" {
						rightIsZero = true
					}

					if leftIsZero || rightIsZero {
						// Get the non-zero side
						var nonZero string
						if leftIsZero {
							nonZero = rightStr
						} else {
							nonZero = leftStr
						}
						if nonZero != "" && nonZero != "(0)" {
							if msg != "" {
								return fmt.Sprintf("if %s == nil { panic(%s) }", nonZero, msg)
							}
							return fmt.Sprintf("if %s == nil { panic(\"require failed\") }", nonZero)
						}
					}
				}

				// Check for == comparison between two addresses (require _tokenA != _tokenB becomes require(_tokenA != _tokenB))
				if argNode.Operator == "!=" {
					leftType := getExpressionType(argNode.LeftExpression)
					rightType := getExpressionType(argNode.RightExpression)

					// If both are addresses and not address(0), handle the negation properly
					if (isAddressType(leftType) || isAddressType(rightType)) && !isAddressZeroCall(argNode.LeftExpression) && !isAddressZeroCall(argNode.RightExpression) {
						leftStr := transformExpression(argNode.LeftExpression, warnings)
						rightStr := transformExpression(argNode.RightExpression, warnings)

						// Skip if either is "(0)" (address zero)
						if leftStr != "(0)" && rightStr != "(0)" {
							// require(a != b) -> if util.Equals(a, b) { panic }
							if msg != "" {
								return fmt.Sprintf("if util.Equals(%s, %s) { panic(%s) }", leftStr, rightStr, msg)
							}
							return fmt.Sprintf("if util.Equals(%s, %s) { panic(\"require failed\") }", leftStr, rightStr)
						}
					}
				}
			}
		}

		if msg != "" {
			return fmt.Sprintf("if !(%s) { panic(%s) }", cond, msg)
		}
		return fmt.Sprintf("if !(%s) { panic(\"require failed\") }", cond)
	case "assert":
		// assert(condition) -> NeoVM ASSERT
		cond := ""
		if len(args) > 0 {
			cond = args[0]
		}
		return fmt.Sprintf("if !(%s) { panic(\"assert failed\") }", cond)
	case "revert":
		// revert() or revert("message")
		if len(args) > 0 {
			return fmt.Sprintf("panic(%s)", args[0])
		}
		return "panic(\"reverted\")"
	case "keccak256":
		if len(args) == 0 {
			return "__keccak256(nil)"
		}
		return fmt.Sprintf("__keccak256(%s)", args[0])
	case "sha256":
		return fmt.Sprintf("crypto.Sha256(%s)", strings.Join(args, ", "))
	case "ripemd160":
		return fmt.Sprintf("crypto.Ripemd160(%s)", strings.Join(args, ", "))
	case "ecrecover":
		return fmt.Sprintf("crypto.Ecrecover(%s)", strings.Join(args, ", "))
	case "gasleft":
		return "runtime.GasLeft()"
	case "address", "address payable", "payable":
		if len(args) == 1 {
			if len(argNodes) > 0 && isZeroNumericLiteral(&argNodes[0]) {
				return "nil"
			}
			return args[0]
		}
		return "nil"
	case "bytes1", "bytes2", "bytes4", "bytes8", "bytes16", "bytes32":
		if size, ok := fixedBytesSize(funcName); ok {
			if len(args) == 0 {
				return fmt.Sprintf("make([]byte, %d)", size)
			}
			if len(argNodes) > 0 && isZeroNumericLiteral(&argNodes[0]) {
				return fmt.Sprintf("make([]byte, %d)", size)
			}
			return fmt.Sprintf("__toFixedBytes(%s, %d)", args[0], size)
		}
	}

	goName := MapBuiltin(funcName)

	// Check if this is a struct constructor call (function name matches a struct)
	// Generate struct literal: StructName{field1: val1, field2: val2, ...}
	if len(stmt.Arguments) > 0 && stmt.Expression != nil {
		if isStructType(funcName) {
			var fields []string
			for _, arg := range args {
				fields = append(fields, arg)
			}
			return fmt.Sprintf("%s{%s}", funcName, strings.Join(fields, ", "))
		}
	}

	return fmt.Sprintf("%s(%s)", goName, strings.Join(args, ", "))
}

func getStaticNamespaceCall(stmt *parser.SolidityASTNode) (string, string, bool) {
	if stmt == nil || stmt.Expression == nil {
		return "", "", false
	}

	expr := stmt.Expression
	if expr.NodeType == "FunctionCallOptions" && expr.Expression != nil {
		expr = expr.Expression
	}
	if expr.NodeType != "MemberAccess" || expr.Expression == nil || expr.Expression.NodeType != "Identifier" {
		return "", "", false
	}

	namespace := expr.Expression.Name
	member := expr.MemberName
	if member == "" && len(expr.Children) > 1 {
		member = expr.Children[1].Name
	}
	if namespace == "" || member == "" {
		return "", "", false
	}
	return namespace, member, true
}

func isPotentialLibraryNamespace(name string) bool {
	if name == "" {
		return false
	}
	c := name[0]
	return c >= 'A' && c <= 'Z'
}

func isSpecialNamespace(name string) bool {
	switch name {
	case "NativeCalls", "Syscalls":
		return true
	default:
		return false
	}
}

func tryTransformAddressTransferCall(stmt *parser.SolidityASTNode, warnings *WarningsCollector) (string, bool) {
	if stmt == nil || stmt.Expression == nil {
		return "", false
	}

	callExpr := stmt.Expression
	if callExpr.NodeType == "FunctionCallOptions" && callExpr.Expression != nil {
		callExpr = callExpr.Expression
	}

	if callExpr.NodeType != "MemberAccess" {
		return "", false
	}

	member := callExpr.MemberName
	if member != "send" && member != "transfer" {
		return "", false
	}

	target := "nil"
	if callExpr.Expression != nil {
		target = transformExpression(callExpr.Expression, warnings)
	}

	amount := "0"
	if len(stmt.Arguments) > 0 {
		amount = transformExpression(&stmt.Arguments[0], warnings)
	}

	if member == "send" {
		return fmt.Sprintf("gas.Transfer(runtime.GetExecutingScriptHash(), %s, %s, nil)", target, amount), true
	}

	return fmt.Sprintf("func() bool { if !gas.Transfer(runtime.GetExecutingScriptHash(), %s, %s, nil) { panic(\"reverted\") }; return true }()", target, amount), true
}

func isBuiltinFunction(name string) bool {
	builtins := []string{"require", "assert", "revert", "keccak256", "sha256", "ripemd160", "ecrecover", "gasleft",
		"addmod", "mulmod", "abi", "blockhash", "address", "uint", "uint256", "int", "int256",
		"bytes", "string", "bool"}
	for _, b := range builtins {
		if strings.EqualFold(name, b) {
			return true
		}
	}
	return false
}

func isLowLevelCallExpression(node *parser.SolidityASTNode) bool {
	if node == nil || node.NodeType != "FunctionCall" || node.Expression == nil {
		return false
	}

	if node.Expression.NodeType == "MemberAccess" && node.Expression.MemberName == "call" {
		return true
	}

	if node.Expression.NodeType == "FunctionCallOptions" && node.Expression.Expression != nil {
		return node.Expression.Expression.NodeType == "MemberAccess" && node.Expression.Expression.MemberName == "call"
	}

	return false
}

func transformLowLevelCall(node *parser.SolidityASTNode, warnings *WarningsCollector) string {
	target, value, data := extractLowLevelCallParts(node, warnings)
	warnings.AddWarning("Low-level .call lowered to __lowLevelCall helper (stub semantics)")
	return fmt.Sprintf("__lowLevelCall(%s, %s, %s)", target, value, data)
}

func extractLowLevelCallParts(node *parser.SolidityASTNode, warnings *WarningsCollector) (target string, value string, data string) {
	target = "nil"
	value = "0"
	data = "nil"

	if node == nil {
		return
	}

	if len(node.Arguments) > 0 {
		data = transformExpression(&node.Arguments[0], warnings)
	} else if len(node.Children) > 1 {
		data = transformExpression(&node.Children[1], warnings)
	}

	if node.Expression == nil {
		return
	}

	if node.Expression.NodeType == "MemberAccess" {
		callTarget := node.Expression.Expression
		if callTarget != nil {
			target = transformExpression(callTarget, warnings)
		}
		return
	}

	if node.Expression.NodeType != "FunctionCallOptions" {
		return
	}

	if node.Expression.Expression != nil && node.Expression.Expression.NodeType == "MemberAccess" {
		callTarget := node.Expression.Expression.Expression
		if callTarget != nil {
			target = transformExpression(callTarget, warnings)
		}
	}

	if len(node.Expression.Options) > 0 {
		for i, opt := range node.Expression.Options {
			optName := ""
			if i < len(node.Expression.Names) {
				optName = node.Expression.Names[i]
			}
			if optName == "value" || (optName == "" && i == 0) {
				value = transformExpression(&opt, warnings)
				break
			}
		}
	}

	return
}

func transformUnaryOperation(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	operator := stmt.Operator

	if operator == "++" || operator == "--" {
		operandNode := getUnaryOperandNode(stmt)
		if operandNode == nil {
			return ""
		}

		isPrefix := stmt.Prefix
		stepOp := "+"
		if operator == "--" {
			stepOp = "-"
		}

		// Mapping-backed/state-backed index updates need explicit storage get/put.
		if operandNode.NodeType == "IndexAccess" {
			if operandNode.BaseExpression != nil && shouldUseDirectArrayIndex(operandNode.BaseExpression) && !isMappingBackedIndexAccess(operandNode) {
				indexExpr := "0"
				if operandNode.IndexExpression != nil {
					indexExpr = transformExpression(operandNode.IndexExpression, warnings)
				} else if len(operandNode.Children) > 1 {
					indexExpr = transformExpression(&operandNode.Children[1], warnings)
				}
				baseExpr := transformExpression(operandNode.BaseExpression, warnings)
				lvalue := fmt.Sprintf("%s[%s]", baseExpr, indexExpr)
				return buildIncDecLValueExpr(lvalue, stepOp, isPrefix)
			}

			storageKey := buildStorageKey(operandNode, warnings)
			if isPrefix {
				return fmt.Sprintf("func() int { __key := %s; __new := getIntFromDB(__key) %s 1; storage.Put(ctx, __key, __new); return __new }()", storageKey, stepOp)
			}
			return fmt.Sprintf("func() int { __key := %s; __old := getIntFromDB(__key); storage.Put(ctx, __key, __old %s 1); return __old }()", storageKey, stepOp)
		}

		lvalue := transformExpression(operandNode, warnings)
		if lvalue == "" {
			return ""
		}
		return buildIncDecLValueExpr(lvalue, stepOp, isPrefix)
	}

	if stmt.SubExpression != nil {
		operand := transformExpression(stmt.SubExpression, warnings)
		return fmt.Sprintf("%s%s", operator, operand)
	}
	// Fallback: Children[0] is the subexpression
	if len(stmt.Children) > 0 {
		operand := transformExpression(&stmt.Children[0], warnings)
		return fmt.Sprintf("%s%s", operator, operand)
	}
	return ""
}

func getUnaryOperandNode(stmt *parser.SolidityASTNode) *parser.SolidityASTNode {
	if stmt == nil {
		return nil
	}
	if stmt.SubExpression != nil {
		return stmt.SubExpression
	}
	if len(stmt.Children) > 0 {
		return &stmt.Children[0]
	}
	return nil
}

func buildIncDecLValueExpr(lvalue, stepOp string, isPrefix bool) string {
	if isPrefix {
		return fmt.Sprintf("func() int { %s = %s %s 1; return %s }()", lvalue, lvalue, stepOp, lvalue)
	}
	return fmt.Sprintf("func() int { __old := %s; %s = %s %s 1; return __old }()", lvalue, lvalue, lvalue, stepOp)
}

func transformBinaryOperation(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	var left, right, operator string
	var leftType, rightType string
	var leftNode, rightNode *parser.SolidityASTNode

	if stmt.LeftExpression != nil && stmt.RightExpression != nil {
		left = transformExpression(stmt.LeftExpression, warnings)
		right = transformExpression(stmt.RightExpression, warnings)
		leftType = getExpressionType(stmt.LeftExpression)
		rightType = getExpressionType(stmt.RightExpression)
		leftNode = stmt.LeftExpression
		rightNode = stmt.RightExpression
		operator = stmt.Operator
	} else if len(stmt.Children) >= 2 {
		left = transformExpression(&stmt.Children[0], warnings)
		right = transformExpression(&stmt.Children[1], warnings)
		leftType = getExpressionType(&stmt.Children[0])
		rightType = getExpressionType(&stmt.Children[1])
		leftNode = &stmt.Children[0]
		rightNode = &stmt.Children[1]
		operator = stmt.Operator
	} else {
		return ""
	}

	// Preserve Solidity AST operator grouping in generated Go expressions.
	if leftNode != nil && leftNode.NodeType == "BinaryOperation" {
		left = fmt.Sprintf("(%s)", left)
	}
	if rightNode != nil && rightNode.NodeType == "BinaryOperation" {
		right = fmt.Sprintf("(%s)", right)
	}

	if operator == "**" {
		return fmt.Sprintf("pow(%s, %s)", left, right)
	}

	leftIsAddressZero := isAddressZeroCall(leftNode)
	rightIsAddressZero := isAddressZeroCall(rightNode)

	if operator == "==" || operator == "!=" {
		if leftIsAddressZero && isAddressType(rightType) {
			if operator == "==" {
				return fmt.Sprintf("%s == nil", right)
			} else {
				return fmt.Sprintf("%s != nil", right)
			}
		}
		if rightIsAddressZero && isAddressType(leftType) {
			if operator == "==" {
				return fmt.Sprintf("%s == nil", left)
			} else {
				return fmt.Sprintf("%s != nil", left)
			}
		}

		isAddressComparison := (isAddressType(leftType) || isAddressType(rightType))

		if isAddressComparison {
			if operator == "==" {
				return fmt.Sprintf("util.Equals(%s, %s)", left, right)
			} else {
				return fmt.Sprintf("!util.Equals(%s, %s)", left, right)
			}
		}

		isBytesComparison := isBytesType(leftType) || isBytesType(rightType)
		if isBytesComparison {
			if operator == "==" {
				return fmt.Sprintf("util.Equals(%s, %s)", left, right)
			}
			return fmt.Sprintf("!util.Equals(%s, %s)", left, right)
		}
	}

	return fmt.Sprintf("%s %s %s", left, MapOperator(operator), right)
}

// isAddressZeroCall checks if a node is address(0) call
func isAddressZeroCall(node *parser.SolidityASTNode) bool {
	if node == nil {
		return false
	}

	// Check if it's a FunctionCall to "address"
	if node.NodeType == "FunctionCall" {
		// Check expression
		if node.Expression != nil && node.Expression.Name == "address" {
			// Check if argument is 0
			if len(node.Arguments) > 0 {
				arg := node.Arguments[0]
				if arg.NodeType == "Literal" && arg.Kind == "number" {
					if arg.Value != nil {
						// Check if value is 0
						if val, ok := arg.Value.(float64); ok && val == 0 {
							return true
						}
						if val, ok := arg.Value.(int); ok && val == 0 {
							return true
						}
						if fmt.Sprintf("%v", arg.Value) == "0" {
							return true
						}
					}
				}
			}
		}
	}

	return false
}

func isZeroNumericLiteral(node *parser.SolidityASTNode) bool {
	if node == nil {
		return false
	}
	if node.NodeType != "Literal" {
		return false
	}
	if node.Kind == "number" || strings.HasPrefix(node.Kind, "int") || strings.HasPrefix(node.Kind, "uint") {
		if node.Value != nil && fmt.Sprintf("%v", node.Value) == "0" {
			return true
		}
	}
	return false
}

func fixedBytesSize(typeName string) (int, bool) {
	if !strings.HasPrefix(typeName, "bytes") || typeName == "bytes" {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(typeName, "bytes"))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// getExpressionType returns the type of an expression if available
func getExpressionType(expr *parser.SolidityASTNode) string {
	if expr == nil {
		return ""
	}

	// Check TypeDescriptions first (most reliable)
	if expr.TypeDescriptions != nil && expr.TypeDescriptions.TypeString != "" {
		return expr.TypeDescriptions.TypeString
	}

	// Check for address literal
	if expr.NodeType == "Literal" && expr.Kind == "address" {
		return "address"
	}

	// Check MemberAccess for msg.sender
	if expr.NodeType == "MemberAccess" {
		if expr.Expression != nil && expr.Expression.Name == "msg" && expr.MemberName == "sender" {
			return "address"
		}
	}

	// Check Identifier - look up variable type if possible
	if expr.NodeType == "Identifier" {
		if expr.Name != "" {
			if typeStr, ok := currentContractVariableTypes[expr.Name]; ok {
				return typeStr
			}
		}
		return ""
	}

	return ""
}

// isAddressType checks if a type string represents an address type
func isAddressType(typeStr string) bool {
	if typeStr == "" {
		return false
	}
	return strings.Contains(typeStr, "address") || strings.Contains(typeStr, "Hash160")
}

func isAddressLikeExpression(expr *parser.SolidityASTNode) bool {
	if expr == nil {
		return false
	}

	if isAddressType(getExpressionType(expr)) {
		return true
	}

	switch expr.NodeType {
	case "Identifier":
		return expr.Name == "this"
	case "MemberAccess":
		return expr.Expression != nil && expr.Expression.NodeType == "Identifier" && expr.Expression.Name == "msg" && expr.MemberName == "sender"
	case "FunctionCall":
		if expr.Expression == nil {
			return false
		}
		if expr.Expression.NodeType == "Identifier" {
			return expr.Expression.Name == "address" || expr.Expression.Name == "payable"
		}
		if expr.Expression.NodeType == "ElementaryTypeNameExpression" && expr.Expression.TypeName != nil {
			return expr.Expression.TypeName.Name == "address"
		}
	}

	return false
}

func isBytesType(typeStr string) bool {
	if typeStr == "" {
		return false
	}
	if strings.Contains(typeStr, "bytes") {
		return true
	}
	return strings.Contains(typeStr, "[]byte")
}

func transformMemberAccess(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	if stmt.Expression != nil {
		isMsgIdentifier := stmt.Expression.NodeType == "Identifier" && stmt.Expression.Name == "msg"
		isBlockIdentifier := stmt.Expression.NodeType == "Identifier" && stmt.Expression.Name == "block"
		isAddressIdentifier := stmt.Expression.NodeType == "Identifier" && stmt.Expression.Name == "address"

		obj := transformExpression(stmt.Expression, warnings)

		member := stmt.MemberName
		if member == "" && len(stmt.Children) > 1 {
			member = stmt.Children[1].Name
		}

		if (member == "max" || member == "min") && stmt.Expression.NodeType == "FunctionCall" {
			if typeName := extractTypeBuiltinArgName(stmt.Expression); typeName != "" {
				if limitExpr, ok := typeLimitExpr(typeName, member); ok {
					return limitExpr
				}
			}
		}

		if stmt.Expression.NodeType == "Identifier" && stmt.Expression.Name == "this" {
			if mappedName, ok := currentContractFunctionNames[member]; ok {
				return mappedName
			}
		}
		if stmt.Expression.NodeType == "Identifier" && currentContractEnumNames[stmt.Expression.Name] {
			return fmt.Sprintf("%s_%s", stmt.Expression.Name, member)
		}

		if isMsgIdentifier && member == "sender" {
			return "runtime.GetCallingScriptHash()"
		}
		if isMsgIdentifier && member == "value" {
			return "0"
		}

		if isBlockIdentifier {
			switch member {
			case "timestamp":
				return "runtime.GetTime()"
			case "number":
				return "ledger.CurrentIndex()"
			case "coinbase":
				return "ledger.GetBlock(ledger.CurrentIndex()).NextConsensus"
			case "difficulty", "gaslimit", "prevrandao":
				return "0"
			}
		}

		if isAddressIdentifier && member == "this" {
			return "runtime.GetExecutingScriptHash()"
		}
		if stmt.Expression.NodeType == "Identifier" && stmt.Expression.Name == "NativeCalls" && member == "GAS_CONTRACT" {
			return "nil"
		}

		// Handle push on arrays (both simple identifiers and index accesses like mapping[...].push)
		if member == "push" {
			// Check if this is a state array (tracked in currentContractStorageArrays)
			if stmt.Expression.NodeType == "Identifier" {
				arrayName := stmt.Expression.Name
				if isStorageArray(arrayName) {
					// Return special marker for state array push
					return fmt.Sprintf("__storagePush__%s", arrayName)
				}
			}
			// Check if expression is an IndexAccess (nested mapping array)
			if stmt.Expression.NodeType == "IndexAccess" {
				storageKey := buildStorageKey(stmt.Expression, warnings)
				return fmt.Sprintf("__append__%s", storageKey)
			}
			// Check if expression is an array type
			if stmt.Expression != nil && stmt.Expression.TypeDescriptions != nil {
				typeStr := stmt.Expression.TypeDescriptions.TypeString
				if strings.Contains(typeStr, "[]") {
					// This is array.push() - return special marker for function call handling
					return fmt.Sprintf("__append__%s", obj)
				}
			}
		}

		// Handle length on arrays
		if member == "length" {
			// Check if this is a state array
			if stmt.Expression.NodeType == "Identifier" {
				arrayName := stmt.Expression.Name
				if isStorageArray(arrayName) {
					return fmt.Sprintf("%sCount", arrayName)
				}
			}
			if stmt.Expression.NodeType == "MemberAccess" && stmt.Expression.MemberName == "code" {
				return fmt.Sprintf("len(%s)", obj)
			}
			if stmt.Expression != nil && stmt.Expression.TypeDescriptions != nil {
				typeStr := stmt.Expression.TypeDescriptions.TypeString
				if strings.Contains(typeStr, "[]") || strings.Contains(typeStr, "bytes") || strings.Contains(typeStr, "string") {
					return fmt.Sprintf("len(%s)", obj)
				}
			}
		}

		if stmt.Expression.NodeType == "Identifier" {
			// Check for state array first
			arrayName := stmt.Expression.Name
			if isStorageArray(arrayName) {
				switch member {
				case "length":
					return fmt.Sprintf("%sCount", arrayName)
				case "push":
					return fmt.Sprintf("__storagePush__%s", arrayName)
				}
			}

			if stmt.Expression.TypeDescriptions != nil {
				typeStr := stmt.Expression.TypeDescriptions.TypeString
				if strings.Contains(typeStr, "enum") {
					return fmt.Sprintf("%s_%s", obj, member)
				}
				if strings.HasSuffix(typeStr, "[]") || strings.Contains(typeStr, "[]") {
					switch member {
					case "length":
						return fmt.Sprintf("len(%s)", obj)
					case "push":
						// For arrays in mappings, build the storage key directly
						if stmt.Expression.NodeType == "IndexAccess" {
							storageKey := buildStorageKey(stmt.Expression, warnings)
							return fmt.Sprintf("__append__%s", storageKey)
						}
						return fmt.Sprintf("__append__%s", obj)
					case "pop":
						return fmt.Sprintf("%s = %s[:len(%s)-1]", obj, obj, obj)
					}
				}
			}

			if strings.EqualFold(stmt.Expression.Name, "this") {
				switch member {
				case "balance":
					return "gas.BalanceOf(runtime.GetExecutingScriptHash())"
				}
			}
		}

		if stmt.Expression.NodeType == "MemberAccess" {
			outerMember := stmt.Expression.MemberName
			if outerMember == "this" && member == "balance" {
				return "gas.BalanceOf(runtime.GetExecutingScriptHash())"
			}
			if outerMember == "this" {
				return fmt.Sprintf("runtime.GetExecutingScriptHash().%s", member)
			}
		}

		if stmt.Expression.NodeType == "FunctionCall" {
			if stmt.Expression.Expression != nil {
				exprName := ""
				if stmt.Expression.Expression.NodeType == "ElementaryTypeNameExpression" && stmt.Expression.Expression.TypeName != nil {
					exprName = stmt.Expression.Expression.TypeName.Name
				} else if stmt.Expression.Expression.NodeType == "Identifier" {
					exprName = stmt.Expression.Expression.Name
				}
				if exprName == "address" {
					if len(stmt.Expression.Arguments) > 0 {
						arg := stmt.Expression.Arguments[0]
						if arg.NodeType == "Identifier" && arg.Name == "this" {
							switch member {
							case "balance":
								return "gas.BalanceOf(runtime.GetExecutingScriptHash())"
							}
						}
					}
					if member == "balance" {
						target := "runtime.GetExecutingScriptHash()"
						if len(stmt.Expression.Arguments) > 0 {
							target = transformExpression(&stmt.Expression.Arguments[0], warnings)
						}
						return fmt.Sprintf("gas.BalanceOf(%s)", target)
					}
				}
			}
		}

		if member == "balance" && isAddressLikeExpression(stmt.Expression) {
			return fmt.Sprintf("gas.BalanceOf(%s)", obj)
		}
		if member == "code" && isAddressLikeExpression(stmt.Expression) {
			warnings.AddWarning("address.code lowered to contract-existence marker via management.GetContract")
			return fmt.Sprintf("__addressCode(%s)", obj)
		}

		return fmt.Sprintf("%s.%s", obj, member)
	}

	if len(stmt.Children) > 1 {
		obj := transformExpression(&stmt.Children[0], warnings)
		member := stmt.Children[1].Name
		if obj == "msg" && member == "sender" {
			return "runtime.GetCallingScriptHash()"
		}
		return fmt.Sprintf("%s.%s", obj, member)
	}
	return ""
}

func extractTypeBuiltinArgName(call *parser.SolidityASTNode) string {
	if call == nil || call.NodeType != "FunctionCall" || call.Expression == nil {
		return ""
	}
	if call.Expression.NodeType != "Identifier" || call.Expression.Name != "type" {
		return ""
	}
	if len(call.Arguments) == 0 {
		return ""
	}

	arg := &call.Arguments[0]
	if arg.TypeName != nil && arg.TypeName.Name != "" {
		return arg.TypeName.Name
	}
	if arg.TypeDescriptions != nil {
		typeStr := strings.TrimSpace(arg.TypeDescriptions.TypeString)
		if strings.HasPrefix(typeStr, "type(") && strings.HasSuffix(typeStr, ")") {
			return strings.TrimSuffix(strings.TrimPrefix(typeStr, "type("), ")")
		}
	}
	return ""
}

func typeLimitExpr(typeName string, member string) (string, bool) {
	t := strings.TrimSpace(typeName)
	if t == "" {
		return "", false
	}

	isUnsigned := strings.HasPrefix(t, "uint")
	isSigned := strings.HasPrefix(t, "int")
	if t == "uint" {
		isUnsigned = true
	}
	if t == "int" {
		isSigned = true
	}
	if !isUnsigned && !isSigned {
		return "", false
	}

	switch member {
	case "max":
		return "int(^uint(0) >> 1)", true
	case "min":
		if isUnsigned {
			return "0", true
		}
		return "(-int(^uint(0)>>1) - 1)", true
	default:
		return "", false
	}
}

func transformIdentifier(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	name := stmt.Name

	if isStorageArray(name) {
		arrayType := currentContractVariableTypes[name]
		if strings.HasPrefix(arrayType, "[]") {
			elemType := strings.TrimPrefix(arrayType, "[]")
			elemZero := zeroValueForType(elemType)
			return fmt.Sprintf("func() %s { out := make(%s, 0); for i := 0; i < %sCount; i++ { _v := storage.Get(ctx, append([]byte(%q), convert.ToBytes(i)...)); if _v == nil { out = append(out, %s) } else { out = append(out, _v.(%s)) } }; return out }()", arrayType, arrayType, name, name+":", elemZero, elemType)
		}
	}

	switch name {
	case "msg":
		return "runtime.GetCallingScriptHash()"
	case "block":
		return "ledger.CurrentIndex()"
	case "tx":
		return "util.TxGet()"
	case "this":
		return "runtime.GetExecutingScriptHash()"
	default:
		return name
	}
}

func transformLiteral(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	// Check for bool kind first - return unquoted true/false
	if stmt.Kind == "bool" {
		if stmt.Value != nil {
			// Return the boolean value without quotes
			return fmt.Sprintf("%v", stmt.Value)
		}
		// Check hexValue for "true" or "false"
		if stmt.HexValue == "74727565" { // hex for "true"
			return "true"
		}
		if stmt.HexValue == "66616c7365" { // hex for "false"
			return "false"
		}
		return "false"
	}

	// Check for string kind - needs quotes
	if stmt.Kind == "string" {
		if stmt.Value != nil {
			return fmt.Sprintf("%q", stmt.Value)
		}
		// Try hexValue for string literals
		if stmt.HexValue != "" {
			// Decode hex to get the actual string
			if decoded, err := hex.DecodeString(stmt.HexValue); err == nil {
				return fmt.Sprintf("%q", string(decoded))
			}
			// If decode fails, return as-is with quotes
			return fmt.Sprintf("%q", stmt.HexValue)
		}
	}

	// Check for address type - return as-is (will be handled by type mapping)
	if stmt.Kind == "address" {
		if stmt.Value != nil {
			return fmt.Sprintf("%v", stmt.Value)
		}
	}

	// Check for number types - don't quote numbers
	if stmt.Kind == "number" || stmt.Kind == "uint" || stmt.Kind == "int" ||
		stmt.Kind == "bytes" || strings.HasPrefix(stmt.Kind, "uint") ||
		strings.HasPrefix(stmt.Kind, "int") || strings.HasPrefix(stmt.Kind, "bytes") {
		if stmt.Value != nil {
			return fmt.Sprintf("%v", stmt.Value)
		}
	}

	// Return the literal value as-is
	if stmt.Value != nil {
		// Check if it's a string that needs quoting
		if strVal, ok := stmt.Value.(string); ok {
			return fmt.Sprintf("%q", strVal)
		}
		return fmt.Sprintf("%v", stmt.Value)
	}

	// Try to get from attributes
	if stmt.Attributes != nil {
		if valAttr, ok := stmt.Attributes["value"]; ok {
			var val interface{}
			if err := json.Unmarshal(valAttr, &val); err == nil {
				if strVal, ok := val.(string); ok {
					return fmt.Sprintf("%q", strVal)
				}
				return fmt.Sprintf("%v", val)
			}
		}
		// Check for kind in attributes
		if kindAttr, ok := stmt.Attributes["kind"]; ok {
			var kind string
			if err := json.Unmarshal(kindAttr, &kind); err == nil {
				if kind == "number" || kind == "uint" || kind == "int" ||
					strings.HasPrefix(kind, "uint") || strings.HasPrefix(kind, "int") {
					if valAttr, ok := stmt.Attributes["value"]; ok {
						var val interface{}
						if err := json.Unmarshal(valAttr, &val); err == nil {
							return fmt.Sprintf("%v", val)
						}
					}
				}
			}
		}
	}

	return ""
}

// formatNodeValue formats a value from AST (can be a direct value or a nested node)
func formatNodeValue(val interface{}) string {
	// Check if it's a nested node (map)
	if m, ok := val.(map[string]interface{}); ok {
		if nodeType, hasNodeType := m["nodeType"].(string); hasNodeType && nodeType != "" {
			if rawNode, err := json.Marshal(m); err == nil {
				var node parser.SolidityASTNode
				if err := json.Unmarshal(rawNode, &node); err == nil {
					expr := transformExpression(&node, &WarningsCollector{})
					if expr != "" && !strings.HasPrefix(expr, "/*") {
						return expr
					}
				}
			}
		}

		// Try to get the value field
		if v, exists := m["value"]; exists {
			return formatConstantValue(v)
		}
		// Try to get from typeDescriptions
		if td, exists := m["typeDescriptions"]; exists {
			if tdMap, ok := td.(map[string]interface{}); ok {
				if typeStr, ok := tdMap["typeString"].(string); ok {
					// Extract number from type string like "int_const 1000000000000000000"
					if strings.HasPrefix(typeStr, "int_const ") {
						return strings.TrimPrefix(typeStr, "int_const ")
					}
				}
			}
		}
		return fmt.Sprintf("%v", val)
	}
	return formatConstantValue(val)
}

// formatConstantValue formats a constant value for Go output
func formatConstantValue(val interface{}) string {
	switch v := val.(type) {
	case string:
		// Check if it's a number string like "1e18"
		if _, err := strconv.ParseFloat(v, 64); err == nil {
			return v
		}
		// It's a string literal
		return fmt.Sprintf("%q", v)
	case float64:
		// Format as integer if it's a whole number
		if v == float64(int(v)) {
			return fmt.Sprintf("%d", int(v))
		}
		return fmt.Sprintf("%v", v)
	case int:
		return fmt.Sprintf("%d", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func canUseConstInitializer(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" || v == "nil" {
		return false
	}
	if v == "true" || v == "false" {
		return true
	}
	if strings.HasPrefix(v, "\"") && strings.HasSuffix(v, "\"") {
		return true
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return true
	}
	return false
}

func transformExpression(expr *parser.SolidityASTNode, warnings *WarningsCollector) string {
	if expr == nil {
		return ""
	}

	switch expr.NodeType {
	case "FunctionCall":
		return transformFunctionCall(expr, warnings)
	case "Identifier":
		return transformIdentifier(expr, warnings)
	case "MemberAccess":
		return transformMemberAccess(expr, warnings)
	case "BinaryOperation":
		return transformBinaryOperation(expr, warnings)
	case "UnaryOperation":
		return transformUnaryOperation(expr, warnings)
	case "Literal":
		return transformLiteral(expr, warnings)
	case "Assignment":
		return transformAssignment(expr, warnings)
	case "Return":
		if expr.Expression != nil {
			return transformExpression(expr.Expression, warnings)
		}
		return "/* Return */"
	case "IndexAccess":
		return transformIndexAccess(expr, warnings)
	case "TupleExpression":
		return transformTupleExpression(expr, warnings)
	case "Conditional":
		return transformConditional(expr, warnings)
	case "StructDefinition":
		// Struct definition - should not appear as expression
		return fmt.Sprintf("/* struct %s */", expr.Name)
	default:
		return fmt.Sprintf("/* %s */", expr.NodeType)
	}
}

func transformAssignment(expr *parser.SolidityASTNode, warnings *WarningsCollector) string {
	if expr.LeftHandSide != nil && expr.RightHandSide != nil {
		left := expr.LeftHandSide
		right := transformExpression(expr.RightHandSide, warnings)
		leftType := getExpressionType(left)
		operator := expr.Operator
		if operator == "" {
			operator = "="
		}

		if left.NodeType == "TupleExpression" && isLowLevelCallExpression(expr.RightHandSide) {
			return transformLowLevelCallTupleAssignment(left, expr.RightHandSide, warnings)
		}

		if operator == "=" && isBytesType(leftType) && strings.HasPrefix(right, "\"") && strings.HasSuffix(right, "\"") {
			right = fmt.Sprintf("[]byte(%s)", right)
		}

		if left.NodeType == "IndexAccess" {
			if arrayRef, ok := getStorageArrayElementReference(left, warnings); ok {
				return transformStorageArrayElementAssignment(arrayRef, right, operator)
			}

			if left.BaseExpression != nil && shouldUseDirectArrayIndex(left.BaseExpression) && !isMappingBackedIndexAccess(left) {
				baseExpr := transformExpression(left.BaseExpression, warnings)
				indexExpr := "0"
				if left.IndexExpression != nil {
					indexExpr = transformExpression(left.IndexExpression, warnings)
				} else if len(left.Children) > 1 {
					indexExpr = transformExpression(&left.Children[1], warnings)
				}
				leftExpr := fmt.Sprintf("%s[%s]", baseExpr, indexExpr)

				if operator == "+=" || operator == "-=" || operator == "*=" || operator == "/=" {
					switch operator {
					case "+=":
						return fmt.Sprintf("\t%s = %s + %s", leftExpr, leftExpr, right)
					case "-=":
						return fmt.Sprintf("\t%s = %s - %s", leftExpr, leftExpr, right)
					case "*=":
						return fmt.Sprintf("\t%s = %s * %s", leftExpr, leftExpr, right)
					case "/=":
						return fmt.Sprintf("\t%s = %s / %s", leftExpr, leftExpr, right)
					}
				}

				return fmt.Sprintf("\t%s %s %s", leftExpr, operator, right)
			}

			storageKey := buildStorageKey(left, warnings)
			if operator == "=" {
				return fmt.Sprintf("\tstorage.Put(ctx, %s, %s)", storageKey, right)
			}
			if operator == "+=" || operator == "-=" || operator == "*=" || operator == "/=" {
				currentVal := fmt.Sprintf("getIntFromDB(%s)", storageKey)
				var newVal string
				switch operator {
				case "+=":
					newVal = fmt.Sprintf("%s + %s", currentVal, right)
				case "-=":
					newVal = fmt.Sprintf("%s - %s", currentVal, right)
				case "*=":
					newVal = fmt.Sprintf("%s * %s", currentVal, right)
				case "/=":
					newVal = fmt.Sprintf("%s / %s", currentVal, right)
				}
				return fmt.Sprintf("\tstorage.Put(ctx, %s, %s)", storageKey, newVal)
			}
		}

		if left.NodeType == "MemberAccess" && left.Expression != nil && left.Expression.NodeType == "IndexAccess" {
			return transformStorageStructFieldAssignment(left, right, operator, warnings)
		}

		// Handle member access on storage reference variable (e.g., task.completed where task was loaded from storage)
		if left.NodeType == "MemberAccess" && left.Expression != nil && left.Expression.NodeType == "Identifier" {
			varName := left.Expression.Name
			if arrayRef, isArrayElementRef := storageArrayElementReferences[varName]; isArrayElementRef {
				memberName := left.MemberName

				assignOp := right
				if operator == "+=" || operator == "-=" || operator == "*=" || operator == "/=" {
					switch operator {
					case "+=":
						assignOp = fmt.Sprintf("%s.%s + %s", varName, memberName, right)
					case "-=":
						assignOp = fmt.Sprintf("%s.%s - %s", varName, memberName, right)
					case "*=":
						assignOp = fmt.Sprintf("%s.%s * %s", varName, memberName, right)
					case "/=":
						assignOp = fmt.Sprintf("%s.%s / %s", varName, memberName, right)
					}
				}

				return fmt.Sprintf("\t%s.%s = %s\n\t%s", varName, memberName, assignOp, buildStorageArrayElementWriteBack(arrayRef, varName))
			}

			if storageKey, isStorageRef := storageReferences[varName]; isStorageRef {
				memberName := left.MemberName
				structType := getStructTypeFromVarName(varName, left.Expression)
				if structType == "" {
					structType = "interface{}"
				}

				assignOp := right
				if operator == "+=" || operator == "-=" || operator == "*=" || operator == "/=" {
					switch operator {
					case "+=":
						assignOp = fmt.Sprintf("%s.%s + %s", varName, memberName, right)
					case "-=":
						assignOp = fmt.Sprintf("%s.%s - %s", varName, memberName, right)
					case "*=":
						assignOp = fmt.Sprintf("%s.%s * %s", varName, memberName, right)
					case "/=":
						assignOp = fmt.Sprintf("%s.%s / %s", varName, memberName, right)
					}
				}

				return fmt.Sprintf("\t%s.%s = %s\n\tstorage.Put(ctx, %s, %s)", varName, memberName, assignOp, storageKey, varName)
			}
		}

		leftExpr := transformExpression(left, warnings)

		if operator == "+=" || operator == "-=" || operator == "*=" || operator == "/=" {
			switch operator {
			case "+=":
				return fmt.Sprintf("\t%s = %s + %s", leftExpr, leftExpr, right)
			case "-=":
				return fmt.Sprintf("\t%s = %s - %s", leftExpr, leftExpr, right)
			case "*=":
				return fmt.Sprintf("\t%s = %s * %s", leftExpr, leftExpr, right)
			case "/=":
				return fmt.Sprintf("\t%s = %s / %s", leftExpr, leftExpr, right)
			}
		}

		return fmt.Sprintf("\t%s %s %s", leftExpr, operator, right)
	}
	return "\t// Assignment"
}

func transformLowLevelCallTupleAssignment(leftTuple *parser.SolidityASTNode, callNode *parser.SolidityASTNode, warnings *WarningsCollector) string {
	target, value, data := extractLowLevelCallParts(callNode, warnings)
	components := []parser.SolidityASTNode{}
	if len(leftTuple.Components) > 0 {
		components = leftTuple.Components
	} else if len(leftTuple.Children) > 0 {
		components = leftTuple.Children
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\t__llSuccess, __llRetData := __lowLevelCallWithData(%s, %s, %s)\n", target, value, data))

	assigned := 0
	for idx, comp := range components {
		if comp.NodeType == "" {
			continue
		}
		lhs := transformExpression(&comp, warnings)
		if lhs == "" || lhs == "_" {
			continue
		}

		switch idx {
		case 0:
			sb.WriteString(fmt.Sprintf("\t%s = __llSuccess\n", lhs))
		case 1:
			compType := getExpressionType(&comp)
			if compType == "string" {
				sb.WriteString(fmt.Sprintf("\t%s = string(__llRetData)\n", lhs))
			} else {
				sb.WriteString(fmt.Sprintf("\t%s = __llRetData\n", lhs))
			}
		default:
			compType := mapTypeFromASTType(getExpressionType(&comp))
			if compType == "" {
				compType = "int"
			}
			sb.WriteString(fmt.Sprintf("\t%s = %s\n", lhs, zeroValueForType(compType)))
		}
		assigned++
	}

	if assigned == 0 {
		sb.WriteString("\t_ = __llSuccess\n")
		sb.WriteString("\t_ = __llRetData\n")
	}

	warnings.AddWarning("Low-level .call tuple assignment lowered to (bool, bytes) stub return data")
	return strings.TrimSuffix(sb.String(), "\n")
}

func getStructTypeFromVarName(varName string, node *parser.SolidityASTNode) string {
	if node == nil {
		return ""
	}
	if node.TypeDescriptions != nil {
		typeStr := node.TypeDescriptions.TypeString
		if strings.HasPrefix(typeStr, "struct ") {
			parts := strings.Fields(typeStr)
			if len(parts) >= 2 {
				structName := parts[1]
				dotIdx := strings.Index(structName, ".")
				if dotIdx >= 0 {
					return structName[dotIdx+1:]
				}
				return structName
			}
		}
	}
	return ""
}

func transformStorageStructFieldAssignment(left *parser.SolidityASTNode, right string, operator string, warnings *WarningsCollector) string {
	indexAccess := left.Expression
	memberName := left.MemberName

	storageKey := buildStorageKey(indexAccess, warnings)
	structType := getMappingValueType(indexAccess)
	if structType == "" {
		structType = "interface{}"
	}

	tempVar := "_temp"
	assignOp := "="
	if operator == "+=" || operator == "-=" || operator == "*=" || operator == "/=" {
		switch operator {
		case "+=":
			assignOp = fmt.Sprintf("%s.%s + %s", tempVar, memberName, right)
		case "-=":
			assignOp = fmt.Sprintf("%s.%s - %s", tempVar, memberName, right)
		case "*=":
			assignOp = fmt.Sprintf("%s.%s * %s", tempVar, memberName, right)
		case "/=":
			assignOp = fmt.Sprintf("%s.%s / %s", tempVar, memberName, right)
		}
	} else {
		assignOp = right
	}

	return fmt.Sprintf("\t%s := storage.Get(ctx, %s).(%s)\n\t%s.%s = %s\n\tstorage.Put(ctx, %s, %s)", tempVar, storageKey, structType, tempVar, memberName, assignOp, storageKey, tempVar)
}

func transformStorageArrayElementAssignment(ref storageArrayElementReference, right string, operator string) string {
	arrayType := ref.ArrayType
	if arrayType == "" || !strings.HasPrefix(arrayType, "[]") {
		arrayType = "[]interface{}"
	}
	defaultArray := zeroValueForType(arrayType)
	tempVar := "_arrElem"

	assignExpr := right
	if operator == "+=" || operator == "-=" || operator == "*=" || operator == "/=" {
		switch operator {
		case "+=":
			assignExpr = fmt.Sprintf("%s[%s] + %s", tempVar, ref.ElementIndex, right)
		case "-=":
			assignExpr = fmt.Sprintf("%s[%s] - %s", tempVar, ref.ElementIndex, right)
		case "*=":
			assignExpr = fmt.Sprintf("%s[%s] * %s", tempVar, ref.ElementIndex, right)
		case "/=":
			assignExpr = fmt.Sprintf("%s[%s] / %s", tempVar, ref.ElementIndex, right)
		}
	}

	return fmt.Sprintf("\t%s := func() %s { _v := storage.Get(ctx, %s); if _v == nil { return %s }; return _v.(%s) }()\n\tif len(%s) > %s {\n\t\t%s[%s] = %s\n\t\tstorage.Put(ctx, %s, %s)\n\t}", tempVar, arrayType, ref.ArrayStorageKey, defaultArray, arrayType, tempVar, ref.ElementIndex, tempVar, ref.ElementIndex, assignExpr, ref.ArrayStorageKey, tempVar)
}

func buildStorageArrayElementWriteBack(ref storageArrayElementReference, valueExpr string) string {
	arrayType := ref.ArrayType
	if arrayType == "" || !strings.HasPrefix(arrayType, "[]") {
		arrayType = "[]interface{}"
	}
	defaultArray := zeroValueForType(arrayType)
	tempVar := "_arrRef"

	return fmt.Sprintf("%s := func() %s { _v := storage.Get(ctx, %s); if _v == nil { return %s }; return _v.(%s) }()\n\tif len(%s) > %s {\n\t\t%s[%s] = %s\n\t\tstorage.Put(ctx, %s, %s)\n\t}", tempVar, arrayType, ref.ArrayStorageKey, defaultArray, arrayType, tempVar, ref.ElementIndex, tempVar, ref.ElementIndex, valueExpr, ref.ArrayStorageKey, tempVar)
}

func getStorageArrayElementReference(node *parser.SolidityASTNode, warnings *WarningsCollector) (storageArrayElementReference, bool) {
	var ref storageArrayElementReference
	if node == nil || node.NodeType != "IndexAccess" || node.BaseExpression == nil {
		return ref, false
	}
	if node.BaseExpression.NodeType != "IndexAccess" {
		return ref, false
	}

	baseName := getIndexAccessBaseName(node.BaseExpression)
	if baseName == "" {
		return ref, false
	}
	mappedType, ok := currentContractMappingValueTypes[baseName]
	if !ok || !strings.HasPrefix(mappedType, "[]") {
		return ref, false
	}

	arrayType := mappedType
	if node.BaseExpression.TypeDescriptions != nil && node.BaseExpression.TypeDescriptions.TypeString != "" {
		if mapped := mapTypeFromASTType(node.BaseExpression.TypeDescriptions.TypeString); strings.HasPrefix(mapped, "[]") {
			arrayType = mapped
		}
	}

	elementIndex := "0"
	if node.IndexExpression != nil {
		elementIndex = transformExpression(node.IndexExpression, warnings)
	} else if len(node.Children) > 1 {
		elementIndex = transformExpression(&node.Children[1], warnings)
	}

	ref = storageArrayElementReference{
		ArrayStorageKey: buildStorageKey(node.BaseExpression, warnings),
		ElementIndex:    elementIndex,
		ArrayType:       arrayType,
	}
	return ref, true
}

func buildStorageKey(node *parser.SolidityASTNode, warnings *WarningsCollector) string {
	var baseName string
	var indices []string

	current := node
	for current != nil && current.NodeType == "IndexAccess" {
		var index string
		if current.IndexExpression != nil {
			index = transformExpression(current.IndexExpression, warnings)
		} else if len(current.Children) > 1 {
			index = transformExpression(&current.Children[1], warnings)
		}
		indices = append([]string{index}, indices...)

		if current.BaseExpression != nil {
			if current.BaseExpression.NodeType == "IndexAccess" {
				current = current.BaseExpression
			} else if current.BaseExpression.NodeType == "Identifier" {
				baseName = current.BaseExpression.Name
				current = nil
			} else {
				baseName = transformExpression(current.BaseExpression, warnings)
				current = nil
			}
		} else if len(current.Children) > 0 {
			if current.Children[0].NodeType == "IndexAccess" {
				current = &current.Children[0]
			} else if current.Children[0].NodeType == "Identifier" {
				baseName = current.Children[0].Name
				current = nil
			} else {
				baseName = transformExpression(&current.Children[0], warnings)
				current = nil
			}
		} else {
			current = nil
		}
	}

	result := fmt.Sprintf("[]byte(%q)", baseName+":")
	for _, idx := range indices {
		result = fmt.Sprintf("append(%s, convert.ToBytes(%s)...)", result, idx)
	}

	return result
}

func transformConditional(expr *parser.SolidityASTNode, warnings *WarningsCollector) string {
	var cond, trueExpr, falseExpr string

	if expr.Condition != nil {
		cond = transformExpression(expr.Condition, warnings)
	}
	if expr.TrueExpression != nil {
		trueExpr = transformExpression(expr.TrueExpression, warnings)
	}
	if expr.FalseExpression != nil {
		falseExpr = transformExpression(expr.FalseExpression, warnings)
	}

	if cond != "" && trueExpr != "" && falseExpr != "" {
		return fmt.Sprintf("func() int { if %s { return %s }; return %s }()", cond, trueExpr, falseExpr)
	}
	return "0"
}

func transformTupleExpression(expr *parser.SolidityASTNode, warnings *WarningsCollector) string {
	var components []string
	if len(expr.Components) > 0 {
		for i := range expr.Components {
			comp := &expr.Components[i]
			if comp != nil && comp.NodeType != "" {
				components = append(components, transformExpression(comp, warnings))
			} else {
				components = append(components, "_")
			}
		}
	}
	if len(components) == 0 && len(expr.Children) > 0 {
		for _, comp := range expr.Children {
			components = append(components, transformExpression(&comp, warnings))
		}
	}
	if len(components) > 0 {
		typeStr := ""
		if expr.TypeDescriptions != nil {
			typeStr = strings.TrimSpace(expr.TypeDescriptions.TypeString)
		}

		// Keep tuple semantics for multi-return expressions.
		if strings.HasPrefix(typeStr, "tuple(") {
			return strings.Join(components, ", ")
		}

		// Solidity fixed-size array literals are represented as TupleExpression
		// nodes. Lower them into Go slice literals.
		if strings.Contains(typeStr, "[") && !strings.Contains(typeStr, "mapping") {
			goType := mapTypeFromASTType(typeStr)
			if strings.HasPrefix(goType, "[]") {
				return fmt.Sprintf("%s{%s}", goType, strings.Join(components, ", "))
			}
		}

		return strings.Join(components, ", ")
	}
	return "0"
}

func transformIndexAccess(node *parser.SolidityASTNode, warnings *WarningsCollector) string {
	if node == nil {
		return ""
	}

	indexExpr := "0"
	if node.IndexExpression != nil {
		indexExpr = transformExpression(node.IndexExpression, warnings)
	} else if len(node.Children) > 1 {
		indexExpr = transformExpression(&node.Children[1], warnings)
	}

	// Check if this is a state array access.
	if node.BaseExpression != nil && node.BaseExpression.NodeType == "Identifier" {
		arrayName := node.BaseExpression.Name
		if isStorageArray(arrayName) {
			varType := getStorageArrayElementType(node)
			if isStructType(varType) {
				return fmt.Sprintf("storage.Get(ctx, append([]byte(%q), convert.ToBytes(%s)...)).(%s)", arrayName+":", indexExpr, varType)
			}
			return fmt.Sprintf("getIntFromDB(append([]byte(%q), convert.ToBytes(%s)...))", arrayName+":", indexExpr)
		}
	}

	// Regular array indexing (params/locals or mapping value that is already an array).
	if node.BaseExpression != nil && shouldUseDirectArrayIndex(node.BaseExpression) {
		baseExpr := transformExpression(node.BaseExpression, warnings)
		return fmt.Sprintf("%s[%s]", baseExpr, indexExpr)
	}

	storageKey := buildStorageKey(node, warnings)
	valueType := getMappingValueType(node)
	if isStructType(valueType) {
		return fmt.Sprintf("storage.Get(ctx, %s).(%s)", storageKey, valueType)
	}

	// Check if this returns an array type
	if node.TypeDescriptions != nil {
		typeStr := node.TypeDescriptions.TypeString
		if strings.Contains(typeStr, "[]") && !strings.Contains(typeStr, "mapping") {
			arrayType := mapTypeFromASTType(typeStr)
			if strings.HasPrefix(arrayType, "[]") {
				defaultArray := zeroValueForType(arrayType)
				return fmt.Sprintf("func() %s { _v := storage.Get(ctx, %s); if _v == nil { return %s }; return _v.(%s) }()", arrayType, storageKey, defaultArray, arrayType)
			}
		}
	}

	switch valueType {
	case "bool":
		return fmt.Sprintf("func() bool { _v := storage.Get(ctx, %s); if _v == nil { return false }; return _v.(bool) }()", storageKey)
	case "string":
		return fmt.Sprintf("func() string { _v := storage.Get(ctx, %s); if _v == nil { return \"\" }; return _v.(string) }()", storageKey)
	case "[]byte":
		return fmt.Sprintf("func() []byte { _v := storage.Get(ctx, %s); if _v == nil { return nil }; return _v.([]byte) }()", storageKey)
	case "interop.Hash160", "interop.Hash256":
		return fmt.Sprintf("func() %s { _v := storage.Get(ctx, %s); if _v == nil { return nil }; return _v.(%s) }()", valueType, storageKey, valueType)
	}

	return fmt.Sprintf("getIntFromDB(%s)", storageKey)
}

func getStorageArrayElementType(node *parser.SolidityASTNode) string {
	if node == nil || node.TypeDescriptions == nil {
		return ""
	}
	typeStr := node.TypeDescriptions.TypeString
	// Handle "struct ContractName.StructName storage ref"
	if strings.HasPrefix(typeStr, "struct ") {
		parts := strings.Fields(typeStr)
		if len(parts) >= 2 {
			structName := parts[1]
			dotIdx := strings.Index(structName, ".")
			if dotIdx >= 0 {
				return structName[dotIdx+1:]
			}
			return structName
		}
	}
	// For primitive types, return "int" for now
	return ""
}

func getMappingValueType(node *parser.SolidityASTNode) string {
	if node == nil {
		return ""
	}
	if node.TypeDescriptions != nil && node.TypeDescriptions.TypeString != "" {
		if mapped := mapTypeFromASTType(node.TypeDescriptions.TypeString); mapped != "" {
			return mapped
		}
	}
	if node.BaseExpression != nil && node.BaseExpression.TypeDescriptions != nil {
		typeStr := node.BaseExpression.TypeDescriptions.TypeString
		if strings.HasPrefix(typeStr, "mapping(") {
			if valueType := extractMappingValueType(typeStr); valueType != "" {
				if mapped := mapTypeFromASTType(valueType); mapped != "" {
					return mapped
				}
			}
		}
	}
	if baseName := getIndexAccessBaseName(node); baseName != "" {
		if mappedType, ok := currentContractMappingValueTypes[baseName]; ok {
			return mappedType
		}
	}
	return ""
}

func getIndexAccessBaseName(node *parser.SolidityASTNode) string {
	current := node
	for current != nil {
		if current.BaseExpression == nil {
			if len(current.Children) > 0 {
				child := &current.Children[0]
				if child.NodeType == "Identifier" {
					return child.Name
				}
				if child.NodeType == "IndexAccess" {
					current = child
					continue
				}
			}
			return ""
		}
		if current.BaseExpression.NodeType == "Identifier" {
			return current.BaseExpression.Name
		}
		if current.BaseExpression.NodeType == "IndexAccess" {
			current = current.BaseExpression
			continue
		}
		return ""
	}
	return ""
}

func isMappingBackedIndexAccess(node *parser.SolidityASTNode) bool {
	baseName := getIndexAccessBaseName(node)
	if baseName == "" {
		return false
	}
	_, ok := currentContractMappingValueTypes[baseName]
	return ok
}

func mapTypeFromASTType(typeStr string) string {
	typeStr = normalizeTypeString(typeStr)
	if typeStr == "" {
		return ""
	}

	if strings.HasPrefix(typeStr, "mapping(") {
		if valueType := extractMappingValueType(typeStr); valueType != "" {
			return mapTypeFromASTType(valueType)
		}
	}

	if strings.HasSuffix(typeStr, "[]") {
		return "[]" + mapTypeFromASTType(strings.TrimSuffix(typeStr, "[]"))
	}
	if idx := strings.LastIndex(typeStr, "["); idx > 0 && strings.HasSuffix(typeStr, "]") {
		return "[]" + mapTypeFromASTType(typeStr[:idx])
	}

	if strings.HasPrefix(typeStr, "struct ") {
		parts := strings.Fields(typeStr)
		if len(parts) >= 2 {
			structName := parts[1]
			if dotIdx := strings.Index(structName, "."); dotIdx >= 0 {
				return structName[dotIdx+1:]
			}
			return structName
		}
	}

	if strings.HasPrefix(typeStr, "enum ") {
		parts := strings.Fields(typeStr)
		if len(parts) >= 2 {
			enumName := parts[1]
			if dotIdx := strings.Index(enumName, "."); dotIdx >= 0 {
				return enumName[dotIdx+1:]
			}
			return enumName
		}
	}

	return MapType(typeStr)
}

func extractMappingValueType(typeStr string) string {
	arrowIdx := strings.Index(typeStr, "=>")
	if arrowIdx < 0 {
		return ""
	}

	remainder := strings.TrimSpace(typeStr[arrowIdx+2:])
	depth := 0
	for i, ch := range remainder {
		switch ch {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return strings.TrimSpace(remainder[:i])
			}
			depth--
		}
	}

	return strings.TrimSpace(remainder)
}

func normalizeTypeString(typeStr string) string {
	parts := strings.Fields(strings.TrimSpace(typeStr))
	if len(parts) == 0 {
		return ""
	}

	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "storage", "memory", "calldata", "ref", "pointer", "slice":
			continue
		default:
			filtered = append(filtered, part)
		}
	}

	return strings.Join(filtered, " ")
}

func isStructType(typeName string) bool {
	if typeName == "" {
		return false
	}
	structTypes := getAllStructNames()
	for _, name := range structTypes {
		if typeName == name {
			return true
		}
	}
	return false
}

func isBuiltinType(typeName string) bool {
	builtins := []string{"int", "bool", "string", "bytes", "address", "uint", "uint256", "int256"}
	for _, b := range builtins {
		if typeName == b || strings.HasPrefix(typeName, b+" ") {
			return true
		}
	}
	return false
}

func getAllStructNames() []string {
	return currentContractStructs
}

func isStorageArray(name string) bool {
	for _, arr := range currentContractStorageArrays {
		if arr == name {
			return true
		}
	}
	return false
}

func shouldUseDirectArrayIndex(base *parser.SolidityASTNode) bool {
	if base == nil {
		return false
	}

	// State arrays and state mappings are storage-backed.
	if base.NodeType == "Identifier" {
		if isStorageArray(base.Name) {
			return false
		}
		if _, isMapping := currentContractMappingValueTypes[base.Name]; isMapping {
			return false
		}
	}

	typeStr := ""
	if base.TypeDescriptions != nil {
		typeStr = base.TypeDescriptions.TypeString
	}
	return strings.Contains(typeStr, "[]") && !strings.Contains(typeStr, "mapping")
}

// transformWhileStatement handles while loops
func transformWhileStatement(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	var sb strings.Builder

	var condition string
	var body *parser.SolidityASTNode

	// New format: condition and body fields
	if stmt.Condition != nil {
		condition = transformExpression(stmt.Condition, warnings)
		body = stmt.Body
	} else if len(stmt.Children) > 0 {
		condition = transformExpression(&stmt.Children[0], warnings)
		if len(stmt.Children) > 1 {
			body = &stmt.Children[1]
		}
	}

	sb.WriteString(fmt.Sprintf("\tfor %s {\n", condition))

	if body != nil {
		bodyCode := transformBlock(body, warnings)
		sb.WriteString(bodyCode)
	}

	sb.WriteString("\t}\n")

	return sb.String()
}

// transformForStatement handles for loops
func transformForStatement(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	var sb strings.Builder

	// ForStatement in solc has: initialization, condition, loopExpression, body
	// Try to get from direct fields first
	init := ""
	cond := ""
	increment := ""

	// Check for initialization (VariableDeclarationStatement)
	// solc uses both "init" and "initializationExpression" depending on version
	initNode := stmt.Init
	if initNode == nil {
		initNode = stmt.InitializationExpression
	}
	if initNode != nil {
		// Handle VariableDeclarationStatement specially for for-loop init
		if initNode.NodeType == "VariableDeclarationStatement" {
			init = transformForLoopInit(initNode, warnings)
		} else {
			init = transformStatement(initNode, warnings)
			// Remove leading/trailing tabs and newlines
			init = strings.Trim(init, "\t\n")
		}
	}

	// Check for condition
	if stmt.Condition != nil {
		cond = transformExpression(stmt.Condition, warnings)
	}

	// Check for loop expression (increment)
	if stmt.LoopExpression != nil {
		loopExpr := stmt.LoopExpression
		// Handle ExpressionStatement wrapper
		if loopExpr.NodeType == "ExpressionStatement" && loopExpr.Expression != nil {
			increment = transformExpression(loopExpr.Expression, warnings)
		} else {
			increment = transformExpression(loopExpr, warnings)
		}
		// Remove leading/trailing whitespace/newlines
		increment = strings.Trim(increment, "\t\n ")
	}

	// Build the for loop
	sb.WriteString(fmt.Sprintf("\tfor %s; %s; %s {\n", init, cond, increment))

	// Get body - check Body field first
	if stmt.Body != nil {
		body := transformBlock(stmt.Body, warnings)
		sb.WriteString(body)
	} else if len(stmt.Children) > 0 {
		// Fallback to children
		body := transformBlock(&stmt.Children[0], warnings)
		sb.WriteString(body)
	}

	sb.WriteString("\t}\n")

	return sb.String()
}

// transformForLoopInit handles for loop initialization (variable declaration)
func transformForLoopInit(stmt *parser.SolidityASTNode, warnings *WarningsCollector) string {
	// For loop init should be: i := 0 or var i int = 0
	if len(stmt.Declarations) > 0 {
		decl := stmt.Declarations[0]
		varName := decl.Name

		if stmt.InitialValue != nil {
			initVal := transformExpression(stmt.InitialValue, warnings)
			return fmt.Sprintf("%s := %s", varName, initVal)
		}
		// No initial value - use zero value
		varType := "int"
		if decl.TypeDescriptions != nil && decl.TypeDescriptions.TypeString != "" {
			varType = MapType(decl.TypeDescriptions.TypeString)
		}
		return fmt.Sprintf("var %s %s", varName, varType)
	}
	return ""
}
