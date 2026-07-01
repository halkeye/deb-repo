package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	flag "github.com/spf13/pflag"
	"github.com/ulikunitz/xz"
	"gopkg.in/yaml.v3"
)

// var errUnknownExtension = errors.New("unknown extension")

type pkgType struct {
	Url          string            `yaml:"url"`
	Name         string            `yaml:"name"`
	Version      string            `yaml:"version"`
	UrlOverrides map[string]string `yaml:"url_overrides"`
}

// cargoType is a Rust crate packaged into a .deb with cargo-deb
// (https://github.com/kornelski/cargo-deb). The git repo is cloned, an
// optional branch/sha is checked out, then cargo-deb builds one .deb per arch.
type cargoType struct {
	Name string `yaml:"name"`
	Url  string `yaml:"url"`
	// Version is a git ref (tag, sha, or branch) to check out before building.
	Version string `yaml:"version"`
}

type alternativeType struct {
	Name     string `yaml:"name"`
	Link     string `yaml:"link"`
	Path     string `yaml:"path"`
	Priority int    `yaml:"priority"`
}

type appType struct {
	Name          string            `yaml:"name"`
	Url           string            `yaml:"url"`
	Version       string            `yaml:"version"`
	Type          string            `yaml:"type"`
	UrlOverrides  map[string]string `yaml:"url_overrides"`
	ArchOverrides map[string]string `yaml:"arch_verrides"`
	MoveRules     []struct {
		SrcRegex regexp.Regexp `yaml:"src_regex"`
		Dst      string        `yaml:"dst"`
		Mode     int           `yaml:"mode"`
	} `yaml:"move_rules"`
	ExtraFiles []struct {
		URL  string `yaml:"url"`
		Dst  string `yaml:"dst"`
		Mode int    `yaml:"mode"`
	} `yaml:"extra_files"`
	Alternatives []alternativeType `yaml:"alternatives"`
}

func (pkg pkgType) BuildURL(arch archType) string {
	pkgUrl := pkg.Url
	if val, ok := pkg.UrlOverrides[arch.deb]; ok {
		pkgUrl = val
	}

	return ProcessURL(pkgUrl, pkg.Version, arch)
}

func (app appType) BuildURL(arch archType) string {
	if val, ok := app.ArchOverrides[arch.deb]; ok {
		arch = archType{
			deb:     val,
			ansible: val,
			kubectx: val,
		}
	}

	appUrl := app.Url
	if val, ok := app.UrlOverrides[arch.deb]; ok {
		appUrl = val
	}

	return ProcessURL(appUrl, app.Version, arch)
}

func ProcessURL(url string, version string, arch archType) string {

	values := map[string]string{
		"version":              version,
		"deb_architecture":     arch.deb,
		"ansible_architecture": arch.ansible,
		"kubectx_architecture": arch.kubectx,
	}

	return templateRe.ReplaceAllStringFunc(url, func(s string) string {
		varName := templateRe.FindStringSubmatch(s)[1]
		if val, ok := values[varName]; ok {
			return val
		}
		return s
	})
}

type archType struct {
	deb     string
	ansible string
	kubectx string
	rust    string
}

type readerFunc func(r io.Reader) (io.Reader, error)

var (
	unarchiveFuncs = map[string]readerFunc{
		".tar.gz": func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) },
		".tgz":    func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) },
		".tar.xz": func(r io.Reader) (io.Reader, error) { return xz.NewReader(r) },
		".txz":    func(r io.Reader) (io.Reader, error) { return xz.NewReader(r) },
	}
	archs = []archType{
		{
			kubectx: "x86_64",
			deb:     "amd64",
			ansible: "x86_64",
			rust:    "x86_64-unknown-linux-gnu",
		},
		{
			deb:     "arm64",
			kubectx: "arm64",
			ansible: "aarch64",
			rust:    "aarch64-unknown-linux-gnu",
		},
	}
)

var (
	templateRe = regexp.MustCompile(`{{\s*(?P<var>\w+)\s*}}`)
)

func downloadURL(dir string, filename string, url string) error {
	// Create tmp directory if it doesn't exist
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create %s directory: %v", dir, err)
	}

	filepath := filepath.Join(dir, filename)

	if _, err := os.Stat(filepath); err == nil {
		return nil
	}

	// Download and save file
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download URL: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download URL %s: status code %d", url, resp.StatusCode)
	}

	// Save file to directory
	file, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("failed to create file: %v", err)
	}
	defer func() { _ = file.Close() }()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return fmt.Errorf("failed to read response body: %v", err)
	}

	return nil
}

