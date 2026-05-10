package skills

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	SourceBuiltin = "builtin"
	SourceCustom  = "custom"

	ManifestFile = "manifest.json"
	SkillMD      = "SKILL.md"

	MaxSkillBytes = 10 * 1024 * 1024
)

var (
	ErrNotFound        = errors.New("skill: not found")
	ErrBuiltinReadOnly = errors.New("skill: built-in cannot be modified; reinstall to update")
	ErrInvalidName     = errors.New("skill: invalid name")
	ErrAlreadyExists   = errors.New("skill: already exists; pass --force to overwrite")
)

type Manifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type InstalledSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Path        string `json:"path,omitempty"`
	Overrides   bool   `json:"overrides,omitempty"`
}

type AddSource struct {
	Path string
}

type ValidateSource struct {
	Name string
	Path string
}

type ValidateResult struct {
	Name        string
	Description string
	Path        string
	OK          bool
	Issues      []string
}

type ImportOptions struct {
	Force  bool
	DryRun bool
}

type ImportResult struct {
	Imported         []string
	Skipped          []SkippedImport
	ShadowedBuiltins []string
}

type SkippedImport struct {
	Name   string
	Reason string
}

type AddResult struct {
	Name string
	Path string
}

type Manager interface {
	ListInstalled() ([]InstalledSkill, error)
	Add(src AddSource, opts ImportOptions) (AddResult, error)
	Remove(name string) error
	Validate(src ValidateSource) (ValidateResult, error)
	Export(name string) ([]byte, error)
	Import(srcPath, name string, opts ImportOptions) (ImportResult, error)
	ImportDirectory(rootDir string, opts ImportOptions) ([]string, []SkippedImport, error)
	Show(name string) (InstalledSkill, error)
	Scaffold(name string) (string, error)
}

type Options struct {
	BuiltinDir string
	CustomDir  string
}

func NewManager(opts Options) Manager {
	return &manager{builtinDir: opts.BuiltinDir, customDir: opts.CustomDir}
}

type manager struct {
	builtinDir string
	customDir  string
}

