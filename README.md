# sol2neo

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.24%2B-00ADD8?logo=go)](https://golang.org/)
[![Neo N3](https://img.shields.io/badge/Neo-N3-00D659?logo=neo)](https://neo.org/)

**A Solidity to Neo N3 transpiler** — Transform Ethereum smart contracts into Neo blockchain compatible code.

```
Solidity (.sol) ──► sol2neo ──► NeoGo (.go) ──► NeoVM bytecode (.nef)
```

## Why sol2neo?

- **Port existing contracts** — Migrate your Solidity codebase to Neo N3 without rewriting from scratch
- **Familiar syntax** — Continue using Solidity's well-known syntax while targeting Neo
- **Automated transformation** — Handles mappings, events, constructors, and Neo-specific patterns automatically

## Quick Start

```bash
# Install
git clone https://github.com/fabwa/sol2neo.git
cd sol2neo
go build -o bin/sol2neo ./cmd/sol2neo

# Transpile
./bin/sol2neo -i MyContract.sol -o MyContract.go
```

## Status

| Category | Status |
|----------|--------|
| Baseline corpus | ✅ 10/10 contracts pass |
| Complex examples | ✅ 9/9 contracts pass |
| External semantic | ✅ 5/5 contracts pass |
| Solidity semantic smoke | ✅ 4/4 contracts pass |
| Full suite | ✅ 28/28 contracts pass |
| Build | ✅ `go build ./...` passes |
| Tests | ✅ `go test ./...` passes |

Validated on **February 24, 2026** via `testcontracts/run_full_suite.sh`.

**Tested contracts:** `FlashLoan`, `Lottery`, `PiggyBank`, `SimpleAMM`, `SimpleDAO`, `SimpleStorage`, `Staking`, `StandaloneERC20`, `TimeLock`, `TodoList`, `Ballot`, `BlindAuction`, `Purchase`, `SimpleAuction`, and more.

## Installation

### Prerequisites

- **Go 1.24+**
- **Solidity compiler (`solc`)** — [Install guide](https://docs.soliditylang.org/en/latest/installing-solidity.html)
- **NeoGo** (for compiling output) — [Install guide](https://github.com/nspcc-dev/neo-go#installation)

### Build from Source

```bash
git clone https://github.com/fabwa/sol2neo.git
cd sol2neo
go build -o bin/sol2neo ./cmd/sol2neo
```

## Usage

### CLI Options

| Flag | Description |
|------|-------------|
| `-i` | Input Solidity file (required) |
| `-o` | Output Go file (default: `<input>.go`) |
| `-v` | Verbose output |
| `-w` | Show warnings (default: true) |

### Basic Transpilation

```bash
sol2neo -i contract.sol -o contract.go
```

### Complete Workflow

```bash
# 1. Transpile
sol2neo -i MyContract.sol -o MyContract/MyContract.go

# 2. Create go.mod
cat > MyContract/go.mod << 'EOF'
module mycontract

go 1.24

require github.com/nspcc-dev/neo-go/pkg/interop v0.0.0-20260121113504-979d1f4aada1
EOF

# 3. Create manifest config
cat > MyContract/MyContract.yml << 'EOF'
name: "MyContract"
sourceurl: ""
supportedstandards: []
events: []
permissions: []
EOF

# 4. Compile with NeoGo
cd MyContract
neo-go contract compile \
  -i MyContract.go \
  -o MyContract.nef \
  -m MyContract.manifest.json \
  -c MyContract.yml \
  --no-events \
  --no-permissions
```

## Features

### Supported Transformations

| Solidity | NeoGo | Status |
|----------|-------|--------|
| State variables | Package-level vars | ✅ |
| Mappings (nested) | `storage.Get/Put` | ✅ |
| Structs / Enums | Go structs / constants | ✅ |
| Events | `runtime.Notify()` | ✅ |
| Constructors | `_deploy()` | ✅ |
| `require()` / `revert()` | `panic()` | ✅ |
| `block.timestamp/number/chainid` | `runtime.GetTime()` / `ledger.CurrentIndex()` / `runtime.GetNetwork()` | ✅ |
| `msg.sender` | `runtime.GetCallingScriptHash()` | ✅ |
| `this` | `runtime.GetExecutingScriptHash()` | ✅ |
| Power operator (`**`) | `pow()` helper | ✅ |
| View/pure functions | Read-only storage context | ✅ |
| `delete` on mapping slots | `storage.Delete()` | ✅ |
| ERC721 receiver pattern | `contract.Call(...)` + selector bytes lowering | ✅ |

### Type Mappings

| Solidity | NeoGo |
|----------|-------|
| `address` | `interop.Hash160` |
| `uint256`, `int256`, `uint`, `int` | `int` |
| `bool` | `bool` |
| `string` | `string` |
| `bytes`, `bytes32` | `[]byte` |
| `Type[]` | `[]Type` |
| `mapping(K => V)` | Storage key-value ops |

## Example

**Solidity:**
```solidity
contract SimpleStorage {
    uint256 public value;
    
    function setValue(uint256 _value) public {
        value = _value;
    }
    
    function getValue() public view returns (uint256) {
        return value;
    }
}
```

**Generated NeoGo:**
```go
package simplestorage

var value int

func setValue(_value int) {
    value = _value
}

func getValue() int {
    return value
}
```

## Limitations

| Feature | Current status |
|---------|----------------|
| `delegatecall` / `staticcall` | Lowered to low-level call helpers with warnings. Does **not** preserve EVM delegate/static semantics. |
| `selfdestruct` | Lowered to `management.Destroy()` with warning (beneficiary argument ignored). |
| Inline assembly | Not supported. Emitted as unsupported statement with warning. |
| Function overloading | Supported via deterministic mangled Go names (for example `foo__uint256`, `foo__address`) while preserving call-site resolution. |
| Multiple inheritance | Not fully mapped. |
| `msg.sig` / `msg.data` | Approximated from `Runtime.GetScriptContainer().Script` with warnings (not EVM calldata-equivalent). |
| `gasleft()` | Supported via `runtime.GasLeft()`. |
| `ecrecover` | Lowered to `crypto.RecoverSecp256K1` + `contract.CreateStandardAccount` with warning (Neo address semantics, not exact EVM address semantics). |

## Project Structure

```
sol2neo/
├── cmd/sol2neo/main.go      # CLI entry point
├── parser/solidity_parser.go # Solidity AST parsing
├── transformer/
│   ├── transformer.go       # Main transformation logic
│   └── type_mapper.go       # Type mapping
├── compiler/compile.go      # (Future) Direct compilation
├── go.mod
└── README.md
```

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

### Roadmap

- [x] `block.chainid` support for EIP-712 (`runtime.GetNetwork()`)
- [x] `ecrecover` compatibility path (Neo-address semantics)
- [x] ERC721 receiver/delete lowering fixes
- [ ] Delegate/static call semantic parity (currently approximation with warnings)
- [ ] Comprehensive test coverage

## License

[MIT](LICENSE)

## Acknowledgments

- [NeoGo](https://github.com/nspcc-dev/neo-go) — Go compiler for NeoN3
- [Solidity](https://github.com/ethereum/solidity) — Solidity compiler
- [Neo Project](https://neo.org/) — Neo blockchain platform
