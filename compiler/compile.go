package compiler

import (
	"fmt"
	"os"
	"path/filepath"
)

// CompileGo compiles a Go source file to NEF using NeoGo compiler
// Note: This is a stub implementation. The actual NeoGo compilation
// requires the neo-go CLI to be installed and properly configured.
// The primary functionality of sol2neo is the transpilation from
// Solidity to Go, which is handled by the transformer package.
func CompileGo(filename, packageName string) ([]byte, error) {
	// Ensure go.mod exists with proper dependencies
	if err := ensureGoMod(filepath.Dir(filename)); err != nil {
		return nil, fmt.Errorf("failed to setup go.mod: %w", err)
	}

	// Check if neo-go is available
	_, err := findNeoGo()
	if err != nil {
		return nil, fmt.Errorf("neo-go CLI not found: %w", err)
	}

	// TODO: Implement actual NeoGo compilation via CLI
	// For now, return a placeholder that indicates the Go source
	// was generated but needs manual compilation
	return []byte(fmt.Sprintf("# Go source generated at: %s\n# Package: %s\n# Run 'neo-go contract compile %s --out %s.nef' to compile to NEF", 
		filename, packageName, filename, filename[:len(filename)-3])), nil
}

// CompileWithManifest compiles a Go source file to NEF with manifest
func CompileWithManifest(filename, packageName string, config *ContractConfig) ([]byte, error, error) {
	// This is a stub - same as CompileGo for now
	nef, err := CompileGo(filename, packageName)
	return nef, nil, err
}

// ContractConfig holds configuration for contract compilation
type ContractConfig struct {
	ManifestFile     string
	NoEventsCheck    bool
	NoStandardCheck  bool
	GenerateManifest bool
	Permissions      []PermissionConfig
	Events           []EventConfig
}

// PermissionConfig holds permission configuration
type PermissionConfig struct {
	Contract string
	Methods  []string
}

// EventConfig holds event configuration
type EventConfig struct {
	Name       string
	Parameters []ParameterConfig
}

// ParameterConfig holds parameter configuration
type ParameterConfig struct {
	Name string
	Type string
}

// ensureGoMod ensures go.mod exists with required dependencies
func ensureGoMod(dir string) error {
	goModPath := filepath.Join(dir, "go.mod")

	// Check if go.mod already exists
	if _, err := os.Stat(goModPath); err == nil {
		return nil // Already exists
	}

	// Create minimal go.mod
	content := `module sol2neo-contract

go 1.21

require (
	github.com/nspcc-dev/neo-go v0.106.1
)
`

	if err := os.WriteFile(goModPath, []byte(content), 0644); err != nil {
		return err
	}

	return nil
}

// findNeoGo looks for neo-go CLI in PATH
func findNeoGo() (string, error) {
	// Check common locations
	paths := []string{
		"neo-go",
		"/usr/local/bin/neo-go",
		"/usr/bin/neo-go",
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	// Use go to find it
	return "", fmt.Errorf("neo-go not found in PATH")
}
