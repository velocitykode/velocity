package router

import (
	"errors"
	"io/fs"
	"net/http"
	"strings"
)

// staticIndexPage is the file http.FileServer serves for a directory,
// appended to the directory's cleaned name exactly as the file server
// appends it.
const staticIndexPage = "/index.html"

// errStaticListing is what a directory opened by noListingFS answers when
// asked for its entries.
var errStaticListing = errors.New("router: static directory listing is disabled")

// noListingFS is the file system Static and StaticFallback serve from and
// staticProbe opens through. It opens names through root and turns a
// directory that has no index.html into fs.ErrNotExist, so the probe
// reports a miss (routing answers the request) and http.FileServer, which
// lists any directory it cannot find an index for, answers 404 instead.
// A directory whose index.html is itself a directory has no index. A
// directory with an index.html opens as before and the file server serves
// the index; an index.html the server may not open keeps its error, so the
// directory answers 403 like any file it may not open.
//
// Every opened file comes back holding the FileInfo read to classify it,
// so the file server's own Stat on the handle repeats no system call.
type noListingFS struct {
	root http.FileSystem
}

// Open opens name through root, reads its FileInfo, and for a directory
// checks for an index.html before returning it. Every handle it does not
// return is closed.
func (n noListingFS) Open(name string) (http.File, error) {
	f, err := n.root.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.IsDir() {
		return statFile{File: f, info: info}, nil
	}
	if err := n.checkIndex(name); err != nil {
		_ = f.Close()
		return nil, err
	}
	return listlessDir{File: f, info: info}, nil
}

// checkIndex returns nil when the directory name holds an index.html that
// is not a directory, an error matching fs.ErrNotExist when it holds none,
// and the open or stat error otherwise. The index handle is closed before
// it returns: the file server opens the index itself.
func (n noListingFS) checkIndex(name string) error {
	idx, err := n.root.Open(strings.TrimSuffix(name, "/") + staticIndexPage)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
		return err
	}
	info, err := idx.Stat()
	_ = idx.Close()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return nil
}

// statFile is an opened file that answers Stat with the FileInfo
// noListingFS read when it opened it.
type statFile struct {
	http.File
	info fs.FileInfo
}

// Stat returns the FileInfo read at open.
func (f statFile) Stat() (fs.FileInfo, error) { return f.info, nil }

// listlessDir is an opened directory that had an index.html when it was
// opened. It answers Stat with the FileInfo read at open and refuses to
// list its entries (it has no ReadDir, and Readdir fails), so an index
// removed between noListingFS's check and the file server's own open of it
// makes the file server answer an error rather than a listing.
type listlessDir struct {
	http.File
	info fs.FileInfo
}

// Stat returns the FileInfo read at open.
func (d listlessDir) Stat() (fs.FileInfo, error) { return d.info, nil }

// Readdir refuses to list the directory.
func (listlessDir) Readdir(int) ([]fs.FileInfo, error) { return nil, errStaticListing }
