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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/errors"

	"github.com/spf13/afero"
)

var (
	_ afero.Fs      = (*SliceFs)(nil)
	_ afero.Lstater = (*SliceFs)(nil)
	_ afero.File    = (*sliceDir)(nil)
)

func NewLanguageFs(langs map[string]bool, sources ...FileMeta) (afero.Fs, error) {
	if len(sources) == 0 {
		return NoOpFs, nil
	}

	for _, source := range sources {
		if source.Fs() == nil {
			return nil, errors.New("missing source Fs")
		}

		if source.Lang() == "" {
			return nil, errors.New("missing source lang")
		}
	}

	applyMeta := func(fs *SliceFs, source FileMeta, name string, fis []os.FileInfo) []os.FileInfo {
		fisn := make([]os.FileInfo, len(fis))
		for i, fi := range fis {
			if fi.IsDir() {
				filename := filepath.Join(name, fi.Name())
				fisn[i] = decorateFileInfo(fs, fs.getOpener(filename), fi, "", "", nil)
				continue
			}

			lang := source.Lang()
			fileLang, translationBaseName := langInfoFrom(langs, fi.Name())
			weight := 0
			if fileLang != "" {
				weight = 1
				if fileLang == lang {
					// Give priority to myfile.sv.txt inside the sv filesystem.
					weight++
				}
				lang = fileLang
			}

			fim := NewFileMetaInfo(fi, FileMeta{
				metaKeyLang:           lang,
				metaKeyWeight:         weight,
				"translationBaseName": translationBaseName,
			})

			fisn[i] = fim

		}

		return fisn
	}

	// TODO(bep) mod simplify: Use weight as one of the sort keys in walker
	filterDuplicates := func(fis []os.FileInfo) []os.FileInfo {
		type idxWeight struct {
			idx    int
			weight int
		}

		keep := make(map[string]idxWeight)

		for i, fi := range fis {
			if fi.IsDir() {
				continue
			}
			meta := fi.(FileMetaInfo).Meta()
			weight := meta.GetInt("weight")
			if weight > 0 {
				name := fi.Name()
				k, found := keep[name]
				if !found || weight > k.weight {
					keep[name] = idxWeight{
						idx:    i,
						weight: weight,
					}
				}
			}
		}

		if len(keep) > 0 {
			toRemove := make(map[int]bool)
			for i, fi := range fis {
				if fi.IsDir() {
					continue
				}
				k, found := keep[fi.Name()]
				if found && i != k.idx {
					toRemove[i] = true
				}
			}

			filtered := fis[:0]
			for i, fi := range fis {
				if !toRemove[i] {
					filtered = append(filtered, fi)
				}
			}
			fis = filtered
		}

		return fis
	}

	return &SliceFs{
		filesystems: sources,
		applyMeta:   applyMeta,
		filterDir:   filterDuplicates,
	}, nil

}

func ToFileMetas(fis ...FileMetaInfo) []FileMeta {
	fms := make([]FileMeta, len(fis))
	for i, v := range fis {
		if v == nil {
			panic("nil: FileMetaInfo")
		}
		fms[i] = v.Meta()
	}
	return fms
}

func NewSliceFs(sources ...FileMeta) (afero.Fs, error) {
	if len(sources) == 0 {
		return NoOpFs, nil
	}

	applyMeta := func(fs *SliceFs, source FileMeta, name string, fis []os.FileInfo) []os.FileInfo {
		fisn := make([]os.FileInfo, len(fis))
		for i, fi := range fis {
			if fi.IsDir() {
				// Make sure any dir is opened by us.
				filename := filepath.Join(name, fi.Name())
				fisn[i] = decorateFileInfo(fs, fs.getOpener(filename), fi, "", "", nil)
				continue
			} else {
				fisn[i] = fi
			}
		}
		return fisn
	}

	filterDir := func(fis []os.FileInfo) []os.FileInfo {
		return fis
	}

	return &SliceFs{
		filesystems: sources,
		applyMeta:   applyMeta,
		filterDir:   filterDir,
	}, nil
}

