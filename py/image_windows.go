package py

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// On Windows the native image is a set of files: CPython's own python3XY.dll with the C
// runtime DLLs it imports, the standard library's extension modules (DLLs/), and the glue
// (pythago_glue.dll, linked against python3XY.dll). Init writes them to a cache directory
// and loads the glue with the Windows loader, which loads python3XY.dll beside it. Being
// a module the loader knows by name, python3XY.dll is then what every extension module
// binds to, and the DLLs they load in turn; an interpreter loaded from memory would be
// invisible to them.

type windowsImage struct {
	id    string
	files fs.FS
}

var (
	registeredFiles  *windowsImage
	imageOnce        sync.Once
	imageErr         error
	imageDir         string
	pythonDLL        string
	loadLibraryExW   = syscall.NewLazyDLL("kernel32.dll").NewProc("LoadLibraryExW")
	pythonDLLPattern = regexp.MustCompile(`^python3\d+t?\.dll$`)
)

// RegisterImageFiles records the files of a Windows native image: python3XY.dll,
// pythago_glue.dll and the DLLs they import at the root, the standard library's extension
// modules under DLLs/. id names the content (a digest); Init extracts the files once per
// id under Config.ImageCacheDir and removes the other ids' images that no running
// process uses. Generated dist packages call it from init.
func RegisterImageFiles(id string, files fs.FS) {
	registeredFiles = &windowsImage{id: id, files: files}
}

// loadImage returns the registered native image's pythago_symtab, extracting and loading
// registered image files on first use.
func loadImage() (uintptr, error) {
	if s := image.Load(); s != 0 || registeredFiles == nil {
		return s, nil
	}
	imageOnce.Do(func() { imageErr = loadImageFiles(registeredFiles) })
	return image.Load(), imageErr
}

// imageSearchPaths lists directories of the native image that go on sys.path: the
// extension modules' DLLs directory.
func imageSearchPaths() []string {
	if imageDir == "" {
		return nil
	}
	return []string{filepath.Join(imageDir, "DLLs")}
}

func loadImageFiles(img *windowsImage) error {
	dir, err := extractImage(img)
	if err != nil {
		return fmt.Errorf("py: extracting the native image: %w", err)
	}
	entries, err := fs.ReadDir(img.files, ".")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if pythonDLLPattern.MatchString(e.Name()) {
			pythonDLL = e.Name()
		}
	}
	if pythonDLL == "" {
		return errors.New("py: the native image has no python3XY.dll")
	}
	glue, err := loadLibrary(filepath.Join(dir, "pythago_glue.dll"))
	if err != nil {
		return err
	}
	symtab, err := syscall.GetProcAddress(glue, "pythago_symtab")
	if err != nil {
		return fmt.Errorf("py: pythago_glue.dll: %w", err)
	}
	python, err := syscall.LoadLibrary(filepath.Join(dir, pythonDLL)) // loaded already, by the glue
	if err != nil {
		return fmt.Errorf("py: %s: %w", pythonDLL, err)
	}
	RegisterExports(nil, func(name string) (uintptr, bool) {
		a, err := syscall.GetProcAddress(python, name)
		return a, err == nil
	})
	imageDir = dir
	RegisterImage(symtab)
	removeStaleImages(filepath.Dir(dir), img.id)
	return nil
}

// loadLibrary loads a DLL by full path, searching its own directory for the DLLs it
// imports (LOAD_WITH_ALTERED_SEARCH_PATH).
func loadLibrary(file string) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(file)
	if err != nil {
		return 0, err
	}
	const loadWithAlteredSearchPath = 0x8
	h, _, errno := loadLibraryExW.Call(uintptr(unsafe.Pointer(p)), 0, loadWithAlteredSearchPath)
	if h == 0 {
		return 0, fmt.Errorf("py: loading %s: %w", file, errno)
	}
	return syscall.Handle(h), nil
}

// Image cache layout, under Config.ImageCacheDir (default <user cache dir>\pythago):
//
//	image-<id>       a complete image; it appears by renaming a filled tmp directory, and
//	                 leaves by being renamed to a trash name, so it is never partial
//	image-<id>.lock  the in-use marker: every process using image <id> keeps it open,
//	                 sharing read and write but not delete, for its whole life
//	tmp-<id>-<rand>  an extraction in progress (or left by a crash)
//	trash-<rand>     a directory being removed
//
// Cleanup removes image <id> only after opening its lock file without sharing (with
// delete-on-close): that fails while any process holds it, and while cleanup holds it no
// process can start using the image. The marker does not rely on Windows refusing to
// delete a loaded DLL, which Wine does not refuse.
const (
	imageLockSuffix   = ".lock"
	staleTmpAge       = time.Hour
	lockBusyTimeout   = 30 * time.Second
	accessDelete      = 0x00010000        // DELETE
	flagDeleteOnClose = 0x04000000        // FILE_FLAG_DELETE_ON_CLOSE
	errSharing        = syscall.Errno(32) // ERROR_SHARING_VIOLATION
)

