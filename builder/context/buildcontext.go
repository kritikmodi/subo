package context

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/suborbital/atmo/directive"
	"github.com/suborbital/subo/subo/release"
	"github.com/suborbital/subo/subo/util"
	"gopkg.in/yaml.v2"
)

var dockerImageForLang = map[string]string{
	"rust":           "suborbital/builder-rs",
	"swift":          "suborbital/builder-swift",
	"assemblyscript": "suborbital/builder-as",
	"tinygo":         "suborbital/builder-tinygo",
}

// BuildContext describes the context under which the tool is being run
type BuildContext struct {
	Cwd           string
	CwdIsRunnable bool
	Runnables     []RunnableDir
	Bundle        BundleRef
	Directive     *directive.Directive
	AtmoVersion   string
	Langs         []string
}

// RunnableDir represents a directory containing a Runnable
type RunnableDir struct {
	Name           string
	UnderscoreName string
	Fullpath       string
	Runnable       *directive.Runnable
	BuildImage     string
}

// BundleRef contains information about a bundle in the current context
type BundleRef struct {
	Exists   bool
	Fullpath string
}

// ForDirectory returns the build context for the provided working directory
func ForDirectory(dir string) (*BuildContext, error) {
	fullDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get Abs path")
	}

	runnables, cwdIsRunnable, err := getRunnableDirs(fullDir)
	if err != nil {
		return nil, errors.Wrap(err, "failed to getRunnableDirs")
	}

	bundle, err := bundleTargetPath(fullDir)
	if err != nil {
		return nil, errors.Wrap(err, "failed to bundleIfExists")
	}

	directive, err := readDirectiveFile(fullDir)
	if err != nil {
		return nil, errors.Wrap(err, "failed to readDirectiveFile")
	}

	bctx := &BuildContext{
		Cwd:           fullDir,
		CwdIsRunnable: cwdIsRunnable,
		Runnables:     runnables,
		Bundle:        *bundle,
		Directive:     directive,
	}

	if directive != nil {
		bctx.AtmoVersion = directive.AtmoVersion
	}

	return bctx, nil
}

// RunnableExists returns true if the context contains a runnable with name <name>
func (b *BuildContext) RunnableExists(name string) bool {
	for _, r := range b.Runnables {
		if r.Name == name {
			return true
		}
	}

	return false
}

// SetBuildLangs sets the languages that the builder will build
// defaults to all languages
func (b *BuildContext) SetBuildLangs(langs []string) {
	b.Langs = langs
}

// ShouldBuildLang returns true if the provided language is safe-listed for building
func (b *BuildContext) ShouldBuildLang(lang string) bool {
	if len(b.Langs) == 0 {
		return true
	}

	for _, l := range b.Langs {
		if l == lang {
			return true
		}
	}

	return false
}

func (b *BuildContext) Modules() ([]os.File, error) {
	modules := []os.File{}

	for _, r := range b.Runnables {
		wasmPath := filepath.Join(r.Fullpath, fmt.Sprintf("%s.wasm", r.Name))

		file, err := os.Open(wasmPath)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to Open module file %s", wasmPath)
		}

		modules = append(modules, *file)
	}

	return modules, nil
}

func getRunnableDirs(cwd string) ([]RunnableDir, bool, error) {
	runnables := []RunnableDir{}

	// go through all of the dirs in the current dir
	topLvlFiles, err := ioutil.ReadDir(cwd)
	if err != nil {
		return nil, false, errors.Wrap(err, "failed to list directory")
	}

	// check to see if we're running from within a Runnable directory
	// and return true if so.
	runnableDir, err := getRunnableFromFiles(cwd, topLvlFiles)
	if err != nil {
		return nil, false, errors.Wrap(err, "failed to getRunnableFromFiles")
	} else if runnableDir != nil {
		runnables = append(runnables, *runnableDir)
		return runnables, true, nil
	}

	for _, tf := range topLvlFiles {
		if !tf.IsDir() {
			continue
		}

		dirPath := filepath.Join(cwd, tf.Name())

		// determine if a .runnable file exists in that dir
		innerFiles, err := ioutil.ReadDir(dirPath)
		if err != nil {
			util.LogWarn(fmt.Sprintf("couldn't read files in %v", dirPath))
			continue
		}

		runnableDir, err := getRunnableFromFiles(dirPath, innerFiles)
		if err != nil {
			return nil, false, errors.Wrap(err, "failed to getRunnableFromFiles")
		} else if runnableDir == nil {
			continue
		}

		runnables = append(runnables, *runnableDir)
	}

	return runnables, false, nil
}

// containsRunnableYaml finds any .runnable file in a list of files
func ContainsRunnableYaml(files []os.FileInfo) (string, bool) {
	for _, f := range files {
		if strings.HasPrefix(f.Name(), ".runnable.") {
			return f.Name(), true
		}
	}

	return "", false
}

func getRunnableFromFiles(wd string, files []os.FileInfo) (*RunnableDir, error) {
	filename, exists := ContainsRunnableYaml(files)
	if !exists {
		return nil, nil
	}

	runnableBytes, err := ioutil.ReadFile(filepath.Join(wd, filename))
	if err != nil {
		return nil, errors.Wrap(err, "failed to ReadFile .runnable yaml")
	}

	runnable := directive.Runnable{}
	if err := yaml.Unmarshal(runnableBytes, &runnable); err != nil {
		return nil, errors.Wrap(err, "failed to Unmarshal .runnable yaml")
	}

	if runnable.Name == "" {
		runnable.Name = filepath.Base(wd)
	}

	if runnable.Namespace == "" {
		runnable.Namespace = "default"
	}

	img := ImageForLang(runnable.Lang)
	if img == "" {
		return nil, fmt.Errorf("(%s) %s is not a valid lang", runnable.Name, runnable.Lang)
	}

	absolutePath, err := filepath.Abs(wd)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get Abs filepath")
	}

	runnableDir := &RunnableDir{
		Name:           runnable.Name,
		UnderscoreName: strings.Replace(runnable.Name, "-", "_", -1),
		Fullpath:       absolutePath,
		Runnable:       &runnable,
		BuildImage:     img,
	}

	return runnableDir, nil
}

func ImageForLang(lang string) string {
	img, ok := dockerImageForLang[lang]
	if !ok {
		return ""
	}

	return fmt.Sprintf("%s:v%s", img, release.SuboDotVersion)
}

func bundleTargetPath(cwd string) (*BundleRef, error) {
	path := filepath.Join(cwd, "runnables.wasm.zip")

	b := &BundleRef{
		Fullpath: path,
		Exists:   false,
	}

	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return b, nil
		} else {
			return nil, err
		}
	}

	b.Exists = true

	return b, nil
}