func main() {
	var err error

	var singleApp = flag.String("app", "", "only process single app")
	var singleArch = flag.String("arch", "", "only build a single arch (e.g. amd64 or arm64)")
	var logLevel = flag.String("log-level", "info", "log level (debug, info, warn, error)")
	flag.Parse()

	// Configure slog based on log level
	var level slog.Level
	switch strings.ToLower(*logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		fmt.Fprintf(os.Stderr, "unknown log level %q, using info\n", *logLevel)
		level = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if singleArch != nil && *singleArch != "" {
		var filtered []archType
		for _, arch := range archs {
			if arch.deb == *singleArch {
				filtered = append(filtered, arch)
			}
		}
		if len(filtered) == 0 {
			slog.Error("unknown arch", "arch", *singleArch)
			os.Exit(1)
		}
		archs = filtered
	}

	pkgs, apps, cargos, err := loadYaml()
	if err != nil {
		slog.Error("failed to load yaml", "error", err)
		os.Exit(1)
	}
	if singleApp != nil && *singleApp != "" {
		for _, pkg := range pkgs {
			if pkg.Name == *singleApp {
				pkgs = []pkgType{pkg}
				break
			}
		}
		for _, app := range apps {
			if app.Name == *singleApp {
				apps = []appType{app}
				break
			}
		}
		for _, cargo := range cargos {
			if cargo.Name == *singleApp {
				cargos = []cargoType{cargo}
				break
			}
		}
		if len(pkgs) > 1 {
			pkgs = []pkgType{}
		}
		if len(apps) > 1 {
			apps = []appType{}
		}
		if len(cargos) > 1 {
			cargos = []cargoType{}
		}
	}

	err = downloadDebs(pkgs)
	if err != nil {
		slog.Error("downloadDebs failed", "error", err)
		os.Exit(1)
	}

	err = downloadApps(apps)
	if err != nil {
		slog.Error("downloadApps failed", "error", err)
		os.Exit(1)
	}

	err = downloadCargoDebs(cargos)
	if err != nil {
		slog.Error("downloadCargoDebs failed", "error", err)
		os.Exit(1)
	}
}

func loadYaml() ([]pkgType, []appType, []cargoType, error) {
	pkgs := []pkgType{}
	apps := []appType{}
	cargos := []cargoType{}

	matches, err := filepath.Glob(filepath.Join("packages", "*.yaml"))
	if err != nil {
		return pkgs, apps, cargos, fmt.Errorf("globbing packages: %w", err)
	}

	for _, match := range matches {
		err := func() error {
			yamlFile, err := os.Open(match)
			if err != nil {
				return fmt.Errorf("reading %s: %w", match, err)
			}
			defer func() { _ = yamlFile.Close() }()

			var app appType
			if err := yaml.NewDecoder(yamlFile).Decode(&app); err != nil {
				return fmt.Errorf("decoding %s: %w", match, err)
			}

			// "deb" entries are prebuilt .deb downloads (pkg); "release_asset"
			// entries are built from release archives/binaries (app);
			// "cargo-deb" entries are Rust crates built from source with cargo-deb.
			switch app.Type {
			case "deb":
				pkgs = append(pkgs, pkgType{
					Url:          app.Url,
					Name:         app.Name,
					Version:      app.Version,
					UrlOverrides: app.UrlOverrides,
				})
			case "release_asset":
				apps = append(apps, app)
			case "cargo-deb":
				cargos = append(cargos, cargoType{
					Name:    app.Name,
					Url:     app.Url,
					Version: app.Version,
				})
			default:
				return fmt.Errorf("unknown type %q (want \"deb\", \"release_asset\" or \"cargo-deb\")", app.Type)
			}
			return nil
		}()
		if err != nil {
			return pkgs, apps, cargos, fmt.Errorf("processing %s: %w", match, err)
		}
	}

	return pkgs, apps, cargos, nil

}

func downloadDebs(pkgs []pkgType) error {
	for _, arch := range archs {
		for _, pkg := range pkgs {
			filename := fmt.Sprintf("%s-%s-%s.deb", pkg.Name, arch.deb, pkg.Version)
			slog.Info("Downloading", "filename", filename)
			err := downloadURL(filepath.Join("tmp", arch.deb), filename, pkg.BuildURL(arch))
			if err != nil {
				return fmt.Errorf("downloading deb %s: %w", filename, err)
			}
		}
	}
	return nil
}

func getUnarchiveFunc(appUrl string) readerFunc {
	for ext, unarchiveFunc := range unarchiveFuncs {
		if strings.HasSuffix(appUrl, ext) {
			return unarchiveFunc
		}
	}

	return nil
}

func downloadApp(app appType, arch archType) error {
	var err error

	appDir := filepath.Join("tmp", "app", app.Name, arch.deb)
	workDir := filepath.Join(appDir, "work")
	debWorkDir := filepath.Join(appDir, "deb")

	appUrl := app.BuildURL(arch)

	filename := filepath.Base(appUrl)
	unarchiveFunc := getUnarchiveFunc(appUrl)
	// if unarchiveFunc == nil {
	// 	if strings.Contains(filename, ".") {
	// 		return errUnknownExtension
	// 	}
	// }

	slog.Info("Downloading App", "path", filepath.Join(appDir, filename))

	if unarchiveFunc == nil {
		err = downloadURL(workDir, filename, appUrl)
		if err != nil {
			return fmt.Errorf("downloading %s: %w", appUrl, err)
		}
	} else {
		err = downloadURL(appDir, filename, appUrl)
		if err != nil {
			return fmt.Errorf("downloading %s: %w", appUrl, err)
		}

		err = unarchive(filepath.Join(appDir, filename), unarchiveFunc, workDir)
		if err != nil {
			return fmt.Errorf("extracting %s: %w", filepath.Join(appDir, filename), err)
		}
	}

	err = processApp(app, workDir, debWorkDir)
	if err != nil {
		return fmt.Errorf("processing app: %w", err)
	}

	for _, extraFile := range app.ExtraFiles {
		err := downloadURL(filepath.Join(debWorkDir, filepath.Dir(extraFile.Dst)), filepath.Base(extraFile.Dst), ProcessURL(extraFile.URL, app.Version, arch))
		if err != nil {
			return fmt.Errorf("unable to extra url %s: %w", extraFile.URL, err)
		}
	}

	debDir := filepath.Join("tmp", arch.deb)
	if err := os.MkdirAll(debDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", debDir, err)
	}

	if err := writeControl(debWorkDir, app.Name, app.Version, arch.deb); err != nil {
		return fmt.Errorf("writing control file: %w", err)
	}

	if err := writeAlternativesScripts(debWorkDir, app.Alternatives); err != nil {
		return fmt.Errorf("writing alternatives scripts: %w", err)
	}

	outDeb := fmt.Sprintf("%s_%s_%s.deb", app.Name, app.Version, arch.deb)
	if err := buildDeb(debWorkDir, filepath.Join(debDir, outDeb)); err != nil {
		return fmt.Errorf("building deb: %w", err)
	}

	return nil
}

func downloadApps(apps []appType) error {
	for _, arch := range archs {
		for _, app := range apps {
			err := downloadApp(app, arch)
			if err != nil {
				return fmt.Errorf("downloading apps %s: %w", app.Name, err)
			}
		}
	}
	return nil
}

func processApp(app appType, workDir string, debWorkDir string) error {
	defer warnTime("processApp "+app.Name, 10*time.Second)()

	madeChanges := false

	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("creating workDir %s: %w", workDir, err)
	}

	if err := os.MkdirAll(debWorkDir, 0o755); err != nil {
		return fmt.Errorf("creating debWorkDir %s: %w", debWorkDir, err)
	}

	err := filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		for ruleIdx, rule := range app.MoveRules {
			filenameWithoutWork := path[len(workDir)+1:]
			// fmt.Printf("Processing file %s with regex %s == %t\n", filenameWithoutWork, rule.SrcRegex.String(), rule.SrcRegex.MatchString(filenameWithoutWork))
			if !rule.SrcRegex.MatchString(filenameWithoutWork) {
				continue
			}

			newFile := filepath.Join(debWorkDir, rule.Dst)
			if err := os.MkdirAll(filepath.Dir(newFile), 0o755); err != nil {
				return fmt.Errorf("processing app %s's %d mkdir: %w", app.Name, ruleIdx, err)
			}

			madeChanges = true
			os.Remove(newFile)
			err = os.Link(path, newFile)
			if err != nil {
				return fmt.Errorf("processing app %s's %d moving file %s to %s: %w", app.Name, ruleIdx, path, newFile, err)
			}

			err = os.Chmod(newFile, os.FileMode(rule.Mode))
			if err != nil {
				return fmt.Errorf("processing app %s's %d setting permissions on file %s to %d: %w", app.Name, ruleIdx, newFile, rule.Mode, err)
			}
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("processing app %s: %w", app.Name, err)
	}

	if !madeChanges {
		return errors.New("no assets found")
	}
	return nil
}

