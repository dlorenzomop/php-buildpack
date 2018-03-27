package supply

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	rice "github.com/GeertJohan/go.rice"
	"github.com/cloudfoundry/libbuildpack"
	"github.com/kr/text"
)

//go:generate rice embed-go

type Stager interface {
	BuildDir() string
	CacheDir() string
	DepDir() string
	DepsDir() string
	DepsIdx() string
	LinkDirectoryInDepDir(string, string) error
	WriteProfileD(string, string) error
}

type Manifest interface {
	AllDependencyVersions(string) []string
	DefaultVersion(string) (libbuildpack.Dependency, error)
	FetchDependency(libbuildpack.Dependency, string) error
	InstallDependency(libbuildpack.Dependency, string) error
	InstallOnlyVersion(string, string) error
	RootDir() string
}

type Command interface {
	Output(dir string, program string, args ...string) (string, error)
	Run(cmd *exec.Cmd) error
}
type JSON interface {
	Load(file string, obj interface{}) error
}

type Supplier struct {
	Manifest       Manifest
	Stager         Stager
	Command        Command
	Log            *libbuildpack.Logger
	JSON           JSON
	PhpVersion     string
	ComposerPath   string
	PhpExtensions  []string
	ZendExtensions []string
	WebDir         string
}

func (s *Supplier) Run() error {
	s.Log.BeginStep("Supplying php")

	if err := s.FindComposer(); err != nil {
		return fmt.Errorf("Initialiizing: composer: %s", err)
	}
	if err := s.SetupPhpVersion(); err != nil {
		return fmt.Errorf("Initialiizing: php version: %s", err)
	}
	if err := s.SetupExtensions(); err != nil {
		return fmt.Errorf("Initialiizing: extensions: %s", err)
	}

	if err := s.InstallHTTPD(); err != nil {
		return fmt.Errorf("Installing HTTPD: %s", err)
	}
	if err := s.InstallPHP(); err != nil {
		return fmt.Errorf("Installing PHP: %s", err)
	}
	if err := s.WriteConfigFiles(); err != nil {
		s.Log.Error("Error writing config files: %s", err)
		return err
	}

	if s.ComposerPath != "" {
		if err := s.InstallComposer(); err != nil {
			s.Log.Error("Failed to install composer: %s", err)
			return err
		}
		if err := s.RunComposer(); err != nil {
			s.Log.Error("Failed to run composer: %s", err)
			return err
		}
	}

	if err := s.InstallVarify(); err != nil {
		s.Log.Error("Failed to copy verify: %s", err)
		return err
	}
	if err := s.WriteProfileD(); err != nil {
		s.Log.Error("Failed to write profile.d: %s", err)
		return err
	}

	if err := s.WriteStartFile(); err != nil {
		s.Log.Error("Error writing start file: %v", err)
		return err
	}

	return nil
}

func (s *Supplier) FindComposer() error {
	if found, err := libbuildpack.FileExists(filepath.Join(s.Stager.BuildDir(), "composer.json")); err != nil {
		return err
	} else if found {
		s.Log.Debug("Found composer in build dir")
		s.ComposerPath = filepath.Join(s.Stager.BuildDir(), "composer.json")
	}

	// TODO webdir ??

	s.Log.Debug("COMPOSER_PATH: %s", os.Getenv("COMPOSER_PATH"))
	if os.Getenv("COMPOSER_PATH") != "" {
		if found, err := libbuildpack.FileExists(filepath.Join(s.Stager.BuildDir(), os.Getenv("COMPOSER_PATH"), "composer.json")); err != nil {
			return err
		} else if found {
			s.Log.Debug("Found composer in COMPOSER_PATH")
			s.ComposerPath = filepath.Join(s.Stager.BuildDir(), os.Getenv("COMPOSER_PATH"), "composer.json")
		}
	}

	return nil
}

