package registry

import (
	"slices"
	"strings"
	"testing"
)

// TestProcessIdentityHoistPreservesCatalogMatching verifies the intended
// refactor property: moving process-only derivation outside the tool loop must
// not change any built-in tool's answer for any process identity.
func TestProcessIdentityHoistPreservesCatalogMatching(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	corpus := processIdentityEquivalenceCorpus(reg.tools)
	if len(corpus) < 800 {
		t.Fatalf("adversarial process corpus has %d entries, want at least 800", len(corpus))
	}

	for processIndex, process := range corpus {
		identity := processIdentityForMatch(process)
		for _, tool := range reg.tools {
			legacy := legacyToolMatchesProcess(tool, process)
			hoisted := tool.matchesProcess(identity)
			if hoisted != legacy {
				t.Fatalf("process[%d] tool %q: hoisted match = %t, legacy match = %t; process = %#v",
					processIndex, tool.ID, hoisted, legacy, process)
			}
		}
	}
}

// TestProcessIdentityHoistKeepsRawSurfaces makes the raw-versus-normalized
// boundary explicit. Slash normalization belongs only in the regex and
// exclude branches, after this process-derived value has been built.
func TestProcessIdentityHoistKeepsRawSurfaces(t *testing.T) {
	process := ProcessInfo{
		Name:    `CoDeX.EXE`,
		Exe:     `\\Server\Share/Codex.EXE`,
		Argv0:   `C:\Tools/mixed\codex.exe`,
		Cmdline: `"D:/Other\codex.exe" daemon`,
	}
	wantIdentities := []string{
		`CoDeX.EXE`,
		`C:\Tools/mixed\codex.exe`,
		`\\Server\Share/Codex.EXE`,
		`D:/Other\codex.exe`,
	}

	identity := processIdentityForMatch(process)
	if identity.shellInterpreter {
		t.Fatal("non-shell process was classified as a shell interpreter")
	}
	if !slices.Equal(identity.identities, wantIdentities) {
		t.Fatalf("process identities = %#v, want raw identities %#v", identity.identities, wantIdentities)
	}
	if identity.subcommand != "daemon" {
		t.Fatalf("subcommand = %q, want raw subcommand %q", identity.subcommand, "daemon")
	}
}

func processIdentityEquivalenceCorpus(tools []Tool) []ProcessInfo {
	corpus := []ProcessInfo{
		{},
		{Name: "   ", Exe: "\t", Argv0: "\n"},
		{Name: "codex", Exe: "/usr/local/bin/not-codex"},
		{Name: "CODEX.EXE", Exe: `C:\TOOLS\CODEX.EXE`},
		{Name: "codex.exe", Exe: `c:/tools/codex.exe`},
		{Name: "codex.exe", Exe: `C:\tools/codex.exe`},
		{Name: "codex.exe", Exe: `C:\tools/codex.exe\`},
		{Name: "codex.exe", Exe: `C:/tools/codex.exe/`},
		{Name: "codex.exe", Exe: `C:\tools/mixed/codex.exe`},
		{Name: "codex.exe", Exe: `\\server\share\tools\codex.exe`},
		{Name: "codex.exe", Exe: `//server/share/tools/codex.exe`},
		{Name: "node", Exe: "/usr/local/bin/node", Argv: []string{"node", `C:\pkg\@openai\codex\cli.js`, "exec"}},
		{Name: "python3.13", Exe: "/usr/bin/python3.13", Argv: []string{"python3.13", "-m", "aider", "--help"}},
		{Name: "pythonish-tool", Exe: "/usr/bin/pythonish-tool", Argv: []string{"pythonish-tool", "-m", "aider"}},
	}

	for _, shell := range []string{"bash", "sh", "zsh", "fish", "dash", "ash", "ksh", "csh", "tcsh", "cmd", "powershell", "pwsh"} {
		corpus = append(corpus,
			ProcessInfo{Name: shell, Cmdline: shell + " -c codex"},
			ProcessInfo{Name: shell + ".exe", Exe: `C:\shells\` + shell + `.exe`, Cmdline: shell + ".exe /C claude"},
			ProcessInfo{Name: "worker", Argv0: shell, Exe: "/bin/worker", Argv: []string{shell, "-c", "gemini"}},
		)
	}

	for _, tool := range tools {
		name := tool.Match.Name
		upperName := strings.ToUpper(name)
		exeName := name + ".exe"
		corpus = append(corpus,
			ProcessInfo{Name: name},
			ProcessInfo{Name: upperName},
			ProcessInfo{Name: exeName},
			ProcessInfo{Name: "not-" + name},
			ProcessInfo{Exe: "/usr/local/bin/" + name},
			ProcessInfo{Exe: `C:\Program Files\Termp\` + exeName},
			ProcessInfo{Exe: `C:/Program Files/Termp/` + exeName},
			ProcessInfo{Exe: `C:\Program Files/Termp\` + exeName},
			ProcessInfo{Exe: `\\server\share\tools\` + exeName},
			ProcessInfo{Exe: `//server/share/tools/` + exeName},
			ProcessInfo{Exe: `c:\TOOLS\` + upperName + `.EXE`},
			ProcessInfo{Exe: `C:\tools\` + exeName + `\`},
			ProcessInfo{Exe: `C:/tools/` + exeName + `/`},
			ProcessInfo{Argv0: `C:\tools\` + exeName},
			ProcessInfo{Cmdline: `"C:\Program Files\Termp\` + exeName + `" --version`},
			ProcessInfo{Argv: []string{`C:\tools\` + exeName, "daemon"}},
			ProcessInfo{Name: "node", Exe: "/usr/bin/node", Argv: []string{"node", `/opt/tools/` + name + `.js`, "serve"}},
			ProcessInfo{Name: "python3.12", Exe: "/usr/bin/python3.12", Argv: []string{"python3.12", `/opt/tools/` + name + `.py`, "serve"}},
			ProcessInfo{Name: "python3.12", Exe: "/usr/bin/python3.12", Argv: []string{"python3.12", "-m", name, "serve"}},
		)
	}

	return corpus
}

// legacyToolMatchesProcess is the pre-hoist matcher. It intentionally derives
// shell and identity data for each tool so the equivalence test and benchmark
// compare the old execution shape with the production implementation.
func legacyToolMatchesProcess(tool Tool, process ProcessInfo) bool {
	if isShellInterpreterProcess(process) {
		return false
	}

	identities, subcommand := processMatchIdentity(process)
	matched := false

	if tool.Match.Name != "" {
		matchName := normalizeName(tool.Match.Name)
		for _, candidate := range identities {
			if strings.EqualFold(normalizeName(candidate), matchName) {
				matched = true
				break
			}
		}
	}

	if !matched && tool.Match.compiled != nil {
		for _, identity := range identities {
			if tool.Match.compiled.MatchString(strings.ReplaceAll(identity, `\`, "/")) {
				matched = true
				break
			}
		}
	}
	if !matched {
		return false
	}

	if tool.compiledExclude != nil {
		excludeSurfaces := identities
		if subcommand != "" {
			excludeSurfaces = append(append([]string(nil), identities...), subcommand)
		}
		for _, surface := range excludeSurfaces {
			if tool.compiledExclude.MatchString(strings.ReplaceAll(surface, `\`, "/")) {
				return false
			}
		}
	}
	return true
}