func unarchive(file string, reader readerFunc, dst string) error {
	defer warnTime("unarchive "+file, time.Second)()
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	archiveReader, err := reader(f)
	if err != nil {
		return err
	}

	if closeReader, ok := archiveReader.(io.ReadCloser); ok {
		defer func() { _ = closeReader.Close() }()
	}

	tr := tar.NewReader(archiveReader)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dst, h.Name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			err = func() error {
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return err
				}
				out, err := os.Create(target)
				if err != nil {
					return err
				}
				defer func() { _ = out.Close() }()

				_, err = io.Copy(out, tr)
				return err
			}()
			if err != nil {
				return err
			}
		default:
			slog.Debug("skipping tar entry", "name", h.Name, "type", string(h.Typeflag))
		}
	}
	return nil
}

func writeControl(dir string, name, version, arch string) error {
	defer warnTime("writeControl "+dir, time.Second)()
	debianDir := filepath.Join(dir, "DEBIAN")
	if err := os.MkdirAll(debianDir, 0755); err != nil {
		return fmt.Errorf("failed to create %s directory: %v", debianDir, err)
	}

	ctrl := fmt.Sprintf(`Package: %s
Version: %s
Architecture: %s
Maintainer: Gavin Mogan <debian@gavinmogan.com>
Section: extra
Priority: optional
Description: %s packaged from tgz
`, name, version, arch, name)
	return os.WriteFile(filepath.Join(debianDir, "control"), []byte(ctrl), 0o644)
}

