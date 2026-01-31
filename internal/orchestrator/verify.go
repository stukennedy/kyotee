package orchestrator

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GoalBackwardResult holds the result of goal-backward verification
type GoalBackwardResult struct {
	AllPassed bool
	Checks    []GBCheck
	Summary   string
}

// GBCheck is a single goal-backward check result
type GBCheck struct {
	Category string // "artifact_existence", "stub_detection", "wiring", "key_links"
	File     string
	Passed   bool
	Detail   string
}

// Stub detection patterns
var stubPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(TODO|FIXME|PLACEHOLDER|HACK)\b`),
	regexp.MustCompile(`(?i)(not\s+implemented|coming\s+soon|lorem\s+ipsum)`),
	regexp.MustCompile(`(?i)return\s+(null|nil|None)\s*;?\s*$`),
	regexp.MustCompile(`(?i)return\s+(\{\}|\[\])\s*;?\s*$`),
	regexp.MustCompile(`(?i)^\s*(pass)\s*$`), // Python pass-only body
	regexp.MustCompile(`(?i)panic\(\s*"(not implemented|todo|unimplemented)"`),
	regexp.MustCompile(`(?i)throw\s+new\s+Error\(\s*["'](not implemented|todo)`),
}

// Console-only handler pattern: function body is just console.log/fmt.Println
var consoleOnlyRe = regexp.MustCompile(`(?i)^\s*(console\.(log|warn|error)|fmt\.(Print|Println|Printf)|print)\s*\(`)

// runGoalBackwardVerification checks that what was planned/implemented actually exists and is substantive
func (e *Engine) runGoalBackwardVerification() (*GoalBackwardResult, error) {
	result := &GoalBackwardResult{AllPassed: true}

	// Collect planned files from plan phase
	plannedFiles := e.collectPlannedFiles()
	// Collect implemented files from implement phase
	implementedFiles := e.collectImplementedFiles()

	// Merge into a unique set of files to check
	allFiles := make(map[string]bool)
	for _, f := range plannedFiles {
		allFiles[f] = true
	}
	for _, f := range implementedFiles {
		allFiles[f] = true
	}

	if len(allFiles) == 0 {
		result.Summary = "No files to verify (no plan/implement output found)"
		return result, nil
	}

	// a) Artifact existence check
	var existingFiles []string
	for f := range allFiles {
		fullPath := filepath.Join(e.RepoRoot, f)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			result.AllPassed = false
			result.Checks = append(result.Checks, GBCheck{
				Category: "artifact_existence",
				File:     f,
				Passed:   false,
				Detail:   "file does not exist on disk",
			})
		} else {
			existingFiles = append(existingFiles, f)
			result.Checks = append(result.Checks, GBCheck{
				Category: "artifact_existence",
				File:     f,
				Passed:   true,
				Detail:   "exists",
			})
		}
	}

	// b) Stub detection on existing files
	for _, f := range existingFiles {
		fullPath := filepath.Join(e.RepoRoot, f)
		stubs, err := detectStubs(fullPath)
		if err != nil {
			continue // skip unreadable files
		}
		if len(stubs) > 0 {
			result.AllPassed = false
			result.Checks = append(result.Checks, GBCheck{
				Category: "stub_detection",
				File:     f,
				Passed:   false,
				Detail:   fmt.Sprintf("found %d stub(s): %s", len(stubs), strings.Join(stubs, "; ")),
			})
		} else {
			result.Checks = append(result.Checks, GBCheck{
				Category: "stub_detection",
				File:     f,
				Passed:   true,
				Detail:   "no stubs detected",
			})
		}

		// Check for empty/trivial files
		if isEmpty, detail := checkEmptyFile(fullPath); isEmpty {
			result.AllPassed = false
			result.Checks = append(result.Checks, GBCheck{
				Category: "stub_detection",
				File:     f,
				Passed:   false,
				Detail:   detail,
			})
		}
	}

	// c) Wiring check — verify exports are imported somewhere
	wiringIssues := e.checkWiring(existingFiles)
	for _, issue := range wiringIssues {
		result.AllPassed = false
		result.Checks = append(result.Checks, issue)
	}

	// Build summary
	passed := 0
	failed := 0
	for _, c := range result.Checks {
		if c.Passed {
			passed++
		} else {
			failed++
		}
	}
	result.Summary = fmt.Sprintf("Goal-backward verification: %d passed, %d failed across %d files", passed, failed, len(allFiles))

	return result, nil
}

// collectPlannedFiles extracts expected files from the plan phase output
func (e *Engine) collectPlannedFiles() []string {
	planPhase := e.findPhaseByID("plan")
	if planPhase == nil || planPhase.ControlJSON == nil {
		return nil
	}

	steps, err := e.extractPlanSteps(planPhase.ControlJSON)
	if err != nil {
		return nil
	}

	var files []string
	for _, s := range steps {
		files = append(files, s.ExpectedFiles...)
	}
	return files
}

// collectImplementedFiles extracts file paths from the implement phase output
func (e *Engine) collectImplementedFiles() []string {
	implPhase := e.findPhaseByID("implement")
	if implPhase == nil || implPhase.ControlJSON == nil {
		return nil
	}

	filesRaw, ok := implPhase.ControlJSON["files"].([]any)
	if !ok {
		return nil
	}

	var files []string
	for _, f := range filesRaw {
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		path, _ := fm["path"].(string)
		action, _ := fm["action"].(string)
		if path != "" && action != "delete" {
			files = append(files, path)
		}
	}
	return files
}

// detectStubs scans a file for stub patterns and returns descriptions of what was found
func detectStubs(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var stubs []string
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		for _, pat := range stubPatterns {
			if pat.MatchString(line) {
				stub := fmt.Sprintf("L%d: %s", lineNum, strings.TrimSpace(line))
				if len(stub) > 120 {
					stub = stub[:120] + "..."
				}
				stubs = append(stubs, stub)
				break // one match per line is enough
			}
		}
	}
	return stubs, scanner.Err()
}