func (s *Supplier) SetupPhpVersion() error {
	// .bp-config/options.json
	var options struct {
		Version string `json:"PHP_VERSION"`
	}
	if err := s.JSON.Load(filepath.Join(s.Stager.BuildDir(), ".bp-config", "options.json"), &options); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		s.Log.Debug("File Not Exist: %s", filepath.Join(s.Stager.BuildDir(), ".bp-config", "options.json"))
	} else if options.Version != "" {
		s.Log.Debug("PHP Version from options.json: %s", options.Version)
		m := regexp.MustCompile(`PHP_(\d)(\d)_LATEST`).FindStringSubmatch(options.Version)
		if len(m) == 3 {
			s.PhpVersion = fmt.Sprintf("%s.%s.x", m[1], m[2])
			s.Log.Debug("PHP Version interpolated: %s", s.PhpVersion)
		} else {
			s.PhpVersion = options.Version
		}
	}

	if s.ComposerPath != "" {
		var composer struct {
			Requires struct {
				Php string `json:"php"`
			} `json:"require"`
		}
		if err := s.JSON.Load(s.ComposerPath, &composer); err != nil {
			return err
		} else if composer.Requires.Php != "" {
			if s.PhpVersion != "" {
				s.Log.Warning("A version of PHP has been specified in both `composer.json` and `./bp-config/options.json`.\nThe version defined in `composer.json` will be used.")
			}
			s.PhpVersion = strings.Replace(composer.Requires.Php, ">=", "~>", -1)
			s.Log.Debug("PHP Version from composer.json: %s", s.PhpVersion)
		}
	}

	if s.PhpVersion != "" {
		versions := s.Manifest.AllDependencyVersions("php")
		if v, err := libbuildpack.FindMatchingVersion(s.PhpVersion, versions); err != nil {
			// TODO or should we blow up
			s.Log.Warning("PHP version %s not available, using default version.\n            In future versions of the buildpack, specifying a non-existent PHP version will cause staging to fail.\n            See: http://docs.cloudfoundry.org/buildpacks/php/gsg-php-composer.html", s.PhpVersion)
			s.PhpVersion = ""
		} else {
			s.PhpVersion = v
			s.Log.Debug("PHP Version interpolated: %s", s.PhpVersion)
		}
	}

	if s.PhpVersion == "" {
		if dep, err := s.Manifest.DefaultVersion("php"); err != nil {
			return err
		} else {
			s.PhpVersion = dep.Version
			s.Log.Debug("PHP Version Default: %s", s.PhpVersion)
		}
	}

	return nil
}

func (s *Supplier) SetupExtensions() error {
	s.PhpExtensions = []string{"bz2", "zlib", "curl", "mcrypt", "openssl"}
	s.ZendExtensions = []string{}
	s.WebDir = ""

	var options map[string]interface{}
	if err := s.JSON.Load(filepath.Join(s.Stager.BuildDir(), ".bp-config", "options.json"), &options); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		s.Log.Debug("File Not Exist: %s", filepath.Join(s.Stager.BuildDir(), ".bp-config", "options.json"))
	} else {
		if arr, ok := options["PHP_EXTENSIONS"].([]interface{}); ok {
			// TODO why implement deprecated feature?
			s.Log.Warning("PHP_EXTENSIONS in options.json is deprecated.")
			s.PhpExtensions = []string{}
			for _, val := range arr {
				if ext, ok := val.(string); ok {
					s.PhpExtensions = append(s.PhpExtensions, ext)
				}
			}
			s.Log.Debug("Found php extensions in options.json: %v", s.PhpExtensions)
		}
		if arr, ok := options["ZEND_EXTENSIONS"].([]interface{}); ok {
			// TODO warning as above?
			s.ZendExtensions = []string{}
			for _, val := range arr {
				if ext, ok := val.(string); ok {
					s.ZendExtensions = append(s.ZendExtensions, ext)
				}
			}
			s.Log.Debug("Found zend extensions in options.json: %v", s.ZendExtensions)
		}

		if val, ok := options["WEBDIR"].(string); ok {
			s.WebDir = val
		}
	}

	if s.ComposerPath != "" {
		if err := s.JSON.Load(s.ComposerPath, &options); err != nil {
			return err
		} else {
			s.Log.Debug("composer.json: %+v", options)
			if requires, ok := options["require"].(map[string]interface{}); ok {
				s.Log.Debug("composer.json->require: %+v", options)
				// TODO should this remove the defaults? appears to me that it should NOT
				// s.PhpExtensions = []string{}
				// TODO does the value mean something? version?
				// TODO does composer.json have zend extensions?
				// TODO document change to NOT testing if extenion available
				for k, _ := range requires {
					if strings.HasPrefix(k, "ext-") {
						s.PhpExtensions = append(s.PhpExtensions, k[4:])
					}
				}
				s.Log.Debug("Found php extensions in composer.json: %v", s.PhpExtensions)
			}
		}
	}
	return nil
}

func (s *Supplier) InstallHTTPD() error {
	if err := s.Manifest.InstallOnlyVersion("httpd", s.Stager.DepDir()); err != nil {
		return err
	}
	for _, dir := range []string{"bin", "lib"} {
		if err := s.Stager.LinkDirectoryInDepDir(filepath.Join(s.Stager.DepDir(), "httpd", dir), dir); err != nil {
			return err
		}
	}
	// convert name of binary in apachectl
	s.Log.Debug("Rewrite references in apachectl from '/app/httpd/' to '$DEPS_DIR/0/httpd/'")
	txt, err := ioutil.ReadFile(filepath.Join(s.Stager.DepDir(), "httpd/bin/apachectl"))
	if err != nil {
		return err
	}
	txt = bytes.Replace(txt, []byte(`HTTPD='/app/httpd/bin/httpd'`), []byte(`HTTPD="/app/httpd/bin/httpd"`), -1)
	txt = bytes.Replace(txt, []byte("/app/httpd/"), []byte(fmt.Sprintf("$DEPS_DIR/%s/httpd/", s.Stager.DepsIdx())), -1)
	return ioutil.WriteFile(filepath.Join(s.Stager.DepDir(), "httpd/bin/apachectl"), txt, 0755)
}

