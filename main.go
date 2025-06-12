package main

import (
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

type arch struct {
	deb     string
	ansible string
}

var (
	archs = []arch{
		{
			deb:     "amd64",
			ansible: "x86_64",
		},
		{
			deb:     "arm64",
			ansible: "aarch64",
		},
	}
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
	// var releaseContent string

	for _, arch := range archs {
		for _, pkg := range pkgs {
			url := strings.ReplaceAll(pkg.url, "{{ version }}", pkg.version)
			url = strings.ReplaceAll(url, "{{ deb_architecture }}", arch.deb)
			url = strings.ReplaceAll(url, "{{ ansible_architecture }}", arch.ansible)

			filename := fmt.Sprintf("%s-%s-%s.deb", pkg.name, arch.deb, pkg.version)
			log.Println("Downloading " + filename)
			err := downloadURL(filepath.Join("tmp", arch.deb), filename, url)
			if err != nil {
				log.Fatal(err)
			}
		}
	}
}
