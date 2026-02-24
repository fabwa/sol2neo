package transformer

import (
	"fmt"
	"strings"
)

// MapType maps Solidity types to NeoGo types
func MapType(solidityType string) string {
	// Handle elementary types
	solidityType = strings.TrimSpace(solidityType)

	// Check for array first (before checking element type)
	// e.g., "address[]" should become "[]interop.Hash160", not "interop.Hash160"
	if strings.HasSuffix(solidityType, "[]") {
		elemType := solidityType[:len(solidityType)-2]
		return "[]" + MapType(elemType)
	}

	// Check for fixed-size array
	if idx := strings.Index(solidityType, "["); idx > 0 {
		elemType := solidityType[:idx]
		return "[]" + MapType(elemType) // Neo doesn't have fixed arrays
	}

	// Handle address types
	if strings.HasPrefix(solidityType, "address") {
		if strings.Contains(solidityType, "payable") {
			return "interop.Hash160" // Address payable in Solidity
		}
		return "interop.Hash160"
	}

	// Handle integer types
	switch solidityType {
	case "Any", "any":
		return "any"
	case "int8", "int16", "int32", "int64", "int", "uint8", "uint16", "uint32", "uint64", "uint", "uint256", "int256":
		return "int"

	// Handle byte types
	case "bytes", "byte":
		return "[]byte"
	case "bytes1", "bytes2", "bytes4", "bytes8", "bytes16", "bytes32":
		return "[]byte"

	// Handle fixed-point types (not supported in Neo, warn and use int)
	case "fixed", "ufixed":
		return "int" // Warning will be added elsewhere

	// Handle boolean
	case "bool":
		return "bool"

	// Handle string
	case "string":
		return "string"

	// Handle address (already handled above)

	// Handle contract types
	case "address payable":
		return "interop.Hash160"

	// Handle contract type references
	default:
		// Check for contract type
		if strings.HasPrefix(solidityType, "contract ") {
			return "interop.Hash160"
		}

		// Check for enum type
		if strings.HasPrefix(solidityType, "enum ") {
			parts := strings.Fields(solidityType)
			if len(parts) >= 2 {
				dotIdx := strings.Index(parts[1], ".")
				if dotIdx >= 0 {
					return parts[1][dotIdx+1:]
				}
				return parts[1]
			}
			return "int"
		}

		// Check for struct type
		if strings.HasPrefix(solidityType, "struct ") {
			// Extract struct name from type string like "struct ContractName.StructName storage ref"
			parts := strings.Fields(solidityType)
			if len(parts) >= 2 {
				structName := parts[1]
				// Remove contract prefix if present (ContractName.StructName -> StructName)
				dotIdx := strings.Index(structName, ".")
				if dotIdx >= 0 {
					return structName[dotIdx+1:]
				}
				return structName
			}
			return "[]interface{}"
		}

		// Check for mapping (handled elsewhere, but return proper type)
		if strings.Contains(solidityType, "mapping") {
			// Parse mapping types
			// mapping(uint256 => Round) -> map[int]interface{}
			// mapping(uint256 => mapping(...)) -> map[int]map[...]
			// mapping(address => uint256) -> map[string]int

			keyType := "string" // default to string for address keys
			valueType := "int"  // default value type

			// Determine key type
			if strings.Contains(solidityType, "uint256 =>") || strings.Contains(solidityType, "uint =>") {
				keyType = "int"
			}

			// Check for nested mapping in value
			if strings.Contains(solidityType, "=> mapping") {
				// Nested mapping - extract inner mapping type
				// This is complex - just return interface{} for now
				valueType = "interface{}"
			} else {
				// Check value type
				if strings.Contains(solidityType, "Round") || strings.Contains(solidityType, "Ticket") ||
					strings.Contains(solidityType, "StakerInfo") || strings.Contains(solidityType, "PoolInfo") {
					valueType = "interface{}"
				} else if strings.Contains(solidityType, "uint256[]") || strings.Contains(solidityType, "uint[]") {
					valueType = "[]int"
				}
			}

			return fmt.Sprintf("map[%s]%s", keyType, valueType)
		}

		// Check for type with location (storage, memory, calldata)
		parts := strings.Fields(solidityType)
		if len(parts) > 1 {
			filtered := make([]string, 0, len(parts))
			for _, part := range parts {
				switch part {
				case "storage", "memory", "calldata", "ref", "pointer", "slice":
					continue
				default:
					filtered = append(filtered, part)
				}
			}
			if len(filtered) > 0 {
				return MapType(strings.Join(filtered, " "))
			}
		}

		// For unknown types, use int for numeric compatibility
		return "int"
	}
}