func writeAlternativesScripts(dir string, alternatives []alternativeType) error {
	if len(alternatives) == 0 {
		return nil
	}

	debianDir := filepath.Join(dir, "DEBIAN")
	if err := os.MkdirAll(debianDir, 0755); err != nil {
		return fmt.Errorf("failed to create %s directory: %v", debianDir, err)
	}

	// Generate postinst script
	var postinstLines []string
	postinstLines = append(postinstLines, "#!/bin/sh", "set -e")
	for _, alt := range alternatives {
		postinstLines = append(postinstLines, fmt.Sprintf(
			"update-alternatives --install %s %s %s %d",
			alt.Link, alt.Name, alt.Path, alt.Priority,
		))
	}
	postinstLines = append(postinstLines, "")
	postinst := strings.Join(postinstLines, "\n")
	if err := os.WriteFile(filepath.Join(debianDir, "postinst"), []byte(postinst), 0o755); err != nil {
		return fmt.Errorf("failed to write postinst: %v", err)
	}

	// Generate prerm script
	var prermLines []string
	prermLines = append(prermLines, "#!/bin/sh", "set -e")
	for _, alt := range alternatives {
		prermLines = append(prermLines, fmt.Sprintf(
			"update-alternatives --remove %s %s",
			alt.Name, alt.Path,
		))
	}
	prermLines = append(prermLines, "")
	prerm := strings.Join(prermLines, "\n")
	if err := os.WriteFile(filepath.Join(debianDir, "prerm"), []byte(prerm), 0o755); err != nil {
		return fmt.Errorf("failed to write prerm: %v", err)
	}

	return nil
}