// checkEmptyFile checks if a file is empty or trivially small
func checkEmptyFile(path string) (bool, string) {
	info, err := os.Stat(path)
	if err != nil {
		return false, ""
	}

	if info.Size() == 0 {
		return true, "file is empty (0 bytes)"
	}

	// Read content and check for trivial files (only comments/imports, no real code)
	content, err := os.ReadFile(path)
	if err != nil {
		return false, ""
	}

	// Strip comments and whitespace to check for substance
	lines := strings.Split(string(content), "\n")
	substantiveLines := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip common comment-only lines
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") ||
			strings.HasPrefix(trimmed, "'''") || strings.HasPrefix(trimmed, `"""`) {
			continue
		}
		substantiveLines++
	}

	if substantiveLines <= 1 && info.Size() > 0 {
		return true, fmt.Sprintf("file has only %d substantive line(s) — likely a stub", substantiveLines)
	}

	return false, ""
}

// checkWiring verifies that created files are actually connected to the project
func (e *Engine) checkWiring(files []string) []GBCheck {
	var issues []GBCheck

	// Build a map of all exports from new files
	type exportInfo struct {
		file   string
		symbol string
	}
	var exports []exportInfo

	for _, f := range files {
		fullPath := filepath.Join(e.RepoRoot, f)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		text := string(content)
		ext := filepath.Ext(f)

		// Extract exported symbols based on language
		switch ext {
		case ".go":
			// Go: exported = capitalized function/type/var names
			for _, pat := range []*regexp.Regexp{
				regexp.MustCompile(`func\s+([A-Z]\w+)`),
				regexp.MustCompile(`type\s+([A-Z]\w+)`),
				regexp.MustCompile(`var\s+([A-Z]\w+)`),
			} {
				for _, m := range pat.FindAllStringSubmatch(text, -1) {
					exports = append(exports, exportInfo{file: f, symbol: m[1]})
				}
			}
		case ".ts", ".tsx", ".js", ".jsx":
			// JS/TS: export keyword
			exportRe := regexp.MustCompile(`export\s+(?:default\s+)?(?:function|class|const|let|var|interface|type|enum)\s+(\w+)`)
			for _, m := range exportRe.FindAllStringSubmatch(text, -1) {
				exports = append(exports, exportInfo{file: f, symbol: m[1]})
			}
		case ".py":
			// Python: top-level defs/classes (all public by convention)
			for _, pat := range []*regexp.Regexp{
				regexp.MustCompile(`(?m)^def\s+([a-zA-Z]\w+)`),
				regexp.MustCompile(`(?m)^class\s+([a-zA-Z]\w+)`),
			} {
				for _, m := range pat.FindAllStringSubmatch(text, -1) {
					if !strings.HasPrefix(m[1], "_") {
						exports = append(exports, exportInfo{file: f, symbol: m[1]})
					}
				}
			}
		}
	}

	if len(exports) == 0 {
		return issues
	}

	// Scan the entire repo (limited) for imports of these symbols
	// We search in the repo root, excluding common non-code dirs
	repoFiles := e.collectRepoSourceFiles()

	for _, exp := range exports {
		found := false
		for _, rf := range repoFiles {
			if rf == exp.file {
				continue // skip self-references
			}
			fullPath := filepath.Join(e.RepoRoot, rf)
			content, err := os.ReadFile(fullPath)
			if err != nil {
				continue
			}
			// Check if the symbol name or the file's module is referenced
			if strings.Contains(string(content), exp.symbol) {
				found = true
				break
			}
			// Also check if the file path (as import) is referenced
			// e.g., import from "./foo" matches foo.ts
			base := strings.TrimSuffix(filepath.Base(exp.file), filepath.Ext(exp.file))
			if strings.Contains(string(content), base) {
				found = true
				break
			}
		}

		if !found {
			issues = append(issues, GBCheck{
				Category: "wiring",
				File:     exp.file,
				Passed:   false,
				Detail:   fmt.Sprintf("exported symbol '%s' is not referenced anywhere else in the project", exp.symbol),
			})
		}
	}

	return issues
}

// collectRepoSourceFiles collects source files from the repo for wiring analysis
func (e *Engine) collectRepoSourceFiles() []string {
	var files []string
	sourceExts := map[string]bool{
		".go": true, ".py": true, ".js": true, ".jsx": true,
		".ts": true, ".tsx": true, ".rs": true, ".java": true,
		".rb": true, ".php": true, ".vue": true, ".svelte": true,
	}
	skipDirs := map[string]bool{
		"node_modules": true, ".git": true, "vendor": true,
		"__pycache__": true, "dist": true, "build": true,
		".next": true, "target": true, ".kyotee": true,
	}

	_ = filepath.WalkDir(e.RepoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if sourceExts[filepath.Ext(d.Name())] {
			rel, _ := filepath.Rel(e.RepoRoot, path)
			files = append(files, rel)
		}
		// Cap at 500 files to avoid scanning huge repos
		if len(files) > 500 {
			return filepath.SkipAll
		}
		return nil
	})

	return files
}
