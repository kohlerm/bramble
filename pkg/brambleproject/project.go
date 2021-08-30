package brambleproject

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/maxmcd/bramble/pkg/bramblebuild"
	"github.com/maxmcd/bramble/pkg/fileutil"
	"github.com/maxmcd/bramble/pkg/logger"
	"github.com/pkg/errors"
	"go.starlark.net/starlark"
)

const BrambleExtension = ".bramble"

var (
	ErrNotInProject = errors.New("couldn't find a bramble.toml file in this directory or any parent")
)

type Project struct {
	config   Config
	Location string

	wd string

	lockFile     LockFile
	lockFileLock sync.Mutex
}

type Config struct {
	Module ConfigModule `toml:"module"`
}
type ConfigModule struct {
	Name string `toml:"name"`
}

func NewProject(wd string) (p *Project, err error) {
	absWD, err := filepath.Abs(wd)
	if err != nil {
		return nil, errors.Wrapf(err, "can't convert relative working directory path %q to absolute path", wd)
	}
	found, location := findConfig(absWD)
	if !found {
		return nil, ErrNotInProject
	}
	p = &Project{
		Location: location,
		wd:       absWD,
	}
	bDotToml := filepath.Join(location, "bramble.toml")
	f, err := os.Open(bDotToml)
	if err != nil {
		return nil, errors.Wrapf(err, "error loading %q", bDotToml)
	}
	defer f.Close()
	if _, err = toml.DecodeReader(f, &p.config); err != nil {
		return nil, errors.Wrapf(err, "error decoding %q", bDotToml)
	}
	lockFile := filepath.Join(location, "bramble.lock")
	if !fileutil.FileExists(lockFile) {
		// Don't read the lockfile if we don't have one
		return p, nil
	}
	f, err = os.Open(lockFile)
	if err != nil {
		return nil, errors.Wrapf(err, "error opening lockfile %q", lockFile)
	}
	defer f.Close()
	_, err = toml.DecodeReader(f, &p.lockFile)
	return p, errors.Wrapf(err, "error decoding lockfile %q", lockFile)
}

func findConfig(wd string) (found bool, location string) {
	if o, _ := filepath.Abs(wd); o != "" {
		wd = o
	}
	for {
		if fileutil.FileExists(filepath.Join(wd, "bramble.toml")) {
			return true, wd
		}
		if wd == filepath.Join(wd, "..") {
			return false, ""
		}
		wd = filepath.Join(wd, "..")
	}
}

type LockFile struct {
	URLHashes map[string]string
}

func (p *Project) AddURLHashToLockfile(url, hash string) (err error) {
	p.lockFileLock.Lock()
	defer p.lockFileLock.Unlock()

	f, err := os.OpenFile(filepath.Join(p.Location, "bramble.lock"),
		os.O_RDWR|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	lf := LockFile{
		URLHashes: map[string]string{},
	}
	if _, err = toml.DecodeReader(f, &lf); err != nil {
		return
	}
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	if v, ok := lf.URLHashes[url]; ok && v != hash {
		return errors.Errorf("found existing hash for %q with value %q not %q, not sure how to proceed", url, v, hash)
	}
	lf.URLHashes[url] = hash

	return toml.NewEncoder(f).Encode(&lf)
}

func (p *Project) FilepathToModuleName(path string) (module string, err error) {
	if !strings.HasSuffix(path, BrambleExtension) {
		return "", errors.Errorf("path %q is not a bramblefile", path)
	}
	if !fileutil.FileExists(path) {
		return "", errors.Wrap(os.ErrNotExist, path)
	}
	rel, err := filepath.Rel(p.Location, path)
	if err != nil {
		return "", errors.Wrapf(err, "%q is not relative to the project directory %q", path, p.Location)
	}
	if strings.HasSuffix(path, "default"+BrambleExtension) {
		rel = strings.TrimSuffix(rel, "default"+BrambleExtension)
	} else {
		rel = strings.TrimSuffix(rel, BrambleExtension)
	}
	rel = strings.TrimSuffix(rel, "/")
	return p.config.Module.Name + "/" + rel, nil
}

func findBrambleFiles(path string) (brambleFiles []string, err error) {
	if fileutil.FileExists(path) {
		return []string{path}, nil
	}
	if fileutil.FileExists(path + BrambleExtension) {
		return []string{path + BrambleExtension}, nil
	}
	return brambleFiles, filepath.Walk(path, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if filepath.Ext(fi.Name()) != BrambleExtension {
			return nil
		}
		brambleFiles = append(brambleFiles, path)
		return nil
	})
}

func (b *Project) parseModuleFuncArgument(args []string) (module, function string, err error) {
	if len(args) == 0 {
		logger.Print(`"bramble build" requires 1 argument`)
		return "", "", flag.ErrHelp
	}

	firstArgument := args[0]
	lastIndex := strings.LastIndex(firstArgument, ":")
	if lastIndex < 0 {
		logger.Print("module and function argument is not properly formatted")
		return "", "", flag.ErrHelp
	}

	path, function := firstArgument[:lastIndex], firstArgument[lastIndex+1:]
	module, err = b.moduleFromPath(path)
	return
}

func findAllDerivationsInProject(loc string) (derivations []*Derivation, err error) {
	project, err := NewProject(loc)
	if err != nil {
		return nil, err
	}

	store, err := bramblebuild.NewStore("")
	if err != nil {
		return nil, err
	}
	rt, err := NewRuntime(project, store)
	if err != nil {
		return nil, err
	}
	if err := filepath.Walk(project.Location, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// TODO: ignore .git, ignore .gitignore?
		if strings.HasSuffix(path, ".bramble") {
			module, err := project.FilepathToModuleName(path)
			if err != nil {
				return err
			}
			globals, err := rt.resolveModule(module)
			if err != nil {
				return err
			}
			for name, v := range globals {
				if fn, ok := v.(*starlark.Function); ok {
					if fn.NumParams()+fn.NumKwonlyParams() > 0 {
						continue
					}
					fn.NumParams()
					value, err := starlark.Call(rt.thread, fn, nil, nil)
					if err != nil {
						return errors.Wrapf(err, "calling %q in %s", name, path)
					}
					derivations = append(derivations, valuesToDerivations(value)...)
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return
}