func buildDeb(dir, outDeb string) error {
	defer warnTime("buildDeb "+dir, 15*time.Second)()
	cmd := exec.Command("fakeroot", "dpkg-deb", "--build", dir, outDeb)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// runCommand runs name+args in dir (cwd when empty), streaming output.
func runCommand(dir, name string, args ...string) error {
	slog.Debug("running command", "dir", dir, "cmd", name, "args", strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func downloadCargoDebs(cargos []cargoType) error {
	for _, arch := range archs {
		for _, cargo := range cargos {
			if err := buildCargoDeb(cargo, arch); err != nil {
				return fmt.Errorf("building cargo-deb %s for %s: %w", cargo.Name, arch.deb, err)
			}
		}
	}
	return nil
}

// buildCargoDeb clones a Rust crate, checks out its ref, and runs cargo-deb to
// produce a .deb for the given arch in tmp/<arch>/.
func buildCargoDeb(cargo cargoType, arch archType) error {
	defer warnTime("buildCargoDeb "+cargo.Name+" "+arch.deb, 60*time.Second)()

	debDir := filepath.Join("tmp", arch.deb)
	if err := os.MkdirAll(debDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", debDir, err)
	}

	srcDir := filepath.Join("tmp", "cargo", cargo.Name)
	if err := cloneCargoSrc(cargo, srcDir); err != nil {
		return err
	}

	cargoToml := map[string]any{}

	tomlFile, err := os.Open(filepath.Join(srcDir, "Cargo.toml"))
	if err != nil {
		return fmt.Errorf("reading Cargo.toml: %w", err)
	}
	defer func() { _ = tomlFile.Close() }()

	if err = toml.NewDecoder(tomlFile).Decode(&cargoToml); err != nil {
		return fmt.Errorf("parsing Cargo.toml: %w", err)
	}

	if cargoToml["package"].(map[string]any)["version"] != "" {
		cargo.Version = cargoToml["package"].(map[string]any)["version"].(string)
	}

	needsChanging := false

	// Ensure package exists
	if cargoToml["package"] == nil {
		cargoToml["package"] = map[string]any{}
	}
	packageMap, ok := cargoToml["package"].(map[string]any)
	if !ok {
		return fmt.Errorf("package is not a map")
	}

	// Ensure metadata exists
	if packageMap["metadata"] == nil {
		packageMap["metadata"] = map[string]any{}
	}
	metadataMap, ok := packageMap["metadata"].(map[string]any)
	if !ok {
		return fmt.Errorf("package.metadata is not a map")
	}

	// Ensure deb exists
	if metadataMap["deb"] == nil {
		metadataMap["deb"] = map[string]any{}
	}
	debMap, ok := metadataMap["deb"].(map[string]any)
	if !ok {
		return fmt.Errorf("package.metadata.deb is not a map")
	}

	if debMap["section"] == nil {
		needsChanging = true
		debMap["section"] = "extra"
	}

	if debMap["priority"] == nil {
		needsChanging = true
		debMap["priority"] = "optional"
	}

	slog.Debug("Cargo.toml needs changing", "needsChanging", needsChanging)

	if needsChanging {
		tomlFile, err = os.Create(filepath.Join(srcDir, "Cargo.toml"))
		if err != nil {
			return fmt.Errorf("opening Cargo.toml for writing: %w", err)
		}

		defer func() { _ = tomlFile.Close() }()

		if err = toml.NewEncoder(tomlFile).Encode(cargoToml); err != nil {
			return fmt.Errorf("updating Cargo.toml: %w", err)
		}
		_ = tomlFile.Close()
	}

	outDeb, err := filepath.Abs(filepath.Join(debDir, fmt.Sprintf("%s_%s_%s.deb", cargo.Name, cargo.Version, arch.deb)))
	if err != nil {
		return fmt.Errorf("resolving output path: %w", err)
	}

	// Skip if already built (e.g. restored from the CI package cache).
	if _, err := os.Stat(outDeb); err == nil {
		slog.Info("cargo-deb already built, skipping", "name", cargo.Name, "arch", arch.deb)
		return nil
	}

	slog.Info("Building with zigbuild", "name", cargo.Name, "arch", arch.deb, "target", arch.rust)
	if err := runCommand(srcDir, "fakeroot", "cargo", "zigbuild", "--release", "--target", arch.rust); err != nil {
		return fmt.Errorf("cargo zigbuild: %w", err)
	}

	slog.Info("Building cargo-deb", "name", cargo.Name, "arch", arch.deb, "target", arch.rust)
	if err := runCommand(srcDir, "fakeroot", "cargo", "deb", "--no-strip", "--no-build", "--target", arch.rust, "--output", outDeb); err != nil {
		return fmt.Errorf("cargo deb: %w", err)
	}

	return nil
}

// cloneCargoSrc clones cargo.Url into dir (once) and checks out cargo.Version
// (a git ref: tag, sha, or branch). A pre-existing dir is left untouched.
func cloneCargoSrc(cargo cargoType, dir string) error {
	defer warnTime("buildCargoDeb "+cargo.Name, 60*time.Second)()

	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("creating cargo src parent: %w", err)
	}

	if err := runCommand("", "git", "clone", cargo.Url, dir); err != nil {
		return fmt.Errorf("cloning %s: %w", cargo.Url, err)
	}

	if cargo.Version != "" {
		if err := runCommand(dir, "git", "checkout", cargo.Version); err != nil {
			return fmt.Errorf("checking out ref %s: %w", cargo.Version, err)
		}
	}

	return nil
}

func warnTime(process string, warnTime time.Duration) func() {
	start := time.Now()
	return func() {
		if time.Since(start) > warnTime {
			slog.Warn("slow operation", "process", process, "duration", time.Since(start))
		}
	}
}
