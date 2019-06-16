// Copyright 2019 The Hugo Authors. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hugofs

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
)

func NewPathDecorator(fs afero.Fs, base string) *FilenameDecoratorFs {
	ffs := &FilenameDecoratorFs{Fs: fs}

	decorator := func(fi os.FileInfo, name string) (os.FileInfo, error) {
		path := strings.TrimPrefix(name, base)
		path = strings.TrimLeft(path, string(os.PathSeparator))
		opener := func() (afero.File, error) {
			return ffs.Open(name)
		}
		return decorateFileInfo("path-decorator", fs, opener, fi, "", path, nil), nil
	}

	ffs.decorate = decorator

	return ffs

}

// NewBasePathPathDecorator returns a new Fs that adds path information
// to the os.FileInfo provided by base.
func NewBasePathPathDecorator(base *afero.BasePathFs) *FilenameDecoratorFs {
	basePath, _ := base.RealPath("")
	basePath = strings.TrimLeft(basePath, "."+string(os.PathSeparator))

	ffs := &FilenameDecoratorFs{Fs: base}

	decorator := func(fi os.FileInfo, name string) (os.FileInfo, error) {
		filename, err := base.RealPath(name)
		if err != nil {
			return nil, &os.PathError{Op: "stat", Path: fi.Name(), Err: err}
		}

		path := strings.TrimPrefix(filename, basePath)
		opener := func() (afero.File, error) {
			return ffs.Open(name)
		}

		return decorateFileInfo("basepath-decorator", base, opener, fi, "", path, nil), nil
	}

	ffs.decorate = decorator

	return ffs
}

// NewFilenameDecorator decorates the given Fs to provide the real filename
// and an Opener func.
func NewFilenameDecorator(fs afero.Fs) *FilenameDecoratorFs {

	ffs := &FilenameDecoratorFs{Fs: fs}

	decorator := func(fi os.FileInfo, name string) (os.FileInfo, error) {
		opener := func() (afero.File, error) {
			return ffs.Open(name)
		}
		return decorateFileInfo("filename-decorator", fs, opener, fi, name, "", nil), nil
	}

	ffs.decorate = decorator
	return ffs
}

// NewCompositeDirDecorator decorates the given filesystem to make sure
// that directories is always opened by that filesystem.
func NewCompositeDirDecorator(fs afero.Fs) *FilenameDecoratorFs {

	decorator := func(fi os.FileInfo, name string) (os.FileInfo, error) {
		if !fi.IsDir() {
			return fi, nil
		}
		opener := func() (afero.File, error) {
			return fs.Open(name)
		}
		return decorateFileInfo("composite-decorator", fs, opener, fi, "", "", nil), nil
	}

	return &FilenameDecoratorFs{Fs: fs, decorate: decorator}
}

// BasePathRealFilenameFs is a thin wrapper around afero.BasePathFs that
// provides the real filename in Stat and LstatIfPossible.
type FilenameDecoratorFs struct {
	afero.Fs
	decorate func(fi os.FileInfo, filename string) (os.FileInfo, error)
}

// Stat adds path information to os.FileInfo returned from the containing
// BasePathFs.
func (fs *FilenameDecoratorFs) Stat(name string) (os.FileInfo, error) {
	fi, err := fs.Fs.Stat(name)
	if err != nil {
		return nil, err
	}

	return fs.decorate(fi, name)

}

// LstatIfPossible returns the os.FileInfo structure describing a given file.
// It attempts to use Lstat if supported or defers to the os.  In addition to
// the FileInfo, a boolean is returned telling whether Lstat was called.
func (b *FilenameDecoratorFs) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	var (
		fi  os.FileInfo
		err error
		ok  bool
	)

	if lstater, isLstater := b.Fs.(afero.Lstater); isLstater {
		fi, ok, err = lstater.LstatIfPossible(name)
	} else {
		fi, err = b.Fs.Stat(name)
	}

	if err != nil {
		return nil, false, err
	}

	fi, err = b.decorate(fi, name)

	return fi, ok, err
}

// Open opens the named file for reading.
func (fs *FilenameDecoratorFs) Open(name string) (afero.File, error) {
	f, err := fs.Fs.Open(name)

	if err != nil {
		return nil, err
	}

	return &filenameDecoratorFile{File: f, fs: fs}, nil
}

type filenameDecoratorFile struct {
	afero.File
	fs *FilenameDecoratorFs
}

// Readdir adds path information to the ps.FileInfo slice returned from
// the wrapped File.
func (l *filenameDecoratorFile) Readdir(c int) (ofi []os.FileInfo, err error) {
	fis, err := l.File.Readdir(c)
	if err != nil {
		return nil, err
	}

	fisp := make([]os.FileInfo, len(fis))

	for i, fi := range fis {
		filename := filepath.Join(l.Name(), fi.Name())
		fi, err = l.fs.decorate(fi, filename)
		if err != nil {
			return nil, err
		}
		fisp[i] = fi
	}

	return fisp, err
}