// imageLock is this process's open in-use marker of its image, never closed: the
// handle closes with the process.
var imageLock syscall.Handle

// imageCacheBase returns the directory holding the image directories.
func imageCacheBase() string {
	if imageCacheDir != "" {
		return imageCacheDir
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "pythago")
}

// openLock opens (creating if needed) base's lock file of image id. shared opens it
// as a user of the image (sharing read and write, not delete); otherwise it opens it
// exclusively, deleting the file when the handle is closed.
func openLock(base, id string, shared bool) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(filepath.Join(base, "image-"+id+imageLockSuffix))
	if err != nil {
		return syscall.InvalidHandle, err
	}
	if shared {
		return syscall.CreateFile(p, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
			nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	}
	return syscall.CreateFile(p, accessDelete, 0, nil, syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL|flagDeleteOnClose, 0)
}

// lockImage takes base's in-use marker of image id for the life of the process. A
// cleanup holding it exclusively (briefly) or a lock file still being deleted refuses
// the open; it retries for a while.
func lockImage(base, id string) error {
	deadline := time.Now().Add(lockBusyTimeout)
	for delay := time.Millisecond; ; delay = min(2*delay, 100*time.Millisecond) {
		h, err := openLock(base, id, true)
		if err == nil {
			imageLock = h
			return nil
		}
		if err != errSharing && err != syscall.ERROR_ACCESS_DENIED || time.Now().After(deadline) {
			return fmt.Errorf("locking the image directory: %w", err)
		}
		time.Sleep(delay)
	}
}

// extractImage writes img's files to <base>/image-<id> unless that directory exists, and
// returns it, holding the image's in-use marker. A new directory is filled under a
// temporary name and renamed into place, so processes starting together each see a
// complete image or write their own; the first rename wins.
func extractImage(img *windowsImage) (string, error) {
	base := imageCacheBase()
	dir := filepath.Join(base, "image-"+img.id)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	// Under the marker no cleanup can remove dir, so an existing dir is complete and stays.
	if err := lockImage(base, img.id); err != nil {
		return "", err
	}
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	tmp := filepath.Join(base, "tmp-"+img.id+"-"+randomSuffix())
	err := fs.WalkDir(img.files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(tmp, filepath.FromSlash(name))
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		in, err := img.files.Open(name)
		if err != nil {
			return err
		}
		defer in.Close()
		f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, in); err != nil {
			f.Close()
			return fmt.Errorf("%s: %w", path.Clean(name), err)
		}
		return f.Close()
	})
	if err == nil {
		err = os.Rename(tmp, dir)
	}
	if err != nil {
		os.RemoveAll(tmp)
		if _, statErr := os.Stat(dir); statErr == nil {
			return dir, nil // another process extracted it first
		}
		return "", err
	}
	return dir, nil
}

func randomSuffix() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// removeStaleImages removes, best effort, the images under base other than id that no
// process uses, extraction directories left for over staleTmpAge (another id's only if
// no process uses that id, so none is extracting it), and leftover trash.
func removeStaleImages(base, id string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	ids := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "image-") && strings.HasSuffix(name, imageLockSuffix) && !e.IsDir():
			ids[strings.TrimSuffix(strings.TrimPrefix(name, "image-"), imageLockSuffix)] = true
		case strings.HasPrefix(name, "image-") && e.IsDir():
			ids[strings.TrimPrefix(name, "image-")] = true
		case strings.HasPrefix(name, "trash-") && e.IsDir():
			os.RemoveAll(filepath.Join(base, name))
		case strings.HasPrefix(name, "tmp-") && e.IsDir():
			info, err := e.Info()
			i := strings.LastIndexByte(name, '-')
			if err != nil || time.Since(info.ModTime()) < staleTmpAge || i < len("tmp-") {
				continue
			}
			if tmpID := name[len("tmp-"):i]; tmpID == id {
				removeDir(base, name) // this image's: its age says no extraction runs there
			} else {
				removeUnused(base, tmpID, name)
			}
		}
	}
	delete(ids, id)
	for other := range ids {
		removeUnused(base, other, "image-"+other)
	}
}

// removeUnused removes base's entry name if no process uses image id: it holds id's lock
// file exclusively while renaming name to a trash name (no process can start using id
// meanwhile, and none sees a partly removed image), and deletes the lock file with it.
func removeUnused(base, id, name string) {
	h, err := openLock(base, id, false)
	if err != nil {
		return // in use (or not ours to remove)
	}
	trash := filepath.Join(base, "trash-"+randomSuffix())
	err = os.Rename(filepath.Join(base, name), trash)
	syscall.CloseHandle(h)
	if err == nil {
		os.RemoveAll(trash)
	}
}

// removeDir renames base's entry name to a trash name and removes it.
func removeDir(base, name string) {
	trash := filepath.Join(base, "trash-"+randomSuffix())
	if os.Rename(filepath.Join(base, name), trash) == nil {
		os.RemoveAll(trash)
	}
}
