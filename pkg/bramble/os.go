package bramble

import (
	"bufio"
	"fmt"
	goos "os"
	"path/filepath"

	"github.com/maxmcd/bramble/pkg/assert"
	"github.com/maxmcd/bramble/pkg/starutil"
	"github.com/pkg/errors"
	"go.starlark.net/starlark"
)

type OS struct {
	bramble *Bramble
}

var (
	_ starlark.Value    = OS{}
	_ starlark.HasAttrs = OS{}
)

func NewOS(bramble *Bramble) OS {
	return OS{bramble: bramble}
}

func (os OS) String() string        { return "<module 'os'>" }
func (os OS) Freeze()               {}
func (os OS) Type() string          { return "module" }
func (os OS) Truth() starlark.Bool  { return starlark.True }
func (os OS) Hash() (uint32, error) { return 0, starutil.ErrUnhashable("os") }
func (os OS) AttrNames() []string {
	return []string{
		"args",
		"cp",
		"error",
		"input",
		"mkdir",
		"session",
	}
}

func makeArgs() (starlark.Value, error) {
	out := []starlark.Value{}
	if len(goos.Args) >= 3 {
		for _, arg := range goos.Args[3:] {
			out = append(out, starlark.String(arg))
		}
	}
	return starlark.NewList(out), nil
}

func (os OS) Attr(name string) (val starlark.Value, err error) {
	callables := map[string]starutil.CallableFunc{
		"cp":      os.cp,
		"error":   os.error,
		"input":   os.input,
		"mkdir":   os.mkdir,
		"session": os.session,
	}
	if fn, ok := callables[name]; ok {
		return starutil.Callable{ThisName: name, ParentName: "os", Callable: fn}, nil
	}
	if name == "args" {
		return makeArgs()
	}
	return nil, nil
}

func (os OS) error(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (val starlark.Value, err error) {
	return assert.Error(thread, nil, args, kwargs)
}

func (os OS) input(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (val starlark.Value, err error) {
	// calling input before a derivation is disallowed
	os.bramble.AfterDerivation()

	reader := bufio.NewReader(goos.Stdin)
	text, err := reader.ReadString('\n')
	return starlark.String(text), err
}

func (os OS) mkdir(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (val starlark.Value, err error) {
	// calling mkdir before a derivation is disallowed
	os.bramble.AfterDerivation()
	var path starlark.String
	if err = starlark.UnpackArgs("mkdir", args, kwargs, "path", &path); err != nil {
		return
	}
	return starlark.None, goos.Mkdir(path.GoString(), 0755)
}

func (os OS) session(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (val starlark.Value, err error) {
	if err = starlark.UnpackArgs("session", args, kwargs); err != nil {
		return
	}
	return os.bramble.newSession("", nil)
}

func (os OS) cp(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (val starlark.Value, err error) {
	// calling cp before a derivation is disallowed
	os.bramble.AfterDerivation()
	if err = starlark.UnpackArgs("cp", nil, kwargs); err != nil {
		return
	}
	paths := make([]string, len(args))
	for i, arg := range args {
		str, err := starutil.ValueToString(arg)
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(str) {
			return nil, errors.New("cp doesn't support relative paths yet")
		}
		paths[i] = str
	}
	err = cp("", paths...)
	fmt.Printf("%+v", err)
	return starlark.None, err
}
