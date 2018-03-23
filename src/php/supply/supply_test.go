package supply_test

import (
	"bytes"
	"io/ioutil"
	"os"
	"path/filepath"
	"php/supply"
	"reflect"
	"syscall"

	"github.com/cloudfoundry/libbuildpack"
	"github.com/cloudfoundry/libbuildpack/ansicleaner"
	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

//go:generate mockgen -source=supply.go --destination=mocks_test.go --package=supply_test

var _ = Describe("Supply", func() {
	var (
		err          error
		buildDir     string
		cacheDir     string
		depsDir      string
		depsIdx      string
		supplier     *supply.Supplier
		logger       *libbuildpack.Logger
		buffer       *bytes.Buffer
		mockCtrl     *gomock.Controller
		mockManifest *MockManifest
		mockCommand  *MockCommand
		mockJSON     *MockJSON
	)

	BeforeEach(func() {
		buildDir, err = ioutil.TempDir("", "php-buildpack.build.")
		Expect(err).To(BeNil())
		cacheDir, err = ioutil.TempDir("", "php-buildpack.cache.")
		Expect(err).To(BeNil())
		depsDir, err = ioutil.TempDir("", "php-buildpack.deps.")
		Expect(err).To(BeNil())

		depsIdx = "9"
		Expect(os.MkdirAll(filepath.Join(depsDir, depsIdx), 0755)).To(Succeed())

		buffer = new(bytes.Buffer)

		logger = libbuildpack.NewLogger(ansicleaner.New(buffer))

		mockCtrl = gomock.NewController(GinkgoT())
		mockManifest = NewMockManifest(mockCtrl)
		mockCommand = NewMockCommand(mockCtrl)
		mockJSON = NewMockJSON(mockCtrl)

		args := []string{buildDir, cacheDir, depsDir, depsIdx}
		stager := libbuildpack.NewStager(args, logger, &libbuildpack.Manifest{})

		supplier = &supply.Supplier{
			Manifest: mockManifest,
			Stager:   stager,
			Command:  mockCommand,
			JSON:     mockJSON,
			Log:      logger,
		}
	})

	AfterEach(func() {
		mockCtrl.Finish()
		Expect(os.RemoveAll(buildDir)).To(Succeed())
		Expect(os.RemoveAll(cacheDir)).To(Succeed())
		Expect(os.RemoveAll(depsDir)).To(Succeed())
	})

	Describe("SetupPhpVersion", func() {
		BeforeEach(func() {
			mockManifest.EXPECT().AllDependencyVersions("php").
				AnyTimes().Return([]string{"1.3.5", "1.3.6", "2.3.4", "2.3.5", "3.4.5", "3.4.6", "3.4.7", "7.1.2"})
		})
		Context("no app settings files", func() {
			BeforeEach(func() {
				mockJSON.EXPECT().Load(gomock.Any(), gomock.Any()).Return(os.NewSyscallError("", syscall.ENOENT)).Times(1)
				mockManifest.EXPECT().DefaultVersion("php").Return(libbuildpack.Dependency{Name: "php", Version: "1.3.5"}, nil)
				Expect(supplier.SetupPhpVersion()).To(Succeed())
			})
			It("sets php version from default php version", func() {
				Expect(supplier.PhpVersion).To(Equal("1.3.5"))
			})
			It("does NOT emit warnings", func() {
				Expect(buffer.String()).ToNot(ContainSubstring("WARNING"))
			})
		})
		Context("app has settings files, but no requested versions in them", func() {
			BeforeEach(func() {
				mockJSON.EXPECT().Load(gomock.Any(), gomock.Any()).Return(nil).Times(1)
				mockManifest.EXPECT().DefaultVersion("php").Return(libbuildpack.Dependency{Name: "php", Version: "1.3.5"}, nil)
				Expect(supplier.SetupPhpVersion()).To(Succeed())
			})
			It("sets php version from default php version", func() {
				Expect(supplier.PhpVersion).To(Equal("1.3.5"))
			})
			It("does NOT emit warnings", func() {
				Expect(buffer.String()).ToNot(ContainSubstring("WARNING"))
			})
		})
		Context("options.json has requested version", func() {
			BeforeEach(func() {
				mockJSON.EXPECT().Load(filepath.Join(buildDir, ".bp-config", "options.json"), gomock.Any()).Do(func(string, obj interface{}) error {
					reflect.ValueOf(obj).Elem().FieldByName("Version").SetString("2.3.4")
					return nil
				})
				Expect(supplier.SetupPhpVersion()).To(Succeed())
			})
			It("sets php version", func() {
				Expect(supplier.PhpVersion).To(Equal("2.3.4"))
			})
			It("does NOT emit warnings", func() {
				Expect(buffer.String()).ToNot(ContainSubstring("WARNING"))
			})
		})
		Context("options.json has requested version of PHP_71_LATEST", func() {
			BeforeEach(func() {
				mockJSON.EXPECT().Load(filepath.Join(buildDir, ".bp-config", "options.json"), gomock.Any()).Do(func(string, obj interface{}) error {
					reflect.ValueOf(obj).Elem().FieldByName("Version").SetString("PHP_71_LATEST")
					return nil
				})
				Expect(supplier.SetupPhpVersion()).To(Succeed())
			})
			It("sets php version", func() {
				Expect(supplier.PhpVersion).To(Equal("7.1.2"))
			})
		})
		Context("composer.json has requested version", func() {
			BeforeEach(func() {
				mockJSON.EXPECT().Load(filepath.Join(buildDir, ".bp-config", "options.json"), gomock.Any()).Return(os.NewSyscallError("", syscall.ENOENT))
				supplier.ComposerPath = filepath.Join(buildDir, "composer.json")
				mockJSON.EXPECT().Load(filepath.Join(buildDir, "composer.json"), gomock.Any()).Do(func(string, obj interface{}) error {
					reflect.ValueOf(obj).Elem().FieldByName("Requires").FieldByName("Php").SetString("3.4.5")
					return nil
				})
				Expect(supplier.SetupPhpVersion()).To(Succeed())
			})
			It("sets php version", func() {
				Expect(supplier.PhpVersion).To(Equal("3.4.5"))
			})
			It("does NOT emit warnings", func() {
				Expect(buffer.String()).ToNot(ContainSubstring("WARNING"))
			})
		})
		Context("composer.json has requested version range (composer semver >=)", func() {
			BeforeEach(func() {
				mockJSON.EXPECT().Load(filepath.Join(buildDir, ".bp-config", "options.json"), gomock.Any()).Return(os.NewSyscallError("", syscall.ENOENT))
				supplier.ComposerPath = filepath.Join(buildDir, "composer.json")
				mockJSON.EXPECT().Load(filepath.Join(buildDir, "composer.json"), gomock.Any()).Do(func(string, obj interface{}) error {
					reflect.ValueOf(obj).Elem().FieldByName("Requires").FieldByName("Php").SetString(">=3.4.5")
					return nil
				})
				Expect(supplier.SetupPhpVersion()).To(Succeed())
			})
			It("sets php version", func() {
				Expect(supplier.PhpVersion).To(Equal("3.4.7"))
			})
			It("does NOT emit warnings", func() {
				Expect(buffer.String()).ToNot(ContainSubstring("WARNING"))
			})
		})
		Context("both options.json and composer.json set versions", func() {
			BeforeEach(func() {
				mockJSON.EXPECT().Load(filepath.Join(buildDir, ".bp-config", "options.json"), gomock.Any()).Do(func(string, obj interface{}) error {
					reflect.ValueOf(obj).Elem().FieldByName("Version").SetString("2.3.4")
					return nil
				})
				supplier.ComposerPath = filepath.Join(buildDir, "composer.json")
				mockJSON.EXPECT().Load(filepath.Join(buildDir, "composer.json"), gomock.Any()).Do(func(string, obj interface{}) error {
					reflect.ValueOf(obj).Elem().FieldByName("Requires").FieldByName("Php").SetString("3.4.5")
					return nil
				})
				Expect(supplier.SetupPhpVersion()).To(Succeed())
			})
			It("warns user", func() {
				Expect(buffer.String()).To(ContainSubstring("WARNING"))
				Expect(buffer.String()).To(ContainSubstring("A version of PHP has been specified in both `composer.json` and `./bp-config/options.json`."))
				Expect(buffer.String()).To(ContainSubstring("The version defined in `composer.json` will be used."))
			})
			It("chooses composer.json version", func() {
				Expect(supplier.PhpVersion).To(Equal("3.4.5"))
			})
		})
	})

	Describe("WriteConfigFiles", func() {
		BeforeEach(func() {
			supplier.PhpVersion = "7.1.1"
			Expect(os.MkdirAll(filepath.Join(depsDir, depsIdx, "php", "etc"), 0755)).To(Succeed())
		})
		It("Writes interpolated php-fpm.conf", func() {
			Expect(supplier.WriteConfigFiles()).To(Succeed())
			body, _ := ioutil.ReadFile(filepath.Join(depsDir, depsIdx, "php", "etc", "php-fpm.conf"))
			Expect(string(body)).To(ContainSubstring("pid = {{.DEPS_DIR}}/9/php/var/run/php-fpm.pid"))
		})
		It("Writes interpolated httpd.conf", func() {
			Expect(supplier.WriteConfigFiles()).To(Succeed())
			body, _ := ioutil.ReadFile(filepath.Join(depsDir, depsIdx, "httpd", "conf", "httpd.conf"))
			Expect(string(body)).To(ContainSubstring(`DocumentRoot "${HOME}/"`))
		})
		It("Writes interpolated httpd/extra/httpd-directories.conf", func() {
			Expect(supplier.WriteConfigFiles()).To(Succeed())
			body, _ := ioutil.ReadFile(filepath.Join(depsDir, depsIdx, "httpd", "conf", "extra", "httpd-directories.conf"))
			Expect(string(body)).To(ContainSubstring(`<Directory "${HOME}/">`))
		})
	})

	Describe("WriteProfileD", func() {
		It("sets PHPRC", func() {
			Expect(supplier.WriteProfileD()).To(Succeed())
			Expect(ioutil.ReadFile(filepath.Join(depsDir, depsIdx, "profile.d", "bp_env_vars.sh"))).
				To(ContainSubstring("export PHPRC=$DEPS_DIR/9/php/etc\n"))
		})
		Context("php.ini.d was supplied", func() {
			BeforeEach(func() {
				Expect(os.MkdirAll(filepath.Join(depsDir, depsIdx, "php/etc/php.ini.d"), 0755)).To(Succeed())
			})
			It("sets PHP_INI_SCAN_DIR", func() {
				Expect(supplier.WriteProfileD()).To(Succeed())
				Expect(ioutil.ReadFile(filepath.Join(depsDir, depsIdx, "profile.d", "bp_env_vars.sh"))).
					To(ContainSubstring("export PHP_INI_SCAN_DIR=$DEPS_DIR/9/php/etc/php.ini.d\n"))
			})
		})
		Context("php.ini.d was NOT supplied", func() {
			It("does NOT set PHP_INI_SCAN_DIR", func() {
				Expect(supplier.WriteProfileD()).To(Succeed())
				Expect(ioutil.ReadFile(filepath.Join(depsDir, depsIdx, "profile.d", "bp_env_vars.sh"))).
					ToNot(ContainSubstring("PHP_INI_SCAN_DIR"))
			})
		})
	})
})