// MapBuiltin maps Solidity built-in functions to Neo equivalents
func MapBuiltin(funcName string) string {
	switch funcName {
	// Cryptographic functions
	case "keccak256":
		return "crypto.Keccak256"
	case "sha256":
		return "crypto.Sha256"
	case "ripemd160":
		return "crypto.Ripemd160"
	case "ecrecover":
		return "crypto.Ecrecover"

	// Address functions
	case "address":
		return "interop.NewHash160" // Would need proper implementation

	// Type conversion functions
	case "uint", "uint8", "uint16", "uint32", "uint64", "uint256":
		return "int" // All integers are int in Neo
	case "int", "int8", "int16", "int32", "int64", "int256":
		return "int"
	case "bytes1", "bytes2", "bytes4", "bytes8", "bytes16", "bytes32", "bytes":
		return "[]byte"
	case "string":
		return "string"
	case "bool":
		return "bool"

	// ABI encoding functions
	case "abi.encode":
		return "interop.ABIEncode"
	case "abi.decode":
		return "interop.ABIDecode"

	// Gas and call functions
	case "gasleft":
		return "runtime.GasLeft"
	case "blockhash":
		return "ledger.GetBlockHash"

	// Other builtins
	case "require":
		return "requireImpl"
	case "assert":
		return "assertImpl"
	case "revert":
		return "revertImpl"

	default:
		return funcName
	}
}

// MapOperator maps Solidity operators to Go operators
func MapOperator(operator string) string {
	switch operator {
	case "+":
		return "+"
	case "-":
		return "-"
	case "*":
		return "*"
	case "/":
		return "/"
	case "%":
		return "%"
	case "**":
		return "pow"

	// Comparison operators
	case "==":
		return "=="
	case "!=":
		return "!="
	case "<":
		return "<"
	case ">":
		return ">"
	case "<=":
		return "<="
	case ">=":
		return ">="

	// Logical operators
	case "&&":
		return "&&"
	case "||":
		return "||"

	// Bitwise operators
	case "&":
		return "&"
	case "|":
		return "|"
	case "^":
		return "^"
	case "<<":
		return "<<"
	case ">>":
		return ">>"

	// Assignment operators
	case "=":
		return "="
	case "+=":
		return "+="
	case "-=":
		return "-="
	case "*=":
		return "*="
	case "/=":
		return "/="
	case "%=":
		return "%="
	case "&=":
		return "&="
	case "|=":
		return "|="
	case "^=":
		return "^="
	case "<<=":
		return "<<="
	case ">>=":
		return ">>="

	default:
		return operator
	}
}

// GetBuiltinImpl returns the implementation for Solidity built-in functions
func GetBuiltinImpl() string {
	return `
// Require implementation
func requireImpl(condition bool, msg string) {
	if !condition {
		panic(msg)
	}
}

// Assert implementation
func assertImpl(condition bool) {
	if !condition {
		panic("assertion failed")
	}
}

// Revert implementation
func revertImpl(msg string) {
	panic(msg)
}

// Power implementation (since Go doesn't have ** operator)
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
}

// MapEvent maps Solidity event parameters to Neo event parameters
func MapEvent(name string, params []string) string {
	// Event parameters are serialized as array for runtime.Notify
	var sb strings.Builder
	sb.WriteString("runtime.Notify(\"")
	sb.WriteString(name)
	sb.WriteString("\", ")
	for i, p := range params {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(p)
	}
	sb.WriteString(")")
	return sb.String()
}

// GetNEP17Name returns the NEP-17 standard name if applicable
func GetNEP17Name(contractName string) string {
	// Simple heuristic: if name contains "Token" or contract has transfer/balanceOf
	return "" // Would need more analysis
}

// GetEventHash returns the event hash for Neo
func GetEventHash(eventName string) string {
	// For Neo, events are identified by name, not hash
	return eventName
}
