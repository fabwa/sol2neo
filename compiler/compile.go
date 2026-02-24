package compiler

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	defaultModuleName    = "sol2neo-contract"
	defaultInteropModule = "github.com/nspcc-dev/neo-go/pkg/interop"
	defaultInteropVer    = "v0.0.0-20260121113504-979d1f4aada1"
	defaultCompileTries  = 12
	transientCompileErr  = "invalid label target: -1"
)

type compilePaths struct {
	workDir      string
	inputPath    string
	contractName string
	nefPath      string
	manifestPath string
	configPath   string
}

// CompileGo compiles a Go source file to a .nef file via neo-go and returns
// the emitted NEF bytes.
func CompileGo(filename, packageName string) ([]byte, error) {
	paths, err := prepareCompilePaths(filename)
	if err != nil {
		return nil, err
	}

	if err := ensureGoMod(paths.workDir, packageName); err != nil {
		return nil, fmt.Errorf("failed to set up go.mod: %w", err)
	}
	if err := runCommand(paths.workDir, "go", "mod", "tidy"); err != nil {
		return nil, fmt.Errorf("failed to resolve contract module dependencies: %w", err)
	}

	neoGoPath, err := findNeoGo()
	if err != nil {
		return nil, fmt.Errorf("neo-go CLI not found: %w", err)
	}

	args := []string{
		"contract", "compile",
		"-i", paths.inputPath,
		"-o", paths.nefPath,
		"--no-events",
		"--no-permissions",
	}
	if err := runNeoGoCompile(paths.workDir, neoGoPath, args...); err != nil {
		return nil, err
	}

	nefBytes, err := os.ReadFile(paths.nefPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read compiled NEF file %q: %w", paths.nefPath, err)
	}
	return nefBytes, nil
}

// CompileWithManifest compiles a Go source file to NEF with manifest
func CompileWithManifest(filename, packageName string, config *ContractConfig) ([]byte, error, error) {
	paths, err := prepareCompilePaths(filename)
	if err != nil {
		return nil, nil, err
	}

	if err := ensureGoMod(paths.workDir, packageName); err != nil {
		return nil, nil, fmt.Errorf("failed to set up go.mod: %w", err)
	}
	if err := runCommand(paths.workDir, "go", "mod", "tidy"); err != nil {
		return nil, nil, fmt.Errorf("failed to resolve contract module dependencies: %w", err)
	}

	if err := ensureManifestConfig(paths.configPath, paths.contractName, config); err != nil {
		return nil, nil, fmt.Errorf("failed to prepare manifest config: %w", err)
	}

	neoGoPath, err := findNeoGo()
	if err != nil {
		return nil, nil, fmt.Errorf("neo-go CLI not found: %w", err)
	}

	manifestPath := paths.manifestPath
	if config != nil && strings.TrimSpace(config.ManifestFile) != "" {
		manifestPath = config.ManifestFile
		if !filepath.IsAbs(manifestPath) {
			manifestPath = filepath.Join(paths.workDir, manifestPath)
		}
	}

	args := []string{
		"contract", "compile",
		"-i", paths.inputPath,
		"-o", paths.nefPath,
		"-m", manifestPath,
		"-c", paths.configPath,
	}
	if shouldDisableEventsCheck(config) {
		args = append(args, "--no-events")
	}
	if shouldDisableStandardsCheck(config) {
		args = append(args, "--no-standards")
	}
	if shouldDisablePermissionsCheck(config) {
		args = append(args, "--no-permissions")
	}

	if err := runNeoGoCompile(paths.workDir, neoGoPath, args...); err != nil {
		return nil, nil, err
	}

	nefBytes, nefErr := os.ReadFile(paths.nefPath)
	if nefErr != nil {
		return nil, nil, fmt.Errorf("failed to read compiled NEF file %q: %w", paths.nefPath, nefErr)
	}

	_, manifestErr := os.Stat(manifestPath)
	if manifestErr != nil {
		return nefBytes, fmt.Errorf("manifest file not generated at %q: %w", manifestPath, manifestErr), nil
	}
	return nefBytes, nil, nil
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
func ensureGoMod(dir, packageName string) error {
	goModPath := filepath.Join(dir, "go.mod")

	// Check if go.mod already exists
	if _, err := os.Stat(goModPath); err == nil {
		return nil // Already exists
	}

	moduleName := normalizeModuleName(packageName)
	content := fmt.Sprintf(`module %s

go 1.24.0

require %s %s
`, moduleName, defaultInteropModule, defaultInteropVer)

	if err := os.WriteFile(goModPath, []byte(content), 0644); err != nil {
		return err
	}

	return nil
}

// findNeoGo looks for neo-go CLI in PATH
func findNeoGo() (string, error) {
	if path, err := exec.LookPath("neo-go"); err == nil {
		return path, nil
	}

	paths := []string{"/usr/local/bin/neo-go", "/usr/bin/neo-go"}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("neo-go not found in PATH")
}

func prepareCompilePaths(filename string) (*compilePaths, error) {
	if strings.TrimSpace(filename) == "" {
		return nil, fmt.Errorf("filename is required")
	}

	absFile, err := filepath.Abs(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve filename %q: %w", filename, err)
	}

	info, err := os.Stat(absFile)
	if err != nil {
		return nil, fmt.Errorf("input file not found %q: %w", filename, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("input path %q is a directory; expected a Go source file", filename)
	}
	if filepath.Ext(absFile) != ".go" {
		return nil, fmt.Errorf("input file %q must have .go extension", filename)
	}

	workDir := filepath.Dir(absFile)
	contractName := strings.TrimSuffix(filepath.Base(absFile), filepath.Ext(absFile))
	return &compilePaths{
		workDir:      workDir,
		inputPath:    absFile,
		contractName: contractName,
		nefPath:      filepath.Join(workDir, contractName+".nef"),
		manifestPath: filepath.Join(workDir, contractName+".manifest.json"),
		configPath:   filepath.Join(workDir, contractName+".yml"),
	}, nil
}

func runCommand(dir, binary string, args ...string) error {
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("%s %s failed: %w", binary, strings.Join(args, " "), err)
		}
		return fmt.Errorf("%s %s failed: %w\n%s", binary, strings.Join(args, " "), err, msg)
	}
	return nil
}