// SliceFs is an ordered composite filesystem.
type SliceFs struct {
	filesystems []FileMeta

	// Allows to filter duplicates etc. from any Readdir operation.
	filterDir func(fis []os.FileInfo) []os.FileInfo

	// Apply metadata to the result of any Readdir operation.
	applyMeta func(fs *SliceFs, source FileMeta, name string, fis []os.FileInfo) []os.FileInfo
}

func (fs *SliceFs) Chmod(n string, m os.FileMode) error {
	return syscall.EPERM
}

func (fs *SliceFs) Chtimes(n string, a, m time.Time) error {
	return syscall.EPERM
}

// TODO(bep) mod lstat
func (fs *SliceFs) LstatIfPossible(name string) (os.FileInfo, bool, error) {

	fi, _, err := fs.pickFirst(name)

	if err != nil {
		return nil, false, err
	}

	if fi.IsDir() {
		return decorateFileInfo(fs, fs.getOpener(name), fi, "", "", nil), false, nil
	}

	return nil, false, errors.Errorf("lstat: files not supported: %q", name)

}

func (fs *SliceFs) Mkdir(n string, p os.FileMode) error {
	return syscall.EPERM
}

func (fs *SliceFs) MkdirAll(n string, p os.FileMode) error {
	return syscall.EPERM
}

func (fs *SliceFs) Name() string {
	return "WeightedFileSystem"
}

func (fs *SliceFs) Open(name string) (afero.File, error) {
	fi, idx, err := fs.pickFirst(name)
	if err != nil {
		return nil, err
	}

	if !fi.IsDir() {
		panic("currently only dirs in here")
	}

	return &sliceDir{
		lfs:     fs,
		idx:     idx,
		dirname: name,
	}, nil

}

func (fs *SliceFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	panic("not implemented")
}

func (fs *SliceFs) ReadDir(name string) ([]os.FileInfo, error) {
	panic("not implemented")
}

func (fs *SliceFs) Remove(n string) error {
	return syscall.EPERM
}

func (fs *SliceFs) RemoveAll(p string) error {
	return syscall.EPERM
}

func (fs *SliceFs) Rename(o, n string) error {
	return syscall.EPERM
}

func (fs *SliceFs) Stat(name string) (os.FileInfo, error) {
	fi, _, err := fs.LstatIfPossible(name)
	return fi, err
}

func (fs *SliceFs) Create(n string) (afero.File, error) {
	return nil, syscall.EPERM
}

func (fs *SliceFs) getOpener(name string) func() (afero.File, error) {
	return func() (afero.File, error) {
		name = strings.TrimPrefix(name, "/") // TODO(bep) mod
		return fs.Open(name)
	}
}

func (fs *SliceFs) pickFirst(name string) (os.FileInfo, int, error) {
	for i, mfs := range fs.filesystems {
		fs := mfs.Fs()
		fi, err := fs.Stat(name)
		if err == nil {
			// Gotta match!
			return fi, i, nil
		}

		if !os.IsNotExist(err) {
			// Real error
			return nil, -1, err
		}
	}

	// Not found
	return nil, -1, os.ErrNotExist
}

func (fs *SliceFs) readDirs(name string, startIdx, count int) ([]os.FileInfo, error) {

	collect := func(lfs FileMeta) ([]os.FileInfo, error) {
		d, err := lfs.Fs().Open(name)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, err
			}
			return nil, nil
		} else {
			defer d.Close()
			dirs, err := d.Readdir(-1)
			if err != nil {
				return nil, err
			}
			return fs.applyMeta(fs, lfs, name, dirs), nil
		}
	}

	var dirs []os.FileInfo

	for i := startIdx; i < len(fs.filesystems); i++ {
		mfs := fs.filesystems[i]

		fis, err := collect(mfs)
		if err != nil {
			return nil, err
		}

		dirs = append(dirs, fis...)
		if count > 0 && len(dirs) >= count {
			return dirs[:count], nil
		}
	}

	return fs.filterDir(dirs), nil

}

