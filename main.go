package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v2"
)

type pkg struct {
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

type archType struct {
	deb     string
	ansible string
	vale    string
	kubectx string
}

var (
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

func downloadURL(dir string, filename string, url string, version string, a archType) error {
	values := map[string]string{
		"version":              version,
		"deb_architecture":     a.deb,
		"ansible_architecture": a.ansible,
		"vale_architecture":    a.vale,
		"kubectx_architecture": a.kubectx,
	}

	url = templateRe.ReplaceAllStringFunc(url, func(s string) string {
		varName := templateRe.FindStringSubmatch(s)[1]
		if val, ok := values[varName]; ok {
			return val
		}
		return s
	})

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

	err = downloadApps(apps)
	if err != nil {
		log.Fatal(fmt.Errorf("downloadApps: %w", err))
	}
	err = downloadDebs(pkgs)
	if err != nil {
		log.Fatal(fmt.Errorf("downloadDebs: %w", err))
	}
}

func loadYaml() ([]pkg, []appType, error) {
	pkgs := []pkg{}
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

func downloadDebs(pkgs []pkg) error {
	for _, arch := range archs {
		for _, pkg := range pkgs {
			filename := fmt.Sprintf("%s-%s-%s.deb", pkg.Name, arch.deb, pkg.Version)
			log.Println("Downloading " + filename)
			err := downloadURL(filepath.Join("tmp", arch.deb), filename, pkg.Url, pkg.Version, arch)
			if err != nil {
				return fmt.Errorf("downloading deb %s: %w", filename, err)
			}
		}
	}
	return nil
}

func downloadApps(apps []appType) error {
	for _, arch := range archs {
		for _, app := range apps {
			var err error
			workDir := filepath.Join("tmp", "app", app.Name, arch.deb)

			if strings.HasSuffix(app.Url, ".tgz") || strings.HasSuffix(app.Url, ".tar.gz") {
				filename := fmt.Sprintf("%s-%s-%s.%s", app.Name, arch.deb, app.Version, "tgz")
				log.Println("Downloading App: " + filename)

				if val, ok := app.ArchOverrides[arch.deb]; ok {
					arch = archType{
						deb:     val,
						ansible: val,
					}
				}

				if val, ok := app.UrlOverrides[arch.deb]; ok {
					err = downloadURL(workDir, filename, val, app.Version, arch)
				} else {
					err = downloadURL(workDir, filename, app.Url, app.Version, arch)
				}
				if err != nil {
					return fmt.Errorf("downloading app %s: %w", app.Name, err)
				}

				err = untar(filepath.Join(workDir, filename), filepath.Join(workDir, "work"))
				if err != nil {
					return fmt.Errorf("extracting %s - %s - %s: %w", app.Name, app.Url, filename, err)
				}

				err = processApp(app, workDir)
				if err != nil {
					return fmt.Errorf("processing app %s - %s - %s: %w", app.Name, app.Url, filename, err)
				}
			} else if strings.HasSuffix(app.Url, ".txz") || strings.HasSuffix(app.Url, ".tar.xz") {
				// do nothing for now
			} else if filepath.Ext(app.Url) == "" {
				// do nothing for now
			} else {
				return fmt.Errorf("not sure what to do with ext %s", app.Url)
			}
		}
	}
	return nil
}

func processApp(app appType, workDir string) error {
	for ruleIdx, rule := range app.MoveRules {
		regex, err := regexp.Compile(rule.SrcRegex)
		if err != nil {
			return fmt.Errorf("processing app %s's %d src_regex: %w", app.Name, ruleIdx, err)
		}

		debWorkDir := filepath.Join(workDir, "deb")
		if err := os.MkdirAll(debWorkDir, 0o755); err != nil {
			return fmt.Errorf("creating debWorkDir %s's %d mkdir: %w", app.Name, ruleIdx, err)
		}

		filepath.Walk(filepath.Join(workDir, "work"), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			filenameWithoutWork := path[len(workDir)+len("work/")+1:]
			// fmt.Printf("Processing file %s with regex %s == %t\n", filenameWithoutWork, rule.SrcRegex, regex.MatchString(filenameWithoutWork))
			if !regex.MatchString(filenameWithoutWork) {
				return nil
			}
			newFile := filepath.Join(debWorkDir, rule.Dst)
			if err := os.MkdirAll(filepath.Dir(newFile), 0o755); err != nil {
				return fmt.Errorf("processing app %s's %d mkdir: %w", app.Name, ruleIdx, err)
			}
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
	return nil
}

func untar(tgz, dst string) error {
	f, err := os.Open(tgz)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
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
	ctrl := fmt.Sprintf(`Package: %s
Version: %s
Architecture: %s
Maintainer: you <you@example.com>
Description: %s packaged from tgz
`, name, version, arch, name)
	return os.WriteFile(filepath.Join(dir, "DEBIAN", "control"), []byte(ctrl), 0o644)
}

func buildDeb(workDir, outDeb string) error {
	cmd := exec.Command("dpkg-deb", "--build", workDir, outDeb)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func main2() {
	if len(os.Args) != 5 {
		fmt.Fprintf(os.Stderr, "usage: %s <file.tgz> <name> <version> <arch>\n", os.Args[0])
		os.Exit(1)
	}
	tgz, name, version, arch := os.Args[1], os.Args[2], os.Args[3], os.Args[4]

	work := "work"
	dataDir := filepath.Join(work, "data")
	if err := os.RemoveAll(work); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "DEBIAN"), 0o755); err != nil {
		log.Fatal(err)
	}

	if err := untar(tgz, dataDir); err != nil {
		log.Fatal(err)
	}

	// TODO: move/rename files inside dataDir as needed here

	if err := writeControl(work, name, version, arch); err != nil {
		log.Fatal(err)
	}

	outDeb := fmt.Sprintf("%s_%s_%s.deb", name, version, arch)
	if err := buildDeb(work, outDeb); err != nil {
		log.Fatal(err)
	}

	fmt.Println("Created", outDeb)
}
