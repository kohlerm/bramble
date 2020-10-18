// +build !race

package bramble

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxmcd/bramble/pkg/reptar"
	"github.com/maxmcd/bramble/pkg/starutil"
)

func runTwiceAndCheck(t *testing.T, cb func(t *testing.T)) {
	var err error
	hasher := NewHasher()
	dir := tmpDir()
	hasher2 := NewHasher()
	dir2 := tmpDir()
	// set a unique bramble store for these tests
	os.Setenv("BRAMBLE_PATH", dir+"/")
	hd, _ := os.UserHomeDir()
	dest := filepath.Join(dir, "var")
	_ = os.MkdirAll(dest, 0755)
	if err = cp("", filepath.Join(hd, "bramble/var/linux-binary"), dest); err != nil {
		t.Fatal(err)
	}
	cb(t)
	if err = reptar.Reptar(dir+"/store", hasher); err != nil {
		t.Error(err)
	}
	os.Setenv("BRAMBLE_PATH", dir2)
	dest2 := filepath.Join(dir2, "var")
	_ = os.MkdirAll(dest2, 0755)
	if err = cp("", filepath.Join(hd, "bramble/var/linux-binary"), dest2); err != nil {
		t.Fatal(err)
	}
	cb(t)
	if err = reptar.Reptar(dir2+"/store", hasher2); err != nil {
		t.Error(err)
	}
	if hasher.String() != hasher2.String() {
		t.Error("content doesn't match, non deterministic", dir, dir2)
		return
	}
	fmt.Println(dir, dir2)
	// _ = os.RemoveAll(dir)
	// _ = os.RemoveAll(dir2)
}

func TestIntegration(t *testing.T) {
	runTests := func(t *testing.T) {
		b := Bramble{}
		if err := b.test([]string{"../../tests"}); err != nil {
			fmt.Printf("%+v", err)
			t.Error(err)
		}
	}
	runTwiceAndCheck(t, runTests)
}

func assembleModules(t *testing.T) []string {
	modules := []string{}
	if err := filepath.Walk("../..", func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(fi.Name(), ".bramble") {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			reader := bufio.NewReader(f)
			for {
				line, err := reader.ReadString('\n')
				if err == io.EOF {
					break
				} else if err != nil {
					return err
				}
				if !strings.HasPrefix(line, "def") {
					continue
				}
				functionName := line[4:strings.Index(line, "(")]
				if strings.HasPrefix(functionName, "_") || strings.HasPrefix(functionName, "test_") {
					continue
				}
				modules = append(modules, fmt.Sprintf("%s:%s", path, functionName))
			}
			_ = f.Close()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return modules
}

func TestIntegrationRunAlmostAllPublicFunctions(t *testing.T) {
	modules := assembleModules(t)

	fmt.Println(modules)
	runTwiceAndCheck(t, func(t *testing.T) {
		for _, module := range modules {
			b := Bramble{}
			if strings.Contains(module, "lib/std") {
				continue
			}
			if !t.Run(module, func(t *testing.T) {
				if err := b.run([]string{module}); err != nil {
					t.Fatal(starutil.AnnotateError(err))
				}
			}) {
				t.Fatal(module, "failed")
			}
		}
	})
}

func TestIntegrationStarlarkBuilder(t *testing.T) {
	runTwiceAndCheck(t, func(t *testing.T) {
		b := Bramble{}
		if err := b.run([]string{"github.com/maxmcd/bramble/lib/busybox:run_busybox"}); err != nil {
			t.Fatal(starutil.AnnotateError(err))
		}
	})
}

func TestIntegrationSimple(t *testing.T) {
	runTwiceAndCheck(t, func(t *testing.T) {
		b := Bramble{}
		if err := b.run([]string{"github.com/maxmcd/bramble/tests/simple/simple:simple"}); err != nil {
			t.Fatal(starutil.AnnotateError(err))
		}
	})
}

func TestIntegrationNixSeed(t *testing.T) {
	runTwiceAndCheck(t, func(t *testing.T) {
		b := Bramble{}
		if err := b.run([]string{"github.com/maxmcd/bramble/lib/nix-seed:stdenv"}); err != nil {
			t.Fatal(starutil.AnnotateError(err))
		}
	})
}
func TestIntegrationBenchmarkFullCacheHit(t *testing.T) {
	t.Skip("don't run benchmarks")
	bramble := Bramble{}
	if err := bramble.run([]string{"../../all:all"}); err != nil {
		t.Fatal(err)
	}
	res := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			bramble := Bramble{}
			if err := bramble.run([]string{"../../all:all"}); err != nil {
				b.Fatal(err)
			}
		}
	})

	fmt.Printf("Time per run: %s\n", time.Duration(time.Nanosecond*time.Duration(res.NsPerOp())))
	fmt.Printf("Total time: %s\n", res.T)
	fmt.Println(res.Extra)
}
