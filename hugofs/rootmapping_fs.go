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
	"os"
	"path/filepath"
	"strings"

	"github.com/gohugoio/hugo/modules"

	"github.com/pkg/errors"

	radix "github.com/hashicorp/go-immutable-radix"
	"github.com/spf13/afero"
)

var filepathSeparator = string(filepath.Separator)

// NewRootMappingFs creates a new RootMappingFs on top of the provided with
// of root mappings with some optional metadata about the root.
// Note that From represents a virtual root that maps to the actual filename in To.
func NewRootMappingFs(fs afero.Fs, rms ...RootMapping) (*RootMappingFs, error) {
	rootMapToReal := radix.New().Txn()

	for _, rm := range rms {
		(&rm).clean()

		fromBase := modules.ResolveComponentFolder(rm.From)
		if fromBase == "" {
			panic("TODO(bep) unrecognised base: " + rm.From) // TODO(bep) mod
		}

		if len(rm.To) < 2 {
			panic(fmt.Sprintf("invalid root mapping; from/to: %s/%s", rm.From, rm.To))
		}

		rm.fromBase = fromBase
		key := []byte(rm.rootKey())
		var mappings []RootMapping
		v, found := rootMapToReal.Get(key)
		if found {
			// There may be more than one language pointing to the same root.
			mappings = v.([]RootMapping)
		}
		mappings = append(mappings, rm)
		rootMapToReal.Insert(key, mappings)
	}

	if rfs, ok := fs.(*afero.BasePathFs); ok {
		fs = NewBasePathPathDecorator(rfs)
	}

	rfs := &RootMappingFs{Fs: fs,
		virtualRoots:  rms,
		rootMapToReal: rootMapToReal.Commit().Root()}

	return rfs, nil
}

// NewRootMappingFsFromFromTo is a convenicence variant of NewRootMappingFs taking
// From and To as string pairs.
func NewRootMappingFsFromFromTo(fs afero.Fs, fromTo ...string) (*RootMappingFs, error) {
	rms := make([]RootMapping, len(fromTo)/2)
	for i, j := 0, 0; j < len(fromTo); i, j = i+1, j+2 {
		rms[i] = RootMapping{
			From: fromTo[j],
			To:   fromTo[j+1],
		}

	}

	return NewRootMappingFs(fs, rms...)
}

type RootMapping struct {
	From     string
	fromBase string // content, i18n, layouts...
	To       string

	// File metadata (lang etc.)
	Meta FileMeta
}

func (rm *RootMapping) clean() {
	rm.From = filepath.Clean(rm.From)
	rm.To = filepath.Clean(rm.To)
}

func (r RootMapping) filename(name string) string {
	return filepath.Join(r.To, strings.TrimPrefix(name, r.From))
}

func (r RootMapping) path(name string) string {
	// TODO(bep) mods consider adding fromBase or similar to the root mapping.
	return strings.TrimPrefix(strings.TrimPrefix(name, r.fromBase), filepathSeparator)
}

func (r RootMapping) rootKey() string {
	return r.From
}

// A RootMappingFs maps several roots into one. Note that the root of this filesystem
// is directories only, and they will be returned in Readdir and Readdirnames
// in the order given.
type RootMappingFs struct {
	afero.Fs
	rootMapToReal *radix.Node
	virtualRoots  []RootMapping
}

func (fs *RootMappingFs) Dirs(base string) ([]FileMetaInfo, error) {
	roots := fs.getRootsWithPrefix(base)

	if roots == nil {
		return nil, nil
	}

	bfs := afero.NewBasePathFs(fs, base)

	var ms []FileMetaInfo
	for _, r := range roots {
		filename := r.filename("")
		fi, err := lstatIfPossible(fs.Fs, filename)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}

		meta := FileMeta{
			metaKeyOpener: func() (afero.File, error) {
				return fs.Open(r.From)
			},
			metaKeyFs:       bfs,
			metaKeyFilename: filename,
		}

		mergeFileMeta(r.Meta, meta)

		ms = append(ms, NewFileMetaInfo(fi, meta))
	}

	return ms, nil
}

// LstatIfPossible returns the os.FileInfo structure describing a given file.
// It attempts to use Lstat if supported or defers to the os.  In addition to
// the FileInfo, a boolean is returned telling whether Lstat was called.
func (fs *RootMappingFs) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	if fs.isRoot(name) {
		opener := func() (afero.File, error) {
			return &rootMappingFile{name: name, fs: fs}, nil
		}
		return newDirNameOnlyFileInfo(name, true, opener), false, nil
	}

	root, found, err := fs.getRoot(name)
	if err != nil {
		return nil, false, err
	}

	if !found {
		return nil, false, os.ErrNotExist
	}

	filename := root.filename(name)

	var b bool
	var fi os.FileInfo

	if ls, ok := fs.Fs.(afero.Lstater); ok {
		fi, b, err = ls.LstatIfPossible(filename)
		if err != nil {

			return nil, b, err
		}

	} else {
		fi, err = fs.Fs.Stat(filename)
		if err != nil {
			return nil, b, err
		}
	}

	// TODO(bep) mod check this vs base
	opener := func() (afero.File, error) {
		return fs.Fs.Open(filename)
	}

	path := root.path(name)

	return decorateFileInfo("rootmapping-fs", fs.Fs, opener, fi, filename, path, root.Meta), b, nil
}

