package bramble

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"

	"github.com/maxmcd/bramble/pkg/assert"
	"github.com/maxmcd/bramble/pkg/bramblecmd"
	"github.com/maxmcd/bramble/pkg/brambleos"
	"github.com/maxmcd/bramble/pkg/derivation"
	"github.com/maxmcd/bramble/pkg/starutil"
	"github.com/mitchellh/cli"
	"go.starlark.net/starlark"
)

var (
	errRequiredFunctionArgument = errors.New("bramble run takes a required positional argument \"function\"")
	errModuleDoesNotExist       = errors.New("module doesn't exist")
	errQuiet                    = errors.New("")
)

// RunCLI runs the cli with os.Args
func RunCLI() {
	c := cli.NewCLI("bramble", "0.0.1")
	c.Args = os.Args[1:]
	b := Bramble{}
	c.Commands = map[string]cli.CommandFactory{
		"run": command{
			help: `Usage: bramble run [options] [module]:<function> [args...]

  Run a function
			`,
			synopsis: "Run a bramble function",
			run:      b.run,
		}.factory(),
		"test": command{
			help:     `Usage: bramble test [path]`,
			synopsis: "Run bramble tests",
			run:      b.test,
		}.factory(),
	}
	exitStatus, err := c.Run()
	if err != nil {
		fmt.Println(err)
	}
	os.Exit(exitStatus)
}

type command struct {
	help     string
	synopsis string
	run      func(args []string) error
}

func (c command) factory() func() (cli.Command, error) {
	return func() (cli.Command, error) {
		return &c, nil
	}
}
func (c *command) Help() string     { return c.help }
func (c *command) Synopsis() string { return c.synopsis }
func (c *command) Run(args []string) int {
	if err := c.run(args); err != nil {
		if err == errQuiet {
			return 1
		}
		fmt.Print(starutil.AnnotateError(err))
		return 1
	}
	return 0
}

type Bramble struct {
	thread         *starlark.Thread
	predeclared    starlark.StringDict
	config         Config
	configLocation string
	derivation     *derivation.Function

	afterDerivation bool
}

var _ starutil.DerivationChecker = new(Bramble)

func (b *Bramble) AfterDerivation() { b.afterDerivation = true }
func (b *Bramble) CalledDerivation() error {
	if b.afterDerivation {
		return errors.New("build context is dirty, can't call derivation after cmd() or other builtins")
	}
	return nil
}

func (b *Bramble) init() (err error) {
	if b.configLocation != "" {
		return errors.New("can't initialize Bramble twice")
	}

	// ensures we have a bramble.toml in the current or parent dir
	b.config, b.configLocation, err = findConfig()
	if err != nil {
		return
	}

	b.thread = &starlark.Thread{
		Name: "main",
		Load: b.load,
	}

	// creates the derivation function and checks we have a valid bramble path and store
	b.derivation, err = derivation.NewFunction(b.thread, b)
	if err != nil {
		return
	}

	assertGlobals, err := assert.LoadAssertModule()
	if err != nil {
		return
	}

	b.predeclared = starlark.StringDict{
		"derivation": b.derivation,
		"cmd":        bramblecmd.NewFunction(),
		"os":         brambleos.NewOS(b),
		"assert":     assertGlobals["assert"],
	}

	return
}

func (b *Bramble) load(thread *starlark.Thread, module string) (globals starlark.StringDict, err error) {
	globals, err = b.resolveModule(module)
	return
}

var extension string = ".bramble"

func isTestFile(name string) bool {
	if !strings.HasSuffix(name, extension) {
		return false
	}
	nameWithoutExtension := name[:len(name)-len(extension)]
	return (strings.HasPrefix(nameWithoutExtension, "test_") ||
		strings.HasSuffix(nameWithoutExtension, "_test"))
}

func findTestFiles(path string) (testFiles []string, err error) {
	if fileExists(path) {
		return []string{path}, nil
	}
	if fileExists(path + extension) {
		return []string{path + extension}, nil
	}
	files, err := ioutil.ReadDir(path)
	if err != nil {
		return
	}
	for _, file := range files {
		name := file.Name()
		if filepath.Ext(name) != extension {
			continue
		}
		if !isTestFile(name) {
			continue
		}
		testFiles = append(testFiles, filepath.Join(path, name))
	}

	return
}

// runErrorReporter reports errors during a run. These errors are just passed up the thread
type runErrorReporter struct{}

func (e runErrorReporter) Error(err error) {}
func (e runErrorReporter) FailNow() bool   { return true }

