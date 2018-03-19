package finalize

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/cloudfoundry/libbuildpack"
)

type Stager interface {
	BuildDir() string
	DepDir() string
	DepsIdx() string
}

type Manifest interface {
	RootDir() string
}

type Finalizer struct {
	Manifest Manifest
	Stager   Stager
	Log      *libbuildpack.Logger
}

func (f *Finalizer) Run() error {
	f.Log.BeginStep("Finalizing php")

	if err := f.SymlinkHttpd(); err != nil {
		f.Log.Error("Error symlinking httpd: %v", err)
		return err
	}

	if err := f.WriteStartFile(); err != nil {
		f.Log.Error("Error writing start file: %v", err)
		return err
	}

	data, err := f.GenerateReleaseYaml()
	if err != nil {
		f.Log.Error("Error generating release YAML: %v", err)
		return err
	}
	return libbuildpack.NewYAML().Write("/tmp/php-buildpack-release-step.yml", data)
}

func (f *Finalizer) SymlinkHttpd() error {
	f.Log.BeginStep("Symlinking httpd into app dir")
	return os.Symlink(filepath.Join("..", "deps", f.Stager.DepsIdx(), "httpd"), filepath.Join(f.Stager.BuildDir(), "httpd"))
}

func (f *Finalizer) WriteStartFile() error {
	start := fmt.Sprintf(`#!/usr/bin/env bash
varify "$DEPS_DIR/%s/php/etc/" "$DEPS_DIR/%s/httpd/conf/"
# TODO real process management
$DEPS_DIR/%s/php/sbin/php-fpm -p "$DEPS_DIR/%s/php/etc" -y "$DEPS_DIR/%s/php/etc/php-fpm.conf" -c "$DEPS_DIR/%s/php/etc" &
$DEPS_DIR/%s/httpd/bin/apachectl -f "$DEPS_DIR/%s/httpd/conf/httpd.conf" -k start -DFOREGROUND
`, f.Stager.DepsIdx(), f.Stager.DepsIdx(), f.Stager.DepsIdx(), f.Stager.DepsIdx(), f.Stager.DepsIdx(), f.Stager.DepsIdx(), f.Stager.DepsIdx(), f.Stager.DepsIdx())
	return ioutil.WriteFile(filepath.Join(f.Stager.DepDir(), "start"), []byte(start), 0755)
}

func (f *Finalizer) GenerateReleaseYaml() (map[string]map[string]string, error) {
	return map[string]map[string]string{
		"default_process_types": {
			"web": fmt.Sprintf("$DEPS_DIR/%s/start", f.Stager.DepsIdx()),
		},
	}, nil
}
