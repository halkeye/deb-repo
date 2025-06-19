package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

	"gopkg.in/yaml.v2"
)

type pkg struct {
	url     string `yaml:"url"`
	name    string `yaml:"name"`
	version string `yaml:"version"`
}

type app struct {
	name      string `yaml:"name"`
	url       string `yaml:"url"`
	version   string `yaml:"version"`
	moveRules []struct {
		srcRegex string `yaml:"src_regex"`
		dst      string `yaml:"dst"`
		mode     int8   `yaml:"mode"`
	} `yaml:"move_rules"`
}

type arch struct {
	deb       string
	ansible   string
	vale      string
	gitAbsorb string
}

var (
	archs = []arch{
		{
			vale:      "64-bit",
			deb:       "amd64",
			ansible:   "x86_64",
			gitAbsorb: "x86_64",
		},
		{
			vale:      "arm64",
			deb:       "arm64",
			ansible:   "aarch64",
			gitAbsorb: "arm",
		},
	}
)

func downloadURL(dir string, filename string, url string, version string, a arch) error {
	tmpl, err := template.New("url").Parse(url)
	if err != nil {
		log.Fatal(err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]string{
		"version":                 version,
		"deb_architecture":        a.deb,
		"ansible_architecture":    a.ansible,
		"vale_architecture":       a.vale,
		"git_absorb_architecture": a.gitAbsorb,
	}); err != nil {
		log.Fatal(err)
	}
	url = buf.String()

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
	downloadDebs()
	downloadApps()
}

func downloadDebs() {
	pkgs := []pkg{}
	yamlFile, err := os.Open("package.yml")
	if err != nil {
		log.Fatal(err)
	}
	defer yamlFile.Close()

	yamlDecoder := yaml.NewDecoder(yamlFile)
	if err := yamlDecoder.Decode(&pkgs); err != nil {
		log.Fatal(err)
	}

	for _, arch := range archs {
		for _, pkg := range pkgs {
			filename := fmt.Sprintf("%s-%s-%s.deb", pkg.name, arch.deb, pkg.version)
			log.Println("Downloading " + filename)
			err = downloadURL(filepath.Join("tmp", arch.deb), filename, pkg.url, pkg.version, arch)
			if err != nil {
				log.Fatal(err)
			}
		}
	}
}

func downloadApps() {
	apps := []app{}
	yamlFile, err := os.Open("app.yml")
	if err != nil {
		log.Fatal(err)
	}
	defer yamlFile.Close()

	yamlDecoder := yaml.NewDecoder(yamlFile)
	if err := yamlDecoder.Decode(&apps); err != nil {
		log.Fatal(err)
	}

	for _, arch := range archs {
		for _, app := range apps {
			filename := fmt.Sprintf("%s-%s-%s.deb", app.name, arch.deb, app.version)
			log.Println("Downloading " + filename)
			err = downloadURL(filepath.Join("tmp", arch.deb), filename, app.url, app.version, arch)
			if err != nil {
				log.Fatal(err)
			}
		}
	}
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