// testErrorReporter reports errors during an individual test
type testErrorReporter struct {
	errors []error
}

func (e *testErrorReporter) Error(err error) {
	e.errors = append(e.errors, err)
}
func (e *testErrorReporter) FailNow() bool { return false }

func (b *Bramble) test(args []string) (err error) {
	failFast := true
	if err = b.init(); err != nil {
		return
	}
	location := "."
	if len(args) > 0 {
		location = args[0]
	}
	testFiles, err := findTestFiles(location)
	if err != nil {
		return errors.Wrap(err, "error finding test files")
	}
	for _, filename := range testFiles {
		globals, err := starlark.ExecFile(b.thread, filename, nil, b.predeclared)
		if err != nil {
			return err
		}
		for name, fn := range globals {
			if !strings.HasPrefix(name, "test_") {
				continue
			}
			starFn, ok := fn.(*starlark.Function)
			if !ok {
				continue
			}
			fmt.Printf("running test %q\n", name)
			errors := testErrorReporter{}
			assert.SetReporter(b.thread, &errors)
			_, err = starlark.Call(b.thread, starFn, nil, nil)
			if len(errors.errors) > 0 {
				fmt.Printf("\nGot %d errors while running %q in %q:\n", len(errors.errors), name, filename)
				for _, err := range errors.errors {
					fmt.Print(starutil.AnnotateError(err))
				}
				if failFast {
					return errQuiet
				}
			}
			if err != nil {
				return err
			}
		}
	}
	return
}

func (b *Bramble) resolveModule(module string) (globals starlark.StringDict, err error) {
	if !strings.HasPrefix(module, b.config.Module.Name) {
		// TODO: support other modules
		err = errors.Errorf("can't find module %s", module)
		return
	}

	path := module[len(b.config.Module.Name):]
	path = filepath.Join(b.configLocation, path)

	directoryWithNameExists := pathExists(path)

	var directoryHasDefaultDotBramble bool
	if directoryWithNameExists {
		directoryHasDefaultDotBramble = fileExists(path + "/default.bramble")
	}

	fileWithNameExists := fileExists(path + extension)

	switch {
	case directoryWithNameExists && directoryHasDefaultDotBramble:
		path += "/default.bramble"
	case fileWithNameExists:
		path += extension
	default:
		err = errModuleDoesNotExist
		return
	}

	return starlark.ExecFile(b.thread, path, nil, b.predeclared)
}

func (b *Bramble) run(args []string) (err error) {
	if len(args) == 0 {
		err = errRequiredFunctionArgument
		return
	}
	if err = b.init(); err != nil {
		return
	}
	assert.SetReporter(b.thread, runErrorReporter{})

	module, fn, err := b.argsToImport(args)
	if err != nil {
		return
	}
	globals, err := b.resolveModule(module)
	if err != nil {
		return
	}
	toCall, ok := globals[fn]
	if !ok {
		return errors.Errorf("global function %q not found", fn)
	}

	_, err = starlark.Call(&starlark.Thread{}, toCall, nil, nil)
	err = errors.Wrap(err, "error running")
	return
}

func (b *Bramble) moduleFromPath(path string) (module string, err error) {
	module = (b.config.Module.Name + "/" + b.relativePathFromConfig())
	if path == "" {
		return
	}
	module += "/"

	// support things like bar/main.bramble:foo
	if strings.HasSuffix(path, extension) && fileExists(path) {
		return module + path[:len(path)-len(extension)], nil
	}

	fullName := path + extension
	if !fileExists(fullName) {
		if !fileExists(path + "/default.bramble") {
			return "", errors.Errorf("can't find module at %q", path)
		}
	}
	// we found it, return
	module += filepath.Join(path)
	return
}

func (b *Bramble) relativePathFromConfig() string {
	wd, _ := os.Getwd()
	relativePath, _ := filepath.Rel(b.configLocation, wd)
	return relativePath
}

func (b *Bramble) argsToImport(args []string) (module, function string, err error) {
	if len(args) == 0 {
		return "", "", errRequiredFunctionArgument
	}

	firstArgument := args[0]
	if !strings.Contains(firstArgument, ":") {
		function = firstArgument
		module = b.config.Module.Name + "/" + b.relativePathFromConfig()
	} else {
		parts := strings.Split(firstArgument, ":")
		if len(parts) != 2 {
			return "", "", errors.New("function name has too many colons")
		}
		var path string
		path, function = parts[0], parts[1]
		module, err = b.moduleFromPath(path)
	}

	return
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !fi.IsDir()
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