func (s *Supplier) InstallPHP() error {
	dep := libbuildpack.Dependency{Name: "php", Version: s.PhpVersion}
	if err := s.Manifest.InstallDependency(dep, s.Stager.DepDir()); err != nil {
		return err
	}
	for _, dir := range []string{"bin", "lib"} {
		if err := s.Stager.LinkDirectoryInDepDir(filepath.Join(s.Stager.DepDir(), "php", dir), dir); err != nil {
			return err
		}
	}
	return nil
}

func (s *Supplier) WriteConfigFiles() error {
	s.Log.BeginStep("Write config files")

	ctxRun := map[string]string{
		"DepsIdx":           s.Stager.DepsIdx(),
		"PhpFpmConfInclude": "",
		"PhpFpmListen":      "127.0.0.1:9000",
		"Webdir":            s.WebDir,
		"HOME":              "{{.HOME}}",
		"DEPS_DIR":          "{{.DEPS_DIR}}",
		"TMPDIR":            "{{.TMPDIR}}",
		"Libdir":            "lib", // TODO shift to deps dir (autoload is in lib/vendor)
		"PhpExtensions":     "",
		"ZendExtensions":    "",
	}

	for _, ext := range s.PhpExtensions {
		ctxRun["PhpExtensions"] = ctxRun["PhpExtensions"] + "extension=" + ext + ".so\n"
	}
	s.Log.Debug("PhpExtensions: %s", ctxRun["PhpExtensions"])
	for _, ext := range s.ZendExtensions {
		ctxRun["ZendExtensions"] = ctxRun["ZendExtensions"] + "zend_extension=" + ext + "\n"
	}
	s.Log.Debug("ZendExtensions: %s", ctxRun["ZendExtensions"])

	ctxStage := make(map[string]string)
	for k, v := range ctxRun {
		ctxStage[k] = v
	}
	ctxStage["DEPS_DIR"] = s.Stager.DepsDir()
	ctxStage["HOME"] = s.Stager.BuildDir()
	ctxStage["TMPDIR"] = "/tmp"
	// TODO remove ; except it appears to be necessary????
	ctxStage["PhpExtensions"] = ctxRun["PhpExtensions"] + "extension=openssl.so\n"

	handler := func(src, dest string, readAll func(string) ([]byte, error)) func(path string, info os.FileInfo, err error) error {
		return func(path string, info os.FileInfo, err error) error {
			if info.IsDir() {
				return nil
			}
			s.Log.Debug("WriteConfigFile: %s", path)
			destFile, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			templateBytes, err := readAll(filepath.Join(src, destFile))
			if err != nil {
				return err
			}
			templateString := string(templateBytes)
			templateString = strings.Replace(templateString, "@{DEPS_DIR}", "{{.DEPS_DIR}}", -1)
			templateString = strings.Replace(templateString, "@{TMPDIR}", "{{.TMPDIR}}", -1)
			templateString = strings.Replace(templateString, "@{HOME}", "{{.HOME}}", -1)
			templateString = strings.Replace(templateString, "#PHP_FPM_LISTEN", "{{.PhpFpmListen}}", -1)
			tmplMessage, err := template.New(filepath.Join(src, destFile)).Parse(templateString)
			if err != nil {
				return err
			}

			for basedir, ctx := range map[string]map[string]string{s.Stager.DepDir(): ctxRun, "/tmp/php_etc": ctxStage} {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(basedir, dest, destFile)), 0755); err != nil {
					return err
				}
				fh, err := os.Create(filepath.Join(basedir, dest, destFile))
				if err != nil {
					return err
				}
				defer fh.Close()
				if err := tmplMessage.Execute(fh, ctx); err != nil {
					return err
				}
			}
			return nil
		}
	}

	phpVersionLine := versionLine(s.PhpVersion)
	s.Log.Debug("PHP VersionLine: %s", phpVersionLine)
	box := rice.MustFindBox("config")
	for src, dest := range map[string]string{fmt.Sprintf("php/%s", phpVersionLine): "php/etc/", "httpd": "httpd/conf"} {
		if err := box.Walk(src, handler(src, dest, box.Bytes)); err != nil {
			return err
		}
	}
	for src, dest := range map[string]string{filepath.Join(s.Stager.BuildDir(), ".bp-config", "php"): "php/etc/", filepath.Join(s.Stager.BuildDir(), ".bp-config", "httpd"): "httpd/conf"} {
		if found, err := libbuildpack.FileExists(src); err != nil {
			return err
		} else if found {
			if err := filepath.Walk(src, handler(src, dest, ioutil.ReadFile)); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *Supplier) InstallComposer() error {
	depVersions := s.Manifest.AllDependencyVersions("composer")
	if len(depVersions) != 1 {
		return fmt.Errorf("expected 1 version of composer, found %d", len(depVersions))
	}
	s.Log.BeginStep("Installing composer %s", depVersions[0])
	dep := libbuildpack.Dependency{Name: "composer", Version: depVersions[0]}
	return s.Manifest.FetchDependency(dep, filepath.Join(s.Stager.DepDir(), "bin", "composer"))
}

func (s *Supplier) RunComposer() error {
	s.Log.BeginStep("Running composer")

	env := append(
		os.Environ(),
		fmt.Sprintf("COMPOSER_CACHE_DIR=%s/composer", s.Stager.CacheDir()),
		fmt.Sprintf("COMPOSER_VENDOR_DIR=%s/lib/vendor", s.Stager.BuildDir()),
		fmt.Sprintf("COMPOSER_BIN_DIR=%s/php/bin", s.Stager.DepDir()),
		"PHPRC=/tmp/php_etc/php/etc",
		"TMPDIR=/tmp",
	)
	if s.ComposerPath != "" {
		env = append(env, "COMPOSER="+s.ComposerPath)
	}

	cmd := exec.Command("php", filepath.Join(s.Stager.DepDir(), "bin", "composer"), "install", "--no-progress", "--no-interaction", "--no-dev")
	cmd.Env = env
	cmd.Dir = s.Stager.BuildDir()
	cmd.Stdout = text.NewIndentWriter(os.Stdout, []byte("       "))
	cmd.Stderr = text.NewIndentWriter(os.Stderr, []byte("       "))
	return s.Command.Run(cmd)
}

func (s *Supplier) InstallVarify() error {
	s.Log.Debug("Installing Varify")

	if exists, err := libbuildpack.FileExists(filepath.Join(s.Stager.DepDir(), "bin", "varify")); err != nil {
		return err
	} else if exists {
		// in unbuilt mode 'bin/supply' builds varify into the correct location
		return nil
	}

	return libbuildpack.CopyFile(filepath.Join(s.Manifest.RootDir(), "bin", "varify"), filepath.Join(s.Stager.DepDir(), "bin", "varify"))
}

func (s *Supplier) WriteProfileD() error {
	s.Log.BeginStep("Writing profile.d script")

	script := fmt.Sprintf("export PHPRC=$DEPS_DIR/%s/php/etc\n", s.Stager.DepsIdx())
	script = script + "export HTTPD_SERVER_ADMIN=admin@localhost\n"
	if found, err := libbuildpack.FileExists(filepath.Join(s.Stager.DepDir(), "php/etc/php.ini.d")); err != nil {
		return err
	} else if found {
		script = script + fmt.Sprintf("export PHP_INI_SCAN_DIR=$DEPS_DIR/%s/php/etc/php.ini.d\n", s.Stager.DepsIdx())
	}

	script = script + fmt.Sprintf(`varify "$DEPS_DIR/%s/php/etc/" "$DEPS_DIR/%s/httpd/conf/"`, s.Stager.DepsIdx(), s.Stager.DepsIdx()) + "\n"

	return s.Stager.WriteProfileD("bp_env_vars.sh", script)
}

func (s *Supplier) WriteStartFile() error {
	s.Log.BeginStep("Writing start script (php_buildpack_start)")

	start := fmt.Sprintf(`#!/usr/bin/env bash
# TODO real process management
$DEPS_DIR/%s/php/sbin/php-fpm -p "$DEPS_DIR/%s/php/etc" -y "$DEPS_DIR/%s/php/etc/php-fpm.conf" -c "$DEPS_DIR/%s/php/etc" &
$DEPS_DIR/%s/httpd/bin/apachectl -f "$DEPS_DIR/%s/httpd/conf/httpd.conf" -k start -DFOREGROUND
`, s.Stager.DepsIdx(), s.Stager.DepsIdx(), s.Stager.DepsIdx(), s.Stager.DepsIdx(), s.Stager.DepsIdx(), s.Stager.DepsIdx(), s.Stager.DepsIdx(), s.Stager.DepsIdx())
	return ioutil.WriteFile(filepath.Join(s.Stager.DepDir(), "bin", "php_buildpack_start"), []byte(start), 0755)
}

func versionLine(v string) string {
	vs := strings.Split(v, ".")
	vs[len(vs)-1] = "x"
	return strings.Join(vs, ".")
}