// Open opens the named file for reading.
func (fs *RootMappingFs) Open(name string) (afero.File, error) {
	if fs.isRoot(name) {
		return &rootMappingFile{name: name, fs: fs}, nil
	}
	root, found, err := fs.getRoot(name)
	if err != nil {
		return nil, err
	}

	if !found {
		return nil, os.ErrNotExist
	}

	filename := root.filename(name)
	f, err := fs.Fs.Open(filename)
	if err != nil {
		return nil, err
	}
	return &rootMappingFile{File: f, name: name, fs: fs, rm: root}, nil
}

// Stat returns the os.FileInfo structure describing a given file.  If there is
// an error, it will be of type *os.PathError.
func (fs *RootMappingFs) Stat(name string) (os.FileInfo, error) {
	fi, _, err := fs.LstatIfPossible(name)
	return fi, err

}

func (fs *RootMappingFs) isRoot(name string) bool {
	return name == "" || name == filepathSeparator

}

func (fs *RootMappingFs) getRoot(name string) (RootMapping, bool, error) {
	roots := fs.getRoots(name)
	if len(roots) == 0 {
		return RootMapping{}, false, nil
	}
	if len(roots) > 1 {
		return RootMapping{}, false, errors.Errorf("got %d matches", len(roots))
	}

	return roots[0], true, nil
}

func (fs *RootMappingFs) getRoots(name string) []RootMapping {
	nameb := []byte(filepath.Clean(name))
	_, v, found := fs.rootMapToReal.LongestPrefix(nameb)
	if !found {
		return nil
	}

	return v.([]RootMapping)
}

func (fs *RootMappingFs) getRootsWithPrefix(prefix string) []RootMapping {
	prefixb := []byte(filepath.Clean(prefix))
	var roots []RootMapping

	fs.rootMapToReal.WalkPrefix(prefixb, func(b []byte, v interface{}) bool {
		roots = append(roots, v.([]RootMapping)...)
		return false
	})

	return roots
}

func (fs *RootMappingFs) hasPrefixPlural(prefix string) bool {
	if fs.isRoot(prefix) {
		return true
	}
	prefixb := []byte(filepath.Clean(prefix))
	var counter int

	fs.rootMapToReal.WalkPrefix(prefixb, func(b []byte, v interface{}) bool {
		counter++
		return counter > 1
	})

	return counter > 1
}

type rootMappingFile struct {
	afero.File
	fs   *RootMappingFs
	name string
	rm   RootMapping
}

func (f *rootMappingFile) Close() error {
	if f.File == nil {
		return nil
	}
	return f.File.Close()
}

func (f *rootMappingFile) Name() string {
	return f.name
}

func (f *rootMappingFile) Readdir(count int) ([]os.FileInfo, error) {

	if f.fs.isRoot(f.name) {
		dirsn := make([]os.FileInfo, 0)
		for i := 0; i < len(f.fs.virtualRoots); i++ {
			if count != -1 && i >= count {
				break
			}

			rm := f.fs.virtualRoots[i]

			opener := func() (afero.File, error) {
				return f.fs.Open(rm.From)
			}

			fi := newDirNameOnlyFileInfo(rm.From, false, opener)
			if rm.Meta != nil {
				mergeFileMeta(rm.Meta, fi.Meta())
			}
			dirsn = append(dirsn, fi)
		}
		return dirsn, nil
	}

	if f.File == nil {
		panic(fmt.Sprintf("no File for %s", f.name))
	}

	fis, err := f.File.Readdir(count)
	if err != nil {
		return nil, err
	}

	for i, fi := range fis {
		fis[i] = decorateFileInfo("rootmapping-file", f.fs.Fs, nil, fi, "", f.rm.path(filepath.Join(f.Name(), fi.Name())), f.rm.Meta)
	}

	return fis, nil

}

func (f *rootMappingFile) Readdirnames(count int) ([]string, error) {
	dirs, err := f.Readdir(count)
	if err != nil {
		return nil, err
	}
	dirss := make([]string, len(dirs))
	for i, d := range dirs {
		dirss[i] = d.Name()
	}
	return dirss, nil
}
