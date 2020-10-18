package bramble

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/pkg/errors"
)

var (
	PathPaddingCharacters = "bramble_store_padding"
	PathPaddingLength     = 50

	ErrStoreDoesNotExist = errors.New("calculated store path doesn't exist, did the location change?")
)

func NewStore() (Store, error) {
	s := Store{}
	return s, s.ensureBramblePath()
}

type Store struct {
	bramblePath string
	storePath   string
}

func (s *Store) ensureBramblePath() (err error) {
	var exists bool
	// Prefer BRAMBLE_PATH if it's set. Otherwise use the folder "bramble" in
	// the user's home directory.
	s.bramblePath, exists = os.LookupEnv("BRAMBLE_PATH")
	if !exists {
		var home string
		home, err = os.UserHomeDir()
		if err != nil {
			return errors.Wrap(err, "error searching for users home directory")
		}
		s.bramblePath = filepath.Join(home, "bramble")
	} else {
		// Ensure we clean the path so that our padding calculation is consistent.
		s.bramblePath = filepath.Clean(s.bramblePath)
	}

	// No support for relative bramble paths.
	if !filepath.IsAbs(s.bramblePath) {
		return errors.Errorf("bramble path %s must be absolute", s.bramblePath)
	}

	if !pathExists(s.bramblePath) {
		fmt.Println("bramble path directory doesn't exist, creating")
		if err = os.Mkdir(s.bramblePath, 0755); err != nil {
			return err
		}
	}

	fileMap := map[string]struct{}{}
	{
		// List all files in the bramble folder.
		files, err := ioutil.ReadDir(s.bramblePath)
		if err != nil {
			return errors.Wrap(err, "error listing files in the BRAMBLE_PATH")
		}
		for _, file := range files {
			fileMap[file.Name()] = struct{}{}
		}

		// Specifically check for files in the var folder.
		files, _ = ioutil.ReadDir(s.joinBramblePath("var"))
		if len(files) > 0 {
			for _, file := range files {
				fileMap["var/"+file.Name()] = struct{}{}
			}
		}
	}

	var storeDirectoryName string
	if storeDirectoryName, err = calculatePaddedDirectoryName(s.bramblePath, PathPaddingLength); err != nil {
		return err
	}

	s.storePath = s.joinBramblePath(storeDirectoryName)

	// Add store folder with the correct padding and add a convenience symlink
	// in the bramble folder.
	if _, ok := fileMap["store"]; !ok {
		if err = os.MkdirAll(s.storePath, 0755); err != nil {
			return err
		}
		if err = os.Symlink("."+storeDirectoryName, s.joinBramblePath("store")); err != nil {
			return err
		}
	}

	folders := []string{
		// TODO: move this to a common cache directory or somewhere else that
		// this would be expected to be
		"tmp", // Tmp folder, probably shouldn't exist.

		"var", // The var folder.

		// Metadata for config files to store recently built derivations so that
		// they're not wiped during GC
		"var/config-registry",

		// Cache for starlark file compilation.
		"var/star-cache",
	}

	for _, folder := range folders {
		if _, ok := fileMap[folder]; !ok {
			if err = os.Mkdir(s.joinBramblePath(folder), 0755); err != nil {
				return errors.Wrap(err, fmt.Sprintf("error creating bramble folder %q", folder))
			}
		}
	}

	// otherwise, check if the exact store path we need exists
	if _, err = os.Stat(s.storePath); err != nil {
		return ErrStoreDoesNotExist
	}

	return
}

func (s Store) joinStorePath(v ...string) string {
	return filepath.Join(append([]string{s.storePath}, v...)...)
}
func (s Store) joinBramblePath(v ...string) string {
	return filepath.Join(append([]string{s.bramblePath}, v...)...)
}

func (s Store) writeReader(src io.Reader, name string, validateHash string) (contentHash, path string, err error) {
	hasher := NewHasher()
	file, err := ioutil.TempFile(s.joinBramblePath("tmp"), "")
	if err != nil {
		err = errors.Wrap(err, "error creating a temporary file for a write to the store")
		return
	}
	tee := io.TeeReader(src, hasher)
	if _, err = io.Copy(file, tee); err != nil {
		err = errors.Wrap(err, "error writing to the temporary store file")
		return
	}
	fileName := hasher.String()
	if validateHash != "" && hasher.Sha256Hex() != validateHash {
		return hasher.Sha256Hex(), "", errHashMismatch
	}
	if name != "" {
		fileName += ("-" + name)
	}
	path = s.joinStorePath(fileName)
	if er := os.Rename(file.Name(), path); er != nil {
		return "", "", errors.Wrap(er, "error moving file into store")
	}
	return hasher.Sha256Hex(), path, nil
}

func (s Store) writeConfigLink(location string, derivations map[string][]string) (err error) {
	hasher := NewHasher()
	if _, err = hasher.Write([]byte(location)); err != nil {
		return
	}
	reg := s.joinBramblePath("var/config-registry")
	hash := hasher.String()
	configFileLocation := filepath.Join(reg, hash+"-metadata.toml")

	f, err := os.OpenFile(configFileLocation,
		os.O_RDWR|os.O_APPEND|os.O_CREATE,
		0644)
	if err != nil {
		return
	}
	var dm derivationMap
	if _, err = toml.DecodeReader(f, &dm); err != nil {
		return
	}
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)

	dm.Location = location
	if dm.Derivations == nil {
		dm.Derivations = map[string][]string{}
	}
	for k, v := range derivations {
		dm.Derivations[k] = v
	}
	if err = toml.NewEncoder(f).Encode(dm); err != nil {
		return
	}
	return f.Close()
}

type derivationMap struct {
	Location    string
	Derivations map[string][]string
}

func calculatePaddedDirectoryName(bramblePath string, paddingLength int) (storeDirectoryName string, err error) {
	paddingLen := paddingLength -
		len(bramblePath) - // parent folder lengths
		1 - // slash before directory
		1 // slash after the directory

	if paddingLen <= 0 {
		return "", errors.Errorf(
			"Bramble location creates a path that is too long. "+
				"Location '%s' is too long to create a directory less than %d in length",
			bramblePath, paddingLen)
	}

	paddingSize := len(PathPaddingCharacters)
	repetitions := paddingLen / (paddingSize + 1)
	extra := paddingLen % (paddingSize + 1)

	for i := 0; i < repetitions; i++ {
		storeDirectoryName += ("/" + PathPaddingCharacters)
	}
	storeDirectoryName += ("/" + PathPaddingCharacters[:extra])
	return storeDirectoryName, nil
}