type sliceDir struct {
	lfs     *SliceFs
	idx     int
	dirname string
}

func (f *sliceDir) Close() error {
	return nil
}

func (f *sliceDir) Name() string {
	panic("not implemented")
}

func (f *sliceDir) Read(p []byte) (n int, err error) {
	panic("not implemented")
}

func (f *sliceDir) ReadAt(p []byte, off int64) (n int, err error) {
	panic("not implemented")
}

func (f *sliceDir) Readdir(count int) ([]os.FileInfo, error) {
	return f.lfs.readDirs(f.dirname, f.idx, count)
}

func (f *sliceDir) Readdirnames(count int) ([]string, error) {
	dirsi, err := f.Readdir(count)
	if err != nil {
		return nil, err
	}

	dirs := make([]string, len(dirsi))
	for i, d := range dirsi {
		dirs[i] = d.Name()
	}
	return dirs, nil
}

func (f *sliceDir) Seek(offset int64, whence int) (int64, error) {
	panic("not implemented")
}

func (f *sliceDir) Stat() (os.FileInfo, error) {
	panic("not implemented")
}

func (f *sliceDir) Sync() error {
	panic("not implemented")
}

func (f *sliceDir) Truncate(size int64) error {
	panic("not implemented")
}

func (f *sliceDir) Write(p []byte) (n int, err error) {
	panic("not implemented")
}

func (f *sliceDir) WriteAt(p []byte, off int64) (n int, err error) {
	panic("not implemented")
}

func (f *sliceDir) WriteString(s string) (ret int, err error) {
	panic("not implemented")
}

func decorateFileInfo(
	fs afero.Fs,
	opener func() (afero.File, error),
	fi os.FileInfo,
	filename,
	path string,
	inMeta FileMeta) os.FileInfo {

	path = strings.TrimPrefix(path, string(os.PathSeparator))

	var meta FileMeta

	if fim, ok := fi.(FileMetaInfo); ok {
		meta = fim.Meta()
		if fn, ok := meta[metaKeyFilename]; ok {
			filename = fn.(string)
		}
	} else {
		meta = make(FileMeta)
		fim := NewFileMetaInfo(fi, meta)
		fi = fim

	}

	if opener != nil {
		meta[metaKeyOpener] = opener
	}

	if fi.IsDir() && fs != nil { // TODO(bep) mod check + argument
		meta.setIfNotZero(metaKeyFs, fs)
	}

	meta.setIfNotZero(metaKeyPath, path)
	meta.setIfNotZero(metaKeyFilename, filename)

	mergeFileMeta(inMeta, meta)

	return fi

}

// Try to extract the language from the given filename.
// Any valid language identificator in the name will win over the
// language set on the file system, e.g. "mypost.en.md".
func langInfoFrom(languages map[string]bool, name string) (string, string) {
	var lang string

	baseName := filepath.Base(name)
	ext := filepath.Ext(baseName)
	translationBaseName := baseName

	if ext != "" {
		translationBaseName = strings.TrimSuffix(translationBaseName, ext)
	}

	fileLangExt := filepath.Ext(translationBaseName)
	fileLang := strings.TrimPrefix(fileLangExt, ".")

	if languages[fileLang] {
		lang = fileLang
		translationBaseName = strings.TrimSuffix(translationBaseName, fileLangExt)
	}

	return lang, translationBaseName

}

func printFs(fs afero.Fs, path string, w io.Writer) {
	if fs == nil {
		return
	}
	afero.Walk(fs, path, func(path string, info os.FileInfo, err error) error {
		fmt.Println("p:::", path)
		return nil
	})
}
