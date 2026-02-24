# sol2neo

A Solidity to Neo N3 Go transpiler that transforms Solidity smart contracts into NeoGo-compatible Go code for compilation to NeoVM bytecode.

## Overview

`sol2neo` bridges the gap between Solidity and Neo N3, enabling developers to port existing Solidity contracts or write new ones using familiar Solidity syntax while targeting the Neo blockchain.

```
Solidity (.sol) → sol2neo → NeoGo (.go) → neo-go compiler → NeoVM bytecode (.nef)
```

## Current Status (February 22, 2026)

- `GO111MODULE=on go build ./...` passes for this repository.
- Baseline corpus is green (`10/10`) for transpile + Go build + NeoGo compile:
  - `FlashLoan`, `Lottery`, `PiggyBank`, `SimpleAMM`, `SimpleDAO`, `SimpleStorage`, `Staking`, `StandaloneERC20`, `TimeLock`, `TodoList`
- Additional complex examples are green (`9/9`) for transpile + Go build + NeoGo compile:
  - `Ballot`, `BlindAuction`, `Purchase`, `SemBaseConstructor`, `SemCallForward`, `SemERC20`, `SemFeatureComplete`, `SemTryCatchNested`, `SimpleAuction`
- Validation result snapshots:
  - `testcontracts/.validation_build12/results.tsv`
  - `testcontracts/complex_examples/build/results.neogo.patch.tsv`
  - `testcontracts/full_suite.results.tsv`
- Full-suite runner:
  - `testcontracts/run_full_suite.sh`
- Latest full-suite snapshot:
  - `baseline`: `10/10` full pass
  - `complex_examples`: `9/9` full pass
  - `external_semantic`: `1/5` full pass (`Owned`); `ReentrancyGuard` and `SignedMath` are method-less outputs and fail NeoGo manifest validation (`ABI: no methods`)
  - `solidity_semantictests`: `3/4` full pass
- Current smoke compile flow uses:
  - `neo-go contract compile ... --no-events --no-permissions`
  - For strict manifest checks, populate `events` and `permissions` in the contract `.yml`.

## Features

### Core Transformations

| Solidity Feature | NeoGo Equivalent | Status |
|-----------------|------------------|--------|
| State variables | Package-level vars | ✅ |
| Mappings | `storage.Get/Put` with keys | ✅ |
| Nested mappings | Compound storage keys | ✅ |
| Structs | Go structs | ✅ |
| Enums | `type X int` + constants | ✅ |
| Events | `runtime.Notify()` | ✅ |
| Functions | Go functions | ✅ |
| Constructors | `_deploy(data any, isUpdate bool)` | ✅ |
| Constructor args bootstrap | Decoded from `_deploy(data any, isUpdate bool)` | ✅ |
| `require()` | `if !cond { panic() }` | ✅ |
| `revert()` | `panic()` | ✅ |
| `block.timestamp` | `runtime.GetTime()` | ✅ |
| `block.number` | `ledger.CurrentIndex()` | ✅ |
| `msg.sender` | `runtime.GetCallingScriptHash()` | ✅ |
| `Syscalls.getCallingScriptHash()` | `runtime.GetCallingScriptHash()` | ✅ |
| `Syscalls.contractCall(...)` | `contract.Call(..., contract.All, args...)` via helper | ✅ |
| `this` | `runtime.GetExecutingScriptHash()` | ✅ |
| `address.code.length` | `len(__addressCode(addr))` using `management.GetContract` | ✅ (existence-based) |
| Power operator (`**`) | `pow()` helper | ✅ |
| Address comparisons | `util.Equals()` | ✅ |
| `address(0)` | `nil` | ✅ |
| View/pure storage reads | `storage.GetReadOnlyContext()` + `getIntFromCtx()` | ✅ |
| Owner verification | `runtime.CheckWitness()` | ✅ |
| Contract hash comparisons | `hash.Equals()` | ✅ |
| Struct serialization | `std.Serialize/Deserialize` | ✅ |
| Devpack `Any` type | Go `any` | ✅ |
| Parse fallback mode | `solc --stop-after parsing` when semantic mode fails | ✅ |

### NeoGo-Specific Patterns

| Pattern | Usage |
|---------|-------|
| `init()` | Initialize storage context |
| `_deploy()` | Constructor with update detection |
| `Verify()` | Contract witness verification |
| `checkOwner()` | Helper for owner-only functions |
| `getIntFromCtx()` | Context-aware int reads (`ctx` / read-only ctx) |
| `getIntFromDB()` | Type-safe storage read with nil check |
| `__sysContractCall()` | Devpack-style cross-contract call bridge |

### Type Mappings

| Solidity Type | NeoGo Type |
|--------------|------------|
| `address` | `interop.Hash160` |
| `uint256`, `int256`, `uint`, `int` | `int` |
| `bool` | `bool` |
| `string` | `string` |
| `bytes`, `bytes32` | `[]byte` |
| `Any` | `any` |
| Arrays (`Type[]`) | `[]Type` |
| Mappings | Storage operations |

