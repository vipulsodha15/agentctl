package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/agentctl/agentctl/internal/skills"
)

func checkSkillsBuiltin(builtinDir string) Check {
	if _, err := os.Stat(builtinDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Check{
				Name:    "skills.builtin",
				Status:  StatusFail,
				Message: "built-in skills dir missing",
				Detail:  builtinDir,
			}
		}
		return Check{Name: "skills.builtin", Status: StatusFail, Message: err.Error()}
	}
	mgr := skills.NewManager(skills.Options{BuiltinDir: builtinDir})
	names, issues := scanSkillDir(builtinDir, mgr)
	if len(issues) > 0 {
		sort.Strings(issues)
		return Check{
			Name:    "skills.builtin",
			Status:  StatusFail,
			Message: fmt.Sprintf("%d skill manifest error(s)", len(issues)),
			Detail:  joinLines(issues),
		}
	}
	return Check{
		Name:    "skills.builtin",
		Status:  StatusOK,
		Message: skillCountSummary(len(names), names),
	}
}

func checkSkillsCustom(customDir, builtinDir string) Check {
	if _, err := os.Stat(customDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Check{
				Name:    "skills.custom",
				Status:  StatusOK,
				Message: "no custom skills dir (none added)",
			}
		}
		return Check{Name: "skills.custom", Status: StatusFail, Message: err.Error()}
	}
	mgr := skills.NewManager(skills.Options{CustomDir: customDir, BuiltinDir: builtinDir})
	names, issues := scanSkillDir(customDir, mgr)
	overrides := overridesOf(names, builtinDir)
	if len(issues) > 0 {
		sort.Strings(issues)
		return Check{
			Name:    "skills.custom",
			Status:  StatusFail,
			Message: fmt.Sprintf("%d skill manifest error(s)", len(issues)),
			Detail:  joinLines(issues),
		}
	}
	msg := skillCountSummary(len(names), names)
	if len(overrides) > 0 {
		msg += fmt.Sprintf("; %d override built-ins (%s)", len(overrides), joinComma(overrides))
	}
	return Check{Name: "skills.custom", Status: StatusOK, Message: msg}
}

func scanSkillDir(dir string, mgr skills.Manager) ([]string, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []string{err.Error()}
	}
	var names []string
	var issues []string
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		full := filepath.Join(dir, ent.Name())
		res, err := mgr.Validate(skills.ValidateSource{Name: ent.Name(), Path: full})
		if err != nil {
			issues = append(issues, ent.Name()+": "+err.Error())
			continue
		}
		if !res.OK {
			for _, iss := range res.Issues {
				issues = append(issues, ent.Name()+": "+iss)
			}
			continue
		}
		names = append(names, ent.Name())
	}
	sort.Strings(names)
	return names, issues
}

func overridesOf(customNames []string, builtinDir string) []string {
	if builtinDir == "" {
		return nil
	}
	var out []string
	for _, n := range customNames {
		if _, err := os.Stat(filepath.Join(builtinDir, n)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func skillCountSummary(n int, names []string) string {
	if n == 0 {
		return "0 skills"
	}
	if n <= 4 {
		return fmt.Sprintf("%d skill(s) (%s)", n, joinComma(names))
	}
	return fmt.Sprintf("%d skill(s)", n)
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