func (m *manager) ListInstalled() ([]InstalledSkill, error) {
	custom, err := readSkillDir(m.customDir, SourceCustom)
	if err != nil {
		return nil, err
	}
	builtin, err := readSkillDir(m.builtinDir, SourceBuiltin)
	if err != nil {
		return nil, err
	}
	customByName := map[string]bool{}
	for _, c := range custom {
		customByName[c.Name] = true
	}
	out := make([]InstalledSkill, 0, len(custom)+len(builtin))
	for _, c := range custom {
		out = append(out, c)
	}
	for _, b := range builtin {
		if customByName[b.Name] {
			continue
		}
		out = append(out, b)
	}
	for i := range out {
		if out[i].Source == SourceCustom {
			for _, b := range builtin {
				if b.Name == out[i].Name {
					out[i].Overrides = true
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *manager) Show(name string) (InstalledSkill, error) {
	all, err := m.ListInstalled()
	if err != nil {
		return InstalledSkill{}, err
	}
	for _, s := range all {
		if s.Name == name {
			return s, nil
		}
	}
	return InstalledSkill{}, ErrNotFound
}

func (m *manager) Scaffold(name string) (string, error) {
	if !validName(name) {
		return "", ErrInvalidName
	}
	dest := filepath.Join(m.customDir, name)
	if _, err := os.Stat(dest); err == nil {
		return "", ErrAlreadyExists
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	manifest := Manifest{Name: name, Description: "TODO: describe what " + name + " does."}
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(dest, ManifestFile), mb, 0o644); err != nil {
		return "", err
	}
	body := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n# %s\n\nTODO\n", name, manifest.Description, name)
	if err := os.WriteFile(filepath.Join(dest, SkillMD), []byte(body), 0o644); err != nil {
		return "", err
	}
	return dest, nil
}

func (m *manager) Add(src AddSource, opts ImportOptions) (AddResult, error) {
	info, err := os.Stat(src.Path)
	if err != nil {
		return AddResult{}, err
	}
	if info.IsDir() {
		return m.addFromDir(src.Path, opts)
	}
	return m.addFromTarball(src.Path, opts)
}

func (m *manager) addFromDir(srcDir string, opts ImportOptions) (AddResult, error) {
	mf, err := readManifest(srcDir)
	if err != nil {
		return AddResult{}, err
	}
	name := mf.Name
	if name == "" {
		name = filepath.Base(srcDir)
	}
	if !validName(name) {
		return AddResult{}, ErrInvalidName
	}
	if err := validateSize(srcDir); err != nil {
		return AddResult{}, err
	}
	dest := filepath.Join(m.customDir, name)
	if _, err := os.Stat(dest); err == nil {
		if !opts.Force {
			return AddResult{}, ErrAlreadyExists
		}
		if err := os.RemoveAll(dest); err != nil {
			return AddResult{}, err
		}
	}
	if opts.DryRun {
		return AddResult{Name: name, Path: dest}, nil
	}
	if err := copyDir(srcDir, dest); err != nil {
		return AddResult{}, err
	}
	return AddResult{Name: name, Path: dest}, nil
}

func (m *manager) addFromTarball(path string, opts ImportOptions) (AddResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return AddResult{}, err
	}
	defer func() { _ = f.Close() }()
	tmp, err := os.MkdirTemp("", "skill-extract-*")
	if err != nil {
		return AddResult{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := extractTarball(f, tmp); err != nil {
		return AddResult{}, err
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return AddResult{}, err
	}
	for _, ent := range entries {
		if ent.IsDir() {
			return m.addFromDir(filepath.Join(tmp, ent.Name()), opts)
		}
	}
	return m.addFromDir(tmp, opts)
}

func (m *manager) Remove(name string) error {
	if isBuiltin(m.builtinDir, name) {
		return ErrBuiltinReadOnly
	}
	dest := filepath.Join(m.customDir, name)
	if _, err := os.Stat(dest); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return os.RemoveAll(dest)
}

func (m *manager) Validate(src ValidateSource) (ValidateResult, error) {
	dir := src.Path
	if dir == "" {
		s, err := m.Show(src.Name)
		if err != nil {
			return ValidateResult{Name: src.Name, OK: false, Issues: []string{err.Error()}}, nil
		}
		dir = s.Path
	}
	mf, err := readManifest(dir)
	res := ValidateResult{Name: src.Name, Path: dir}
	if err != nil {
		res.Issues = append(res.Issues, err.Error())
		return res, nil
	}
	res.Name = mf.Name
	res.Description = mf.Description
	if mf.Description == "" {
		res.Issues = append(res.Issues, "manifest.description is empty")
	}
	if dirName := filepath.Base(dir); mf.Name != "" && dirName != mf.Name {
		res.Issues = append(res.Issues, fmt.Sprintf("manifest.name %q != dir %q", mf.Name, dirName))
	}
	if err := validateSize(dir); err != nil {
		res.Issues = append(res.Issues, err.Error())
	}
	res.OK = len(res.Issues) == 0
	return res, nil
}

func (m *manager) Export(name string) ([]byte, error) {
	all, err := m.ListInstalled()
	if err != nil {
		return nil, err
	}
	var path string
	for _, s := range all {
		if s.Name == name {
			path = s.Path
			break
		}
	}
	if path == "" {
		return nil, ErrNotFound
	}
	return tarballDir(path)
}

func (m *manager) Import(srcPath, name string, opts ImportOptions) (ImportResult, error) {
	out := ImportResult{}
	if name == "" {
		name = filepath.Base(srcPath)
	}
	if !validName(name) {
		out.Skipped = append(out.Skipped, SkippedImport{Name: name, Reason: "invalid name"})
		return out, nil
	}
	dest := filepath.Join(m.customDir, name)
	if _, err := os.Stat(dest); err == nil && !opts.Force {
		out.Skipped = append(out.Skipped, SkippedImport{Name: name, Reason: "already in custom-skills"})
		return out, nil
	}
	mf, err := readManifest(srcPath)
	if err != nil {
		out.Skipped = append(out.Skipped, SkippedImport{Name: name, Reason: err.Error()})
		return out, nil
	}
	if mf.Description == "" {
		out.Skipped = append(out.Skipped, SkippedImport{Name: name, Reason: "manifest description empty"})
		return out, nil
	}
	if isBuiltin(m.builtinDir, name) && !opts.Force {
		out.Skipped = append(out.Skipped, SkippedImport{Name: name, Reason: "would shadow built-in; use --force"})
		return out, nil
	}
	if err := validateSize(srcPath); err != nil {
		out.Skipped = append(out.Skipped, SkippedImport{Name: name, Reason: err.Error()})
		return out, nil
	}
	if opts.DryRun {
		out.Imported = append(out.Imported, name)
		if isBuiltin(m.builtinDir, name) {
			out.ShadowedBuiltins = append(out.ShadowedBuiltins, name)
		}
		return out, nil
	}
	if err := os.MkdirAll(m.customDir, 0o700); err != nil {
		return out, err
	}
	if _, err := os.Stat(dest); err == nil {
		if err := os.RemoveAll(dest); err != nil {
			return out, err
		}
	}
	if err := copyDir(srcPath, dest); err != nil {
		return out, err
	}
	out.Imported = append(out.Imported, name)
	if isBuiltin(m.builtinDir, name) {
		out.ShadowedBuiltins = append(out.ShadowedBuiltins, name)
	}
	return out, nil
}

func (m *manager) ImportDirectory(rootDir string, opts ImportOptions) ([]string, []SkippedImport, error) {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return nil, nil, err
	}
	var imported []string
	var skipped []SkippedImport
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		full := filepath.Join(rootDir, ent.Name())
		res, err := m.Import(full, ent.Name(), opts)
		if err != nil {
			skipped = append(skipped, SkippedImport{Name: ent.Name(), Reason: err.Error()})
			continue
		}
		imported = append(imported, res.Imported...)
		skipped = append(skipped, res.Skipped...)
	}
	return imported, skipped, nil
}

func readSkillDir(root, source string) ([]InstalledSkill, error) {
	if root == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]InstalledSkill, 0, len(entries))
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		dir := filepath.Join(root, ent.Name())
		mf, err := readManifest(dir)
		if err != nil {
			continue
		}
		out = append(out, InstalledSkill{
			Name:        nonEmpty(mf.Name, ent.Name()),
			Description: mf.Description,
			Source:      source,
			Path:        dir,
		})
	}
	return out, nil
}

