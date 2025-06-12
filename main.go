package main

import (
	"crypto/md5"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type pkg struct {
	url     string
	name    string
	version string
}

var (
	pkgs = []pkg{
		{
			url:     "https://github.com/ajeetdsouza/zoxide/releases/download/v{{ version }}/zoxide_{{ version }}-1_{{ deb_architecture }}.deb",
			name:    "zoxide",
			version: "0.9.8", // repo: ajeetdsouza/zoxide
		},
		{
			url:     "https://github.com/lsd-rs/lsd/releases/download/v{{ version }}/lsd_{{ version }}_{{ deb_architecture }}.deb",
			name:    "lsd",
			version: "1.1.5", // repo: lsd-rs/lsd
		},
		{
			url:     "https://github.com/cli/cli/releases/download/v{{ version }}/gh_{{ version }}_linux_{{ deb_architecture }}.deb",
			name:    "gh",
			version: "2.74.1", // repo: cli/cli
		},
		{
			url:     "https://github.com/sharkdp/fd/releases/download/v{{ version }}/fd_{{ version }}_{{ deb_architecture }}.deb",
			name:    "fd",
			version: "10.2.0", // repo: sharkdp/fd
		},
		{
			url:     "https://github.com/aymanbagabas/shcopy/releases/download/v{{ version }}/shcopy_{{ version }}_linux_{{ deb_architecture }}.deb",
			name:    "shcopy",
			version: "0.1.5", // repo: aymanbagabas/shcopy
		},
		{
			url:     "https://github.com/humanlogio/humanlog/releases/download/v{{ version }}/humanlog_{{ version }}_linux_{{ deb_architecture }}.deb",
			name:    "humanlog",
			version: "0.7.8", // repo: humanlogio/humanlog"
		},
		{
			url:     "https://github.com/getsops/sops/releases/download/v{{ version }}/sops_{{ version }}_{{ deb_architecture }}.deb",
			name:    "sops",
			version: "3.10.2", // repo: getsops/sops
		},
		{
			url:     "https://github.com/sharkdp/vivid/releases/download/v{{ version }}/vivid_{{ version }}_{{ deb_architecture }}.deb",
			name:    "vivid",
			version: "0.10.1", // repo: sharkdp/vivid
		},
		{
			url:     "https://github.com/wagoodman/dive/releases/download/v{{ version }}/dive_{{ version }}_linux_{{ deb_architecture }}.deb",
			name:    "dive",
			version: "0.13.1", // repo: wagoodman/dive
		},
		{
			url:     "https://github.com/goreleaser/goreleaser/releases/download/v{{ version }}/goreleaser_{{ version }}_{{ deb_architecture }}.deb",
			name:    "goreleaser",
			version: "2.10.2", // repo: goreleaser/goreleaser
		},
		{
			url:     "https://github.com/ms-jpq/sad/releases/download/v{{ version }}/{{ ansible_architecture }}-unknown-linux-gnu.deb",
			name:    "sad",
			version: "0.4.32", // repo: ms-jpq/sad
		},
		{
			url:     "https://github.com/dandavison/delta/releases/download/{{ version }}/git-delta_{{ version }}_{{ deb_architecture }}.deb",
			name:    "delta",
			version: "0.18.2", // repo: dandavison/delta
		},
		{
			url:     "https://gitlab.com/gitlab-org/cli/-/releases/v{{ version }}/downloads/glab_{{ version }}_linux_{{ deb_architecture }}.deb",
			name:    "glab",
			version: "1.59.2", // gitlab_repo: gitlab-org/cli
		},
		{
			url:     "https://dl.min.io/client/mc/release/linux-{{ deb_architecture }}/archive/mcli_{{ version }}.0.0_{{ deb_architecture }}.deb",
			name:    "mc",
			version: "20250416181326", // repo: minio/mc,
		},
		{
			url:     "https://github.com/go-task/task/releases/download/v{{ version }}/task_linux_{{ deb_architecture }}.deb",
			name:    "task",
			version: "3.44.0", // repo: go-task/task
		},
	}
)

func downloadAndCalculateMD5(url string) (string, error) {
	h := md5.New()

	// Create tmp directory if it doesn't exist
	if err := os.MkdirAll("tmp", 0755); err != nil {
		return "", fmt.Errorf("failed to create tmp directory: %v", err)
	}

	// Get filename from URL
	filename := filepath.Base(url)
	filepath := filepath.Join("tmp", filename)

	if _, err := os.Stat(filepath); err != nil {
		// Download and save file
		resp, err := http.Get(url)
		if err != nil {
			return "", fmt.Errorf("failed to download URL: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("failed to download URL %s: status code %d", url, resp.StatusCode)
		}

		// Save file to tmp directory
		file, err := os.Create(filepath)
		if err != nil {
			return "", fmt.Errorf("failed to create file: %v", err)
		}
		defer file.Close()

		// Calculate MD5 while copying to file
		multiWriter := io.MultiWriter(file, h)
		if _, err := io.Copy(multiWriter, resp.Body); err != nil {
			return "", fmt.Errorf("failed to read response body: %v", err)
		}
	} else {
		// File already exists, calculate MD5
		file, err := os.Open(filepath)
		if err != nil {
			return "", fmt.Errorf("failed to open file: %v", err)
		}
		defer file.Close()

		if _, err := io.Copy(h, file); err != nil {
			return "", fmt.Errorf("failed to read file: %v", err)
		}
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func main() {
	var releaseContent string

	debArchitecture := "amd64"
	ansibleArchitecture := "x86_64"
	for _, pkg := range pkgs {
		url := strings.ReplaceAll(pkg.url, "{{ version }}", pkg.version)
		url = strings.ReplaceAll(url, "{{ deb_architecture }}", debArchitecture)
		url = strings.ReplaceAll(url, "{{ ansible_architecture }}", ansibleArchitecture)

		log.Println("Downloading " + pkg.name)
		md5Sum, err := downloadAndCalculateMD5(url)
		if err != nil {
			log.Fatal(err)
		}
		log.Println("MD5: " + md5Sum)

		releaseContent += fmt.Sprintf(`Package: %s
Version: %s
Architecture: %s
URL: %s

Archive: halkeye
Component: main
Origin: halkeye 
Label: halkeye 
Codename: halkeye
Acquire-By-Hash: yes 
Date: $DATE 
Description: local dpkg repo 
MD5Sum:
%s
`,
			pkg.name, pkg.version, debArchitecture, url, md5Sum)
	}

	// Write to release file
	releaseFile := filepath.Join("tmp", "Packages")
	if err := os.WriteFile(releaseFile, []byte(releaseContent), 0644); err != nil {
		log.Fatal(err)
	}

	// Run command when done
	// pkg-scanpackages . | xz -9 > Packages.xz
	// cmd := exec.Command("apt-ftparchive", "generate", "-c", "/etc/apt/apt.conf.d/50deb-repo")
	// if err := cmd.Run(); err != nil {
	// 	log.Fatal(err)
	// }
}
