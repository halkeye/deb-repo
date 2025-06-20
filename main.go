package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ulikunitz/xz"
	"gopkg.in/yaml.v2"
)

var errUnknownExtension = errors.New("unknown extension")

type pkgType struct {
	Url     string `yaml:"url"`
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

type appType struct {
	Name          string            `yaml:"name"`
	Url           string            `yaml:"url"`
	Version       string            `yaml:"version"`
	UrlOverrides  map[string]string `yaml:"url_overrides"`
	ArchOverrides map[string]string `yaml:"arch_verrides"`
	MoveRules     []struct {
		SrcRegex string `yaml:"src_regex"`
		Dst      string `yaml:"dst"`
		Mode     int    `yaml:"mode"`
	} `yaml:"move_rules"`
}

func (pkg pkgType) BuildURL(arch archType) string {
	return ProcessURL(pkg.Url, pkg.Version, arch)
}

func (app appType) BuildURL(arch archType) string {
	if val, ok := app.ArchOverrides[arch.deb]; ok {
		arch = archType{
			deb:     val,
			ansible: val,
			vale:    val,
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
		"vale_architecture":    arch.vale,
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
	vale    string
	kubectx string
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
			vale:    "64-bit",
			kubectx: "x86_64",
			deb:     "amd64",
			ansible: "x86_64",
		},
		{
			vale:    "arm64",
			deb:     "arm64",
			kubectx: "arm64",
			ansible: "aarch64",
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download URL %s: status code %d", url, resp.StatusCode)
	}

	// Save file to directory
	file, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("failed to create file: %v", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return fmt.Errorf("failed to read response body: %v", err)
	}

	return nil
}

func main() {
	var err error
	pkgs, apps, err := loadYaml()
	if err != nil {
		log.Fatal(err)
	}

	err = downloadDebs(pkgs)
	if err != nil {
		log.Fatal(fmt.Errorf("downloadDebs: %w", err))
	}

	err = downloadApps(apps)
	if err != nil {
		log.Fatal(fmt.Errorf("downloadApps: %w", err))
	}
}

func loadYaml() ([]pkgType, []appType, error) {
	pkgs := []pkgType{}
	apps := []appType{}

	yamlFile, err := os.Open("packages.yaml")
	if err != nil {
		return pkgs, apps, fmt.Errorf("reading packages.yaml: %w", err)
	}
	defer yamlFile.Close()

	yamlDecoder := yaml.NewDecoder(yamlFile)
	if err := yamlDecoder.Decode(&pkgs); err != nil {
		return pkgs, apps, fmt.Errorf("decoding pkgs: %w", err)
	}

	yamlFile, err = os.Open("apps.yaml")
	if err != nil {
		return pkgs, apps, fmt.Errorf("reading apps.yaml: %w", err)
	}
	defer yamlFile.Close()

	yamlDecoder = yaml.NewDecoder(yamlFile)
	if err := yamlDecoder.Decode(&apps); err != nil {
		return pkgs, apps, fmt.Errorf("decoding apps: %w", err)
	}

	return pkgs, apps, nil

}

func downloadDebs(pkgs []pkgType) error {
	for _, arch := range archs {
		for _, pkg := range pkgs {
			filename := fmt.Sprintf("%s-%s-%s.deb", pkg.Name, arch.deb, pkg.Version)
			log.Println("Downloading " + filename)
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
	debDir := filepath.Join("tmp", arch.deb)

	appUrl := app.BuildURL(arch)

	filename := filepath.Base(appUrl)
	unarchiveFunc := getUnarchiveFunc(appUrl)
	if unarchiveFunc == nil {
		if strings.Contains(filename, ".") {
			return errUnknownExtension
		}
	}

	log.Println("Downloading App: " + filepath.Join(appDir, filename))

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

	if err := writeControl(debWorkDir, app.Name, app.Version, arch.deb); err != nil {
		return fmt.Errorf("writing control file: %w", err)
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

	madeChanges := false

	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("creating workDir %s: %w", workDir, err)
	}

	if err := os.MkdirAll(debWorkDir, 0o755); err != nil {
		return fmt.Errorf("creating debWorkDir %s: %w", debWorkDir, err)
	}

	// TODO flip this, walk slow fs and go through loop of rules
	for ruleIdx, rule := range app.MoveRules {
		regex, err := regexp.Compile(rule.SrcRegex)
		if err != nil {
			return fmt.Errorf("processing app %s's %d src_regex: %w", app.Name, ruleIdx, err)
		}

		err = filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if info.IsDir() {
				return nil
			}

			filenameWithoutWork := path[len(workDir)+1:]
			fmt.Printf("Processing file %s with regex %s == %t\n", filenameWithoutWork, rule.SrcRegex, regex.MatchString(filenameWithoutWork))
			if !regex.MatchString(filenameWithoutWork) {
				return nil
			}

			newFile := filepath.Join(debWorkDir, rule.Dst)
			if err := os.MkdirAll(filepath.Dir(newFile), 0o755); err != nil {
				return fmt.Errorf("processing app %s's %d mkdir: %w", app.Name, ruleIdx, err)
			}

			madeChanges = true
			err = os.Rename(path, newFile)
			if err != nil {
				return fmt.Errorf("processing app %s's %d moving file %s to %s: %w", app.Name, ruleIdx, path, newFile, err)
			}

			err = os.Chmod(newFile, os.FileMode(rule.Mode))
			if err != nil {
				return fmt.Errorf("processing app %s's %d setting permissions on file %s to %d: %w", app.Name, ruleIdx, newFile, rule.Mode, err)
			}
			return nil
		})

		if err != nil {
			return fmt.Errorf("processing app %s's %d: %w", app.Name, ruleIdx, err)
		}
	}

	if !madeChanges {
		return errors.New("no assets found")
	}
	return nil
}

func unarchive(file string, reader readerFunc, dst string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()

	archiveReader, err := reader(f)
	if err != nil {
		return err
	}

	if closeReader, ok := archiveReader.(io.ReadCloser); ok {
		defer closeReader.Close()
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
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err = io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		default:
			log.Printf("skip %s (type %c)", h.Name, h.Typeflag)
		}
	}
	return nil
}

func writeControl(dir string, name, version, arch string) error {
	debianDir := filepath.Join(dir, "DEBIAN")
	if err := os.MkdirAll(debianDir, 0755); err != nil {
		return fmt.Errorf("failed to create %s directory: %v", debianDir, err)
	}

	ctrl := fmt.Sprintf(`Package: %s
Version: %s
Architecture: %s
Maintainer: Gavin Mogan <debian@gavinmogan.com>
Section: extra
Description: %s packaged from tgz
`, name, version, arch, name)
	return os.WriteFile(filepath.Join(debianDir, "control"), []byte(ctrl), 0o644)
}

func buildDeb(dir, outDeb string) error {
	cmd := exec.Command("fakeroot", "dpkg-deb", "--build", dir, outDeb)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
