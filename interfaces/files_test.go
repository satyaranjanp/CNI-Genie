package interfaces

import (
	"errors"
	"os"
)

type FakeIo struct {
	Files    []string
	Bytes    []byte
	BinFiles []os.FileInfo
}

func (fi *FakeIo) ReadFile(file string) ([]byte, error) {
	if file == "" {
		return errors.New("Invalid file path")
	}
	return fi.Bytes, nil
}

func (fi *FakeIo) ReadDir(dir string) ([]os.FileInfo, error) {
	if dir == "" {
		return errors.New("Invalid directory path")
	}
	return fi.BinFiles, nil
}

func (fi *FakeIo) CreateFile(filePath string, bytes []byte, perm os.FileMode) error {
	if filePath == "" {
		return errors.New("Invalid file path")
	}
	fi.Files = append(fi.Files, filePath)
	return nil
}
