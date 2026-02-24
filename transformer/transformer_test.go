package transformer

import (
	goparser "go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	solparser "sol2neo/parser"
)

func transformSolidityForTest(t *testing.T, source string) (string, []string) {
	t.Helper()

	if !solparser.CheckSolc() {
		t.Skip("solc is required for transformer tests")
	}

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "TestContract.sol")
	if err := os.WriteFile(srcPath, []byte(source), 0o644); err != nil {
		t.Fatalf("write solidity source: %v", err)
	}

	ast, err := solparser.ParseSolidityAST(srcPath)
	if err != nil {
		t.Fatalf("parse solidity AST: %v", err)
	}

	warnings := &WarningsCollector{ShowWarnings: true}
	result, err := Transform(ast, warnings)
	if err != nil {
		t.Fatalf("transform solidity: %v", err)
	}

	return result.GoSource, warnings.Warnings
}

func assertGoSourceParses(t *testing.T, goSource string) {
	t.Helper()
	_, err := goparser.ParseFile(token.NewFileSet(), "generated.go", goSource, goparser.AllErrors)
	if err != nil {
		t.Fatalf("generated Go source does not parse: %v\n%s", err, goSource)
	}
}

func hasWarningContaining(warnings []string, needle string) bool {
	for _, w := range warnings {
		if strings.Contains(w, needle) {
			return true
		}
	}
	return false
}

func TestOverloadManglingAndCallResolution(t *testing.T) {
	source := `pragma solidity ^0.8.20;
contract O {
    function f(uint256 x) public pure returns (uint256) { return x + 1; }
    function f(address a) public pure returns (address) { return a; }
    function g() public view returns (uint256, address) {
        return (f(uint256(3)), f(address(this)));
    }
}`

	goSource, warnings := transformSolidityForTest(t, source)
	assertGoSourceParses(t, goSource)

	if strings.Count(goSource, "func F__uint256(") != 1 {
		t.Fatalf("expected one uint overload, got source:\n%s", goSource)
	}
	if strings.Count(goSource, "func F__address(") != 1 {
		t.Fatalf("expected one address overload, got source:\n%s", goSource)
	}
	if strings.Contains(goSource, "func F(") {
		t.Fatalf("did not expect unmangled overloaded function name, got source:\n%s", goSource)
	}
	if !strings.Contains(goSource, "F__uint256(") || !strings.Contains(goSource, "F__address(") {
		t.Fatalf("expected overloaded calls to use mangled names, got source:\n%s", goSource)
	}
	if hasWarningContaining(warnings, "Unsupported") {
		t.Fatalf("unexpected unsupported warning for overload case: %#v", warnings)
	}
}

func TestNeoGoCapabilityLowerings(t *testing.T) {
	source := `pragma solidity ^0.8.20;
contract C {
    function callDel(address target, bytes memory data) public returns (bool, bytes memory) {
        return target.delegatecall(data);
    }

    function callStatic(address target, bytes memory data) public view returns (bool, bytes memory) {
        return target.staticcall(data);
    }

    function kill() public {
        selfdestruct(payable(msg.sender));
    }

    function meta() public view returns (bytes4, bytes memory) {
        return (msg.sig, msg.data);
    }

    function gasAndRec(bytes32 h, uint8 v, bytes32 r, bytes32 s) public view returns (uint256, address) {
        return (gasleft(), ecrecover(h, v, r, s));
    }

    function bh(uint256 n) public view returns (bytes32) {
        return blockhash(n);
    }
}`

	goSource, warnings := transformSolidityForTest(t, source)
	assertGoSourceParses(t, goSource)

	requiredSnippets := []string{
		"management.Destroy()",
		"func __ecrecover(",
		"func __msgData()",
		"func __msgSig()",
		"func __blockHash(",
		"runtime.GasLeft()",
		"__lowLevelCallWithData(target, 0, data)",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(goSource, snippet) {
			t.Fatalf("expected generated source to contain %q, got:\n%s", snippet, goSource)
		}
	}

	requiredWarnings := []string{
		"delegatecall lowered",
		"staticcall lowered",
		"selfdestruct lowered",
		"msg.sig approximated",
		"msg.data approximated",
		"blockhash lowered",
		"ecrecover lowered",
	}
	for _, needle := range requiredWarnings {
		if !hasWarningContaining(warnings, needle) {
			t.Fatalf("expected warning containing %q, got %#v", needle, warnings)
		}
	}
}
