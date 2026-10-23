package dstruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// PersistentStorage only accepts paths inside approved save directories.
// Canonical assets remain in dst-admin for existing manifests and backups;
// these derived copies are the files the game actually loads.
const executionDirectory = "save/mod_config_data/dst-admin/runtime"

func executionPaths() []string {
	paths := make([]string, 0, len(managedAssetNames))
	for _, name := range managedAssetNames {
		paths = append(paths, filepath.Join(executionDirectory, name))
	}
	return paths
}

func checkExecutionDirectory(worldPath string, create bool) (bool, error) {
	current := worldPath
	for _, name := range []string{"save", "mod_config_data", managedDirectory, "runtime"} {
		current = filepath.Join(current, name)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if !create {
				return false, nil
			}
			if err := os.Mkdir(current, 0750); err != nil && !os.IsExist(err) {
				return false, err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return false, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("%w: runtime execution directory", ErrUnsafeRuntimePath)
		}
	}
	return true, nil
}

// Missing derived files can be repaired explicitly. Modified files are
// protected just like canonical assets, including on upgrade.
func checkExecutionAssets(worldPath string, current manifest) (bool, error) {
	exists, err := checkExecutionDirectory(worldPath, false)
	if err != nil || !exists {
		return false, err
	}
	complete := true
	for _, name := range managedAssetNames {
		data, _, exists, err := readRegular(filepath.Join(worldPath, executionDirectory, name), maxManagedBytes)
		if err != nil {
			return false, err
		}
		if !exists {
			complete = false
			continue
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != current.Assets[name] {
			return false, fmt.Errorf("%w: execution asset %s", ErrManagedFileChanged, name)
		}
	}
	return complete, nil
}

func writeExecutionAssets(worldPath string, assets map[string][]byte) error {
	if _, err := checkExecutionDirectory(worldPath, true); err != nil {
		return err
	}
	for _, name := range managedAssetNames {
		if err := atomicWrite(filepath.Join(worldPath, executionDirectory, name), assets[name], 0640); err != nil {
			return err
		}
	}
	return nil
}

// Derive from the restored canonical files, not the manager's current version.
// This keeps schema-1 backups readable without changing their file list.
func restoredExecutionFiles(files []capturedFile) []capturedFile {
	var result []capturedFile
	for _, file := range files {
		if filepath.Dir(file.path) == managedDirectory && contains(managedAssetNames, filepath.Base(file.path)) {
			file.path = filepath.Join(executionDirectory, filepath.Base(file.path))
			result = append(result, file)
		}
	}
	return result
}