func readManifest(dir string) (Manifest, error) {
	if data, err := os.ReadFile(filepath.Join(dir, ManifestFile)); err == nil {
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return Manifest{}, fmt.Errorf("manifest.json: %w", err)
		}
		return m, nil
	}
	if data, err := os.ReadFile(filepath.Join(dir, SkillMD)); err == nil {
		return parseSkillMDFrontMatter(data)
	}
	return Manifest{}, fmt.Errorf("no manifest.json or SKILL.md in %s", dir)
}

func parseSkillMDFrontMatter(body []byte) (Manifest, error) {
	s := string(body)
	if !strings.HasPrefix(s, "---") {
		return Manifest{}, fmt.Errorf("SKILL.md missing YAML front matter")
	}
	end := strings.Index(s[3:], "---")
	if end < 0 {
		return Manifest{}, fmt.Errorf("SKILL.md unterminated front matter")
	}
	front := s[3 : 3+end]
	mf := Manifest{}
	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "name":
			mf.Name = v
		case "description":
			mf.Description = v
		}
	}
	return mf, nil
}

func validateSize(dir string) error {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
			if total > MaxSkillBytes {
				return fmt.Errorf("skill exceeds %d bytes", MaxSkillBytes)
			}
		}
		return nil
	})
	return err
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func tarballDir(dir string) ([]byte, error) {
	var buf strings.Builder
	gz := gzip.NewWriter(&writerString{b: &buf})
	tw := tar.NewWriter(gz)
	root := filepath.Base(dir)
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		name := filepath.Join(root, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(name)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = tw.Write(body)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

type writerString struct {
	b *strings.Builder
}

func (w *writerString) Write(p []byte) (int, error) {
	w.b.Write(p)
	return len(p), nil
}

func extractTarball(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("tar entry escapes root: %s", hdr.Name)
		}
		target := filepath.Join(dst, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			_ = f.Close()
		}
	}
	return nil
}

func isBuiltin(builtinDir, name string) bool {
	if builtinDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(builtinDir, name))
	return err == nil
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !(c == '-' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