func runNeoGoCompile(dir, binary string, args ...string) error {
	var lastErr error
	var lastMsg string

	for attempt := 1; attempt <= defaultCompileTries; attempt++ {
		cmd := exec.Command(binary, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}

		lastErr = err
		lastMsg = strings.TrimSpace(string(out))
		isTransient := strings.Contains(lastMsg, transientCompileErr)
		if !isTransient || attempt == defaultCompileTries {
			if lastMsg == "" {
				return fmt.Errorf("%s %s failed after %d attempt(s): %w", binary, strings.Join(args, " "), attempt, err)
			}
			return fmt.Errorf("%s %s failed after %d attempt(s): %w\n%s", binary, strings.Join(args, " "), attempt, err, lastMsg)
		}
	}

	if lastMsg == "" {
		return fmt.Errorf("%s %s failed: %w", binary, strings.Join(args, " "), lastErr)
	}
	return fmt.Errorf("%s %s failed: %w\n%s", binary, strings.Join(args, " "), lastErr, lastMsg)
}

func normalizeModuleName(packageName string) string {
	trimmed := strings.ToLower(strings.TrimSpace(packageName))
	if trimmed == "" {
		return defaultModuleName
	}

	var b strings.Builder
	for _, r := range trimmed {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '/' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	module := strings.Trim(b.String(), "-./_")
	if module == "" {
		return defaultModuleName
	}
	return module
}

func shouldDisableEventsCheck(config *ContractConfig) bool {
	if config == nil {
		return true
	}
	return config.NoEventsCheck
}

func shouldDisableStandardsCheck(config *ContractConfig) bool {
	if config == nil {
		return false
	}
	return config.NoStandardCheck
}

func shouldDisablePermissionsCheck(config *ContractConfig) bool {
	if config == nil {
		return true
	}
	return len(config.Permissions) == 0
}

func ensureManifestConfig(configPath, contractName string, cfg *ContractConfig) error {
	if cfg == nil {
		if _, err := os.Stat(configPath); err == nil {
			return nil
		}
	}

	content := buildManifestYAML(contractName, cfg)
	return os.WriteFile(configPath, []byte(content), 0644)
}

func buildManifestYAML(contractName string, cfg *ContractConfig) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("name: %q\n", contractName))
	b.WriteString("sourceurl: \"\"\n")
	b.WriteString("supportedstandards: []\n")

	if cfg == nil || len(cfg.Events) == 0 {
		b.WriteString("events: []\n")
	} else {
		b.WriteString("events:\n")
		for _, ev := range cfg.Events {
			b.WriteString(fmt.Sprintf("  - name: %q\n", ev.Name))
			if len(ev.Parameters) == 0 {
				b.WriteString("    parameters: []\n")
				continue
			}
			b.WriteString("    parameters:\n")
			for _, p := range ev.Parameters {
				b.WriteString(fmt.Sprintf("      - name: %q\n", p.Name))
				b.WriteString(fmt.Sprintf("        type: %q\n", p.Type))
			}
		}
	}

	if cfg == nil || len(cfg.Permissions) == 0 {
		b.WriteString("permissions: []\n")
	} else {
		b.WriteString("permissions:\n")
		for _, p := range cfg.Permissions {
			b.WriteString(fmt.Sprintf("  - contract: %q\n", p.Contract))
			if len(p.Methods) == 0 {
				b.WriteString("    methods: []\n")
				continue
			}
			b.WriteString("    methods:\n")
			for _, m := range p.Methods {
				b.WriteString(fmt.Sprintf("      - %q\n", m))
			}
		}
	}

	return b.String()
}