## Installation

### Prerequisites

- Go 1.24+
- Solidity compiler (`solc`) - [Installation guide](https://docs.soliditylang.org/en/latest/installing-solidity.html)
- NeoGo (for compiling output) - [Installation guide](https://github.com/nspcc-dev/neo-go#installation)

### Build

```bash
git clone https://github.com/your-org/sol2neo.git
cd sol2neo
go build -o bin/sol2neo ./cmd/sol2neo
```

## Usage

### Basic Transpilation

```bash
sol2neo -i contract.sol -o contract.go
```

### With Verbose Output

```bash
sol2neo -i contract.sol -o contract.go -v
```

### Options

| Flag | Description |
|------|-------------|
| `-i` | Input Solidity file (required) |
| `-o` | Output Go file (default: `<input>.go`) |
| `-v` | Verbose output |
| `-w` | Show warnings (default: true) |

### Complete Workflow

```bash
# 1. Transpile Solidity to NeoGo
sol2neo -i MyContract.sol -o MyContract/MyContract.go

# 2. Create go.mod for the output
cat > MyContract/go.mod << 'EOF'
module mycontract

go 1.24

require github.com/nspcc-dev/neo-go/pkg/interop v0.0.0-20260121113504-979d1f4aada1
EOF

# 3. Compile with NeoGo
cd MyContract
cat > MyContract.yml << 'EOF'
name: "MyContract"
sourceurl: ""
supportedstandards: []
events: []
permissions: []
EOF
neo-go contract compile \
  -i MyContract.go \
  -o MyContract.nef \
  -m MyContract.manifest.json \
  -c MyContract.yml \
  --no-events \
  --no-permissions
```

### Full Validation

```bash
# from workspace root
cd testcontracts
./run_full_suite.sh
```

Artifacts and summaries are written to:

- `testcontracts/.validation_build12/results.tsv` (baseline corpus)
- `testcontracts/complex_examples/build/results.neogo.patch.tsv` (complex examples)
- `testcontracts/external_semantic/build/results.tsv` (external semantic set)
- `testcontracts/external_semantic/solidity_semantictests/build/results.tsv` (semantic smoke tests)
- `testcontracts/full_suite.results.tsv` (combined suite matrix)

## Examples

### Simple Storage

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

import "github.com/nspcc-dev/neo-go/pkg/interop/runtime"

var value int

func setValue(_value int) {
    value = _value
}

func getValue() int {
    return value
}
```

### ERC20 Token with Mappings

**Solidity:**
```solidity
contract Token {
    mapping(address => uint256) public balanceOf;
    
    function transfer(address to, uint256 amount) public returns (bool) {
        require(balanceOf[msg.sender] >= amount, "Insufficient balance");
        balanceOf[msg.sender] -= amount;
        balanceOf[to] += amount;
        return true;
    }
}
```

**Generated NeoGo:**
```go
package token

import (
    "github.com/nspcc-dev/neo-go/pkg/interop"
    "github.com/nspcc-dev/neo-go/pkg/interop/runtime"
    "github.com/nspcc-dev/neo-go/pkg/interop/storage"
    "github.com/nspcc-dev/neo-go/pkg/interop/convert"
)

// Storage context
var ctx storage.Context

func init() {
    ctx = storage.GetContext()
}

func getIntFromDB(key []byte) int {
    val := storage.Get(ctx, key)
    if val == nil { return 0 }
    return val.(int)
}

func checkOwner() bool {
    return runtime.CheckWitness(runtime.GetCallingScriptHash())
}

func transfer(to interop.Hash160, amount int) bool {
    sender := runtime.GetCallingScriptHash()
    balance := getIntFromDB(append([]byte("balanceOf:"), convert.ToBytes(sender)...))
    if !(balance >= amount) { panic("Insufficient balance") }
    storage.Put(ctx, append([]byte("balanceOf:"), convert.ToBytes(sender)...), balance - amount)
    destBalance := getIntFromDB(append([]byte("balanceOf:"), convert.ToBytes(to)...))
    storage.Put(ctx, append([]byte("balanceOf:"), convert.ToBytes(to)...), destBalance + amount)
    runtime.Notify("Transfer", sender, to, amount)
    return true
}
```

## Storage Architecture

Solidity mappings are fundamentally different from Go maps. In NeoN3, persistent storage uses key-value operations:

### Single Mapping
```solidity
mapping(address => uint256) balanceOf;
balanceOf[user] = 100;
```
```go
storage.Put(ctx, append([]byte("balanceOf:"), user...), 100)
```

### Nested Mapping
```solidity
mapping(address => mapping(address => uint256)) allowance;
allowance[owner][spender] = 100;
```
```go
storage.Put(ctx, append([]byte("allowance:"), owner..., spender...), 100)
```

## Limitations

### Not Supported

| Feature | Reason |
|---------|--------|
| `delegatecall` | NeoVM has no delegate-call equivalent |
| Exact low-level EVM `.call` semantics | Lowered for compile/runtime continuity, not full EVM revert/returndata fidelity |
| Exact EVM native-asset semantics | Neo uses GAS token model (`gas.Transfer` / `gas.BalanceOf`) |
| `selfdestruct` | No direct equivalent in Neo |
| `blockhash()` | Different block structure in Neo |
| `msg.sig` | Neo dispatch is method-name based, not selector based |
| `msg.data` | Raw calldata model does not map directly to Neo typed invocation model |
| `gasleft()` | Different gas model in Neo |
| Inline assembly | No NeoVM assembly support |
| Function overloading | Go doesn't support overloading |
| Multiple inheritance | Complex to map to Go |

### Current Missing Functions and Gaps (from full-suite results)

| Solidity construct | Current behavior | Impact |
|--------------------|------------------|--------|
| `msg.data` | Can be emitted as `runtime.GetCallingScriptHash().data` | Go compile failure (`BasicSmoke`) |
| `block.chainid` | Can be emitted as `ledger.CurrentIndex().chainid` | Go compile failure (`ERC20` EIP-712 flow) |
| `ecrecover(...)` | Lowered to `crypto.Ecrecover(...)` (not available in current NeoGo interop target) | Go compile failure (`ERC20 permit`) |
| EIP-712 `DOMAIN_SEPARATOR`-style ternary | Can emit mixed `int`/`[]byte` branch types | Go type mismatch (`ERC20`) |
| Assignment in expressions (`require((owner = _ownerOf[id]) != ...)`) | May emit invalid Go syntax | Go compile failure (`ERC721`) |
| Complex mapping `delete` + receiver callback patterns | Lowering still incomplete in some ERC721 paths | Compile or semantic-risk areas |
| Method-less abstract/library outputs | Transpile + Go build pass, but NeoGo manifest fails (`ABI: no methods`) | NeoGo compile failure (`ReentrancyGuard`, `SignedMath`) |

### Patterns Requiring Manual Work

1. **Low-level EVM `.call` behavior** - transformed output compiles, but semantic parity may require manual hardening.
2. **Account authorization semantics** - Solidity `msg.sender` checks may need explicit `runtime.CheckWitness(...)` review for account-centric security.
3. **Payable/value-transfer behavior** - review GAS transfer and callback flow for business-critical value logic.
4. **Strict manifest conformance** - events/permissions should be explicitly authored in `.yml`.
5. **Method-less units** - abstract/library-style sources may need wrapper/exported methods for NeoGo manifest validity.

## Project Structure

```
sol2neo/
├── cmd/
│   └── sol2neo/
│       └── main.go          # CLI entry point
├── parser/
│   └── solidity_parser.go   # Solidity AST parsing via solc
├── transformer/
│   ├── transformer.go       # Main transformation logic
│   └── type_mapper.go       # Solidity → NeoGo type mapping
├── compiler/
│   └── compile.go           # (Future) NeoVM bytecode compilation
├── go.mod
├── go.sum
└── README.md
```

## Debug Output

Each transpilation generates a `.debug.json` file with metadata:

```json
{
  "contract_name": "MyContract",
  "package_name": "mycontract",
  "has_storage": true,
  "has_events": true,
  "functions": [...],
  "events": [...],
  "variables": [...]
}
```

## Contributing

Contributions are welcome! Please follow these steps:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

### Development Areas

- [ ] `msg.data`/`msg.sig` policy (safe lowering or explicit compile-time rejection)
- [ ] `block.chainid` lowering for EIP-712 style contracts
- [ ] `ecrecover` compatibility path for NeoGo target interop
- [ ] Named-return assignment expression lowering (`(x = y)` inside conditionals)
- [ ] ERC721-heavy pattern support (`delete`, receiver callback checks, complex guards)
- [ ] Method-less contract policy (skip/classify/wrapper generation for NeoGo manifest)
- [ ] More comprehensive semantic tests and NeoGo integration coverage

## License

MIT License - see [LICENSE](LICENSE) for details.

## Acknowledgments

- [NeoGo](https://github.com/nspcc-dev/neo-go) - Go compiler for NeoN3 smart contracts
- [Solidity](https://github.com/ethereum/solidity) - Solidity compiler and language
- [Neo Project](https://neo.org/) - Neo blockchain platform

## Related Projects

- [NeoGo Examples](https://github.com/nspcc-dev/neo-go/tree/master/examples) - Official NeoGo contract examples
- [Neo Documentation](https://developers.neo.org/) - Neo developer documentation
- [NEP Standards](https://github.com/neo-project/proposals) - Neo Enhancement Proposals
