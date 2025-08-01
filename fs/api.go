// Copyright 2019 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package fs provides infrastructure to build tree-organized filesystems.
//
// # Structure of a file system implementation
//
// To create a file system, you should first define types for the
// nodes of the file system tree.
//
//	type myNode struct {
//		fs.Inode
//	}
//
//	// Node types must be InodeEmbedders
//	var _ = (fs.InodeEmbedder)((*myNode)(nil))
//
//	// Node types should implement some file system operations, eg. Lookup
//	var _ = (fs.NodeLookuper)((*myNode)(nil))
//
//	func (n *myNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
//		ops := myNode{}
//		out.Mode = 0755
//		out.Size = 42
//		return n.NewInode(ctx, &ops, fs.StableAttr{Mode: syscall.S_IFREG}), 0
//	}
//
// The method names are inspired on the system call names, so we have
// Listxattr rather than ListXAttr.
//
// the file system is mounted by calling mount on the root of the tree,
//
//	server, err := fs.Mount("/tmp/mnt", &myNode{}, &fs.Options{})
//	..
//	// start serving the file system
//	server.Wait()
//
// # Error handling
//
// All error reporting must use the syscall.Errno type. This is an
// integer with predefined error codes, where the value 0 (`OK`)
// should be used to indicate success.
//
// # File system concepts
//
// The FUSE API is very similar to Linux' internal VFS API for
// defining file systems in the kernel. It is therefore useful to
// understand some terminology.
//
// File content: the raw bytes that we store inside regular files.
//
// Path: a /-separated string path that describes location of a node
// in the file system tree. For example
//
//	dir1/file
//
// describes path root → dir1 → file.
//
// There can be several paths leading from tree root to a particular node,
// known as hard-linking, for example
//
//	  root
//	  /  \
//	dir1 dir2
//	  \  /
//	  file
//
// Inode: ("index node") points to the file content, and stores
// metadata (size, timestamps) about a file or directory. Each
// inode has a type (directory, symlink, regular file, etc.) and
// an identity (a 64-bit number, unique to the file
// system). Directories can have children.
//
// The inode in the kernel is represented in Go-FUSE as the Inode
// type.
//
// While common OS APIs are phrased in terms of paths (strings), the
// precise semantics of a file system are better described in terms of
// Inodes. This allows us to specify what happens in corner cases,
// such as writing data to deleted files.
//
// File descriptor: a handle returned to opening a file. File
// descriptors always refer to a single inode.
//
// Dentry: a dirent maps (parent inode number, name string) tuple to
// child inode, thus representing a parent/child relation (or the
// absense thereof). Dentries do not have an equivalent type inside
// Go-FUSE, but the result of Lookup operation essentially is a
// dentry, which the kernel puts in a cache.
//
// # Kernel caching
//
// The kernel caches several pieces of information from the FUSE process:
//
// 1. File contents: enabled with the fuse.FOPEN_KEEP_CACHE return flag
// in Open, manipulated with ReadCache and WriteCache, and invalidated
// with Inode.NotifyContent
//
// 2. File Attributes (size, mtime, etc.): controlled with the
// attribute timeout fields in fuse.AttrOut and fuse.EntryOut, which
// get be populated from Getattr and Lookup
//
// 3. Dentries (parent/child relations in the FS tree):
// controlled with the timeout fields in fuse.EntryOut, and
// invalidated with Inode.NotifyEntry and Inode.NotifyDelete.
//
// Without entry timeouts, every operation on file "a/b/c"
// must first do lookups for "a", "a/b" and "a/b/c", which is
// expensive because of context switches between the kernel and the
// FUSE process.
//
// Unsuccessful entry lookups can also be cached by setting an entry
// timeout when Lookup returns ENOENT.
//
// The libfuse C library specifies 1 second timeouts for both
// attribute and directory entries, but no timeout for negative
// entries. by default. This can be achieve in go-fuse by setting
// options on mount, eg.
//
//	sec := time.Second
//	opts := fs.Options{
//	  EntryTimeout: &sec,
//	  AttrTimeout: &sec,
//	}
//
// # Interrupts
//
// If the process accessing a FUSE file system is interrupted, the
// kernel sends an interrupt message, which cancels the context passed
// to the NodeXxxxx methods. If the file system chooses to honor this
// cancellation, the method must return [syscall.EINTR].  All unmasked
// signals generate an interrupt. In particular, the SIGURG signal
// (which the Go runtime uses for managing goroutine preemption) also
// generates an interrupt.
//
// # Locking
//
// Locks for networked filesystems are supported through the suite of
// Getlk, Setlk and Setlkw methods. They alllow locks on regions of
// regular files.
//
// # Parallelism
//
// The VFS layer in the kernel is optimized to be highly parallel, and
// this parallelism also affects FUSE file systems: many FUSE
// operations can run in parallel, and this invites race
// conditions. It is strongly recommended to test your FUSE file
// system issuing file operations in parallel, and using the race
// detector to weed out data races.
//
// # Deadlocks
//
// The Go runtime multiplexes Goroutines onto operating system
// threads, and makes assumptions that some system calls do not
// block. When accessing a file system from the same process that
// serves the file system (e.g. in unittests), this can lead to
// deadlocks, especially when GOMAXPROCS=1, when the Go runtime
// assumes a system call does not block, but actually is served by the
// Go-FUSE process.
//
// The following deadlocks are known:
//
// 1. Spawning a subprocess uses a fork/exec sequence: the process
// forks itself into a parent and child. The parent waits for the
// child to signal that the exec failed or succeeded, while the child
// prepares for calling exec(). Any setup step in the child that
// triggers a FUSE request can cause a deadlock.
//
// 1a. If the subprocess has a directory specified, the child will
// chdir into that directory. This generates an ACCESS operation on
// the directory.
//
// This deadlock can be avoided by disabling the ACCESS
// operation: return syscall.ENOSYS in the Access implementation, and
// ensure it is triggered called before initiating the subprocess.
//
// 1b. If the subprocess inherits files, the child process uses dup3()
// to remap file descriptors. If the destination fd happens to be
// backed by Go-FUSE, the dup3() call will implicitly close the fd,
// generating a FLUSH operation, eg.
//
//	f1, err := os.Open("/fusemnt/file1")
//	// f1.Fd() == 3
//	f2, err := os.Open("/fusemnt/file1")
//	// f2.Fd() == 4
//
//	cmd := exec.Command("/bin/true")
//	cmd.ExtraFiles = []*os.File{f2}
//	// f2 (fd 4) is moved to fd 3. Deadlocks with GOMAXPROCS=1.
//	cmd.Start()
//
// This deadlock can be avoided by ensuring that file descriptors
// pointing into FUSE mounts and file descriptors passed into
// subprocesses do not overlap, e.g. inserting the following before
// the above example:
//
//	for {
//		f, _ := os.Open("/dev/null")
//		defer f.Close()
//		if f.Fd() > 3 {
//			break
//		}
//	}
//
// The library tries to reserve fd 3, because FUSE mounts are created
// by calling "fusermount" with an inherited file descriptor, but the
// same problem may occur for other file descriptors.
//
// 2. The Go runtime uses the epoll system call to understand which
// goroutines can respond to I/O.  The runtime assumes that epoll does
// not block, but if files are on a FUSE filesystem, the kernel will
// generate a POLL operation. To prevent this from happening, Go-FUSE
// disables the POLL opcode on mount. To ensure this has happened, call
// WaitMount.
//
// 3. Memory mapping a file served by FUSE. Accessing the mapped
// memory generates a page fault, which blocks the OS thread running
// the goroutine.
//
// # Dynamically discovered file systems
//
// File system data usually cannot fit all in RAM, so the kernel must
// discover the file system dynamically: as you are entering and list
// directory contents, the kernel asks the FUSE server about the files
// and directories you are busy reading/writing, and forgets parts of
// your file system when it is low on memory.
//
// The two important operations for dynamic file systems are:
// 1. Lookup, part of the NodeLookuper interface for discovering
// individual children of directories, and 2. Readdir, part of the
// NodeReaddirer interface for listing the contents of a directory.
//
// # Static in-memory file systems
//
// For small, read-only file systems, getting the locking mechanics of
// Lookup correct is tedious, so Go-FUSE provides a feature to
// simplify building such file systems.
//
// Instead of discovering the FS tree on the fly, you can construct
// the entire tree from an OnAdd method. Then, that in-memory tree
// structure becomes the source of truth. This means that Go-FUSE must
// remember Inodes even if the kernel is no longer interested in
// them. This is done by instantiating "persistent" inodes from the
// OnAdd method of the root node.  See the ZipFS example for a
// runnable example of how to do this.
package fs

import (
	"context"
	"log"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// InodeEmbedder is an interface for structs that embed Inode.
//
// InodeEmbedder objects usually should implement some of the NodeXxxx
// interfaces, to provide user-defined file system behaviors.
//
// In general, if an InodeEmbedder does not implement specific
// filesystem methods, the filesystem will react as if it is a
// read-only filesystem with a predefined tree structure.
type InodeEmbedder interface {
	// inode is used internally to link Inode to a Node.
	//
	// See Inode() for the public API to retrieve an inode from Node.
	embed() *Inode

	// EmbeddedInode returns a pointer to the embedded inode.
	EmbeddedInode() *Inode
}

// Statfs implements statistics for the filesystem that holds this
// Inode. If not defined, the `out` argument will zeroed with an OK
// result.  This is because OSX filesystems must Statfs, or the mount
// will not work.
type NodeStatfser interface {
	Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno
}

// Access should return if the caller can access the file with the
// given mode.  This is used for two purposes: to determine if a user
// may enter a directory, and to implement the access system
// call.  In the latter case, the context has data about the real
// UID. For example, a root-SUID binary called by user susan gets the
// UID and GID for susan here.
//
// If not defined, a default implementation will check traditional
// unix permissions of the Getattr result agains the caller. If access
// permissions must be obeyed precisely, the filesystem should return
// permissions from GetAttr/Lookup, and set [Options.NullPermissions].
// Without [Options.NullPermissions], a missing permission (mode =
// 0000) is interpreted as 0755 for directories, and chdir is always
// allowed.
type NodeAccesser interface {
	Access(ctx context.Context, mask uint32) syscall.Errno
}

// GetAttr reads attributes for an Inode. The library will ensure that
// Mode and Ino are set correctly. For files that are not opened with
// FOPEN_DIRECTIO, Size should be set so it can be read correctly.  If
// returning zeroed permissions, the default behavior is to change the
// mode of 0755 (directory) or 0644 (files). This can be switched off
// with the Options.NullPermissions setting. If blksize is unset, 4096
// is assumed, and the 'blocks' field is set accordingly. The 'f'
// argument is provided for consistency, however, in practice the
// kernel never sends a file handle, even if the Getattr call
// originated from an fstat system call.
type NodeGetattrer interface {
	Getattr(ctx context.Context, f FileHandle, out *fuse.AttrOut) syscall.Errno
}

// SetAttr sets attributes for an Inode. Default is to return ENOTSUP.
type NodeSetattrer interface {
	Setattr(ctx context.Context, f FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno
}

// OnAdd is called when this InodeEmbedder is initialized.
type NodeOnAdder interface {
	OnAdd(ctx context.Context)
}

// Getxattr should read data for the given attribute into
// `dest` and return the number of bytes. If `dest` is too
// small, it should return ERANGE and the size of the attribute.
// If not defined, Getxattr will return ENOATTR.
type NodeGetxattrer interface {
	Getxattr(ctx context.Context, attr string, dest []byte) (uint32, syscall.Errno)
}

// Setxattr should store data for the given attribute.  See
// setxattr(2) for information about flags.
// If not defined, Setxattr will return ENOATTR.
type NodeSetxattrer interface {
	Setxattr(ctx context.Context, attr string, data []byte, flags uint32) syscall.Errno
}

// Removexattr should delete the given attribute.
// If not defined, Removexattr will return ENOATTR.
type NodeRemovexattrer interface {
	Removexattr(ctx context.Context, attr string) syscall.Errno
}

// Listxattr should read all attributes (null terminated) into
// `dest`. If the `dest` buffer is too small, it should return ERANGE
// and the correct size.  If not defined, return an empty list and
// success.
type NodeListxattrer interface {
	Listxattr(ctx context.Context, dest []byte) (uint32, syscall.Errno)
}

// Readlink reads the content of a symlink.
type NodeReadlinker interface {
	Readlink(ctx context.Context) ([]byte, syscall.Errno)
}

// Open opens an Inode (of regular file type) for reading. It
// is optional but recommended to return a FileHandle.
type NodeOpener interface {
	Open(ctx context.Context, flags uint32) (fh FileHandle, fuseFlags uint32, errno syscall.Errno)
}

// Reads data from a file. The data should be returned as
// ReadResult, which may be constructed from the incoming
// `dest` buffer. If the file was opened without FileHandle,
// the FileHandle argument here is nil. The default
// implementation forwards to the FileHandle.
type NodeReader interface {
	Read(ctx context.Context, f FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno)
}

// Writes the data into the file handle at given offset. After
// returning, the data will be reused and may not referenced.
// The default implementation forwards to the FileHandle.
type NodeWriter interface {
	Write(ctx context.Context, f FileHandle, data []byte, off int64) (written uint32, errno syscall.Errno)
}

// Fsync is a signal to ensure writes to the Inode are flushed
// to stable storage.
type NodeFsyncer interface {
	Fsync(ctx context.Context, f FileHandle, flags uint32) syscall.Errno
}

// Flush is called for the close(2) call on a file descriptor. In case
// of a descriptor that was duplicated using dup(2), it may be called
// more than once for the same FileHandle.  The default implementation
// forwards to the FileHandle, or if the handle does not support
// FileFlusher, returns OK.
type NodeFlusher interface {
	Flush(ctx context.Context, f FileHandle) syscall.Errno
}

// This is called to before a FileHandle is forgotten. The
// kernel ignores the return value of this method,
// so any cleanup that requires specific synchronization or
// could fail with I/O errors should happen in Flush instead.
// The default implementation forwards to the FileHandle.
type NodeReleaser interface {
	Release(ctx context.Context, f FileHandle) syscall.Errno

	// TODO - what about ReleaseIn?
}

// Allocate preallocates space for future writes, so they will
// never encounter ESPACE.
type NodeAllocater interface {
	Allocate(ctx context.Context, f FileHandle, off uint64, size uint64, mode uint32) syscall.Errno
}

// CopyFileRange copies data between sections of two files,
// without the data having to pass through the calling process.
type NodeCopyFileRanger interface {
	CopyFileRange(ctx context.Context, fhIn FileHandle,
		offIn uint64, out *Inode, fhOut FileHandle, offOut uint64,
		len uint64, flags uint64) (uint32, syscall.Errno)

	// Ugh. should have been called Copyfilerange
}

type NodeStatxer interface {
	Statx(ctx context.Context, f FileHandle, flags uint32, mask uint32, out *fuse.StatxOut) syscall.Errno
}

// Lseek is used to implement holes: it should return the
// first offset beyond `off` where there is data (SEEK_DATA)
// or where there is a hole (SEEK_HOLE).
type NodeLseeker interface {
	Lseek(ctx context.Context, f FileHandle, Off uint64, whence uint32) (uint64, syscall.Errno)
}

// Getlk returns locks that would conflict with the given input
// lock. If no locks conflict, the output has type L_UNLCK. See
// fcntl(2) for more information.
// If not defined, returns ENOTSUP
type NodeGetlker interface {
	Getlk(ctx context.Context, f FileHandle, owner uint64, lk *fuse.FileLock, flags uint32, out *fuse.FileLock) syscall.Errno
}

// Setlk obtains a lock on a file, or fail if the lock could not
// obtained.  See fcntl(2) for more information.  If not defined,
// returns ENOTSUP
type NodeSetlker interface {
	Setlk(ctx context.Context, f FileHandle, owner uint64, lk *fuse.FileLock, flags uint32) syscall.Errno
}

// Setlkw obtains a lock on a file, waiting if necessary. See fcntl(2)
// for more information.  If not defined, returns ENOTSUP
type NodeSetlkwer interface {
	Setlkw(ctx context.Context, f FileHandle, owner uint64, lk *fuse.FileLock, flags uint32) syscall.Errno
}

// Ioctl implements an ioctl on an open file.
type NodeIoctler interface {
	Ioctl(ctx context.Context, f FileHandle, cmd uint32, arg uint64, input []byte, output []byte) (result int32, errno syscall.Errno)
}

// OnForget is called when the node becomes unreachable. This can
// happen because the kernel issues a FORGET request,
// ForgetPersistent() is called on the inode, the last child of the
// directory disappears, or (for the root node) unmounting the file
// system. Implementers must make sure that the inode cannot be
// revived concurrently by a LOOKUP call. Modifying the tree using
// RmChild and AddChild can also trigger a spurious OnForget; use
// MvChild instead.
type NodeOnForgetter interface {
	OnForget()
}

// DirStream lists directory entries.
type DirStream interface {
	// HasNext indicates if there are further entries. HasNext
	// might be called on already closed streams.
	HasNext() bool

	// Next retrieves the next entry. It is only called if HasNext
	// has previously returned true.  The Errno return may be used to
	// indicate I/O errors
	Next() (fuse.DirEntry, syscall.Errno)

	// Close releases resources related to this directory
	// stream.
	Close()
}

// Lookup should find a direct child of a directory by the child's name.  If
// the entry does not exist, it should return ENOENT and optionally
// set a NegativeTimeout in `out`. If it does exist, it should return
// attribute data in `out` and return the Inode for the child. A new
// inode can be created using `Inode.NewInode`. The new Inode will be
// added to the FS tree automatically if the return status is OK.
//
// If a directory does not implement NodeLookuper, the library looks
// for an existing child with the given name.
//
// The input to a Lookup is {parent directory, name string}.
//
// Lookup, if successful, must return an *Inode. Once the Inode is
// returned to the kernel, the kernel can issue further operations,
// such as Open or Getxattr on that node.
//
// A successful Lookup also returns an EntryOut. Among others, this
// contains file attributes (mode, size, mtime, etc.).
//
// FUSE supports other operations that modify the namespace. For
// example, the Symlink, Create, Mknod, Link methods all create new
// children in directories. Hence, they also return *Inode and must
// populate their fuse.EntryOut arguments.
type NodeLookuper interface {
	Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*Inode, syscall.Errno)
}

// OpenDir opens a directory Inode for reading its
// contents. The actual reading is driven from Readdir, so
// this method is just for performing sanity/permission
// checks. The default is to return success.
type NodeOpendirer interface {
	Opendir(ctx context.Context) syscall.Errno
}

// Readdir opens a stream of directory entries.
//
// Readdir essentiallly returns a list of strings, and it is allowed
// for Readdir to return different results from Lookup. For example,
// you can return nothing for Readdir ("ls my-fuse-mount" is empty),
// while still implementing Lookup ("ls my-fuse-mount/a-specific-file"
// shows a single file). The DirStream returned must be deterministic;
// a randomized result (e.g. due to map iteration) can lead to entries
// disappearing if multiple processes read the same directory
// concurrently.
//
// If a directory does not implement NodeReaddirer, a list of
// currently known children from the tree is returned. This means that
// static in-memory file systems need not implement NodeReaddirer.
type NodeReaddirer interface {
	Readdir(ctx context.Context) (DirStream, syscall.Errno)
}

// Mkdir is similar to Lookup, but must create a directory entry and Inode.
// Default is to return ENOTSUP.
type NodeMkdirer interface {
	Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*Inode, syscall.Errno)
}

// Mknod is similar to Lookup, but must create a device entry and Inode.
// Default is to return ENOTSUP.
type NodeMknoder interface {
	Mknod(ctx context.Context, name string, mode uint32, dev uint32, out *fuse.EntryOut) (*Inode, syscall.Errno)
}

// Link is similar to Lookup, but must create a new link to an existing Inode.
// Default is to return ENOTSUP.
type NodeLinker interface {
	Link(ctx context.Context, target InodeEmbedder, name string, out *fuse.EntryOut) (node *Inode, errno syscall.Errno)
}

// Symlink is similar to Lookup, but must create a new symbolic link.
// Default is to return ENOTSUP.
type NodeSymlinker interface {
	Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (node *Inode, errno syscall.Errno)
}

// Create is similar to Lookup, but should create a new
// child. It typically also returns a FileHandle as a
// reference for future reads/writes.
// Default is to return EROFS.
type NodeCreater interface {
	Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (node *Inode, fh FileHandle, fuseFlags uint32, errno syscall.Errno)
}

// Unlink should remove a child from this directory.  If the
// return status is OK, the Inode is removed as child in the
// FS tree automatically. Default is to return success.
type NodeUnlinker interface {
	Unlink(ctx context.Context, name string) syscall.Errno
}

// Rmdir is like Unlink but for directories.
// Default is to return success.
type NodeRmdirer interface {
	Rmdir(ctx context.Context, name string) syscall.Errno
}

// Rename should move a child from one directory to a different
// one. The change is effected in the FS tree if the return status is
// OK. Default is to return ENOTSUP.
type NodeRenamer interface {
	Rename(ctx context.Context, name string, newParent InodeEmbedder, newName string, flags uint32) syscall.Errno
}

// FileHandle is a resource identifier for opened files. Usually, a
// FileHandle should implement some of the FileXxxx interfaces.
//
// All of the FileXxxx operations can also be implemented at the
// InodeEmbedder level, for example, one can implement NodeReader
// instead of FileReader.
//
// FileHandles are useful in two cases: First, if the underlying
// storage systems needs a handle for reading/writing. This is the
// case with Unix system calls, which need a file descriptor (See also
// the function `NewLoopbackFile`). Second, it is useful for
// implementing files whose contents are not tied to an inode. For
// example, a file like `/proc/interrupts` has no fixed content, but
// changes on each open call. This means that each file handle must
// have its own view of the content; this view can be tied to a
// FileHandle. Files that have such dynamic content should return the
// FOPEN_DIRECT_IO flag from their `Open` method. See directio_test.go
// for an example.
type FileHandle interface {
}

// FilePassthroughFder is a file backed by a physical
// file. PassthroughFd should return an open file descriptor (and
// true), and the kernel will execute read/write operations directly
// on the backing file, bypassing the FUSE process. This function will
// be called once when processing the Create or Open operation, so
// there is no concern about concurrent access to the Fd. If the
// function returns false, passthrough will not be used for this file.
type FilePassthroughFder interface {
	PassthroughFd() (int, bool)
}

// See NodeReleaser.
type FileReleaser interface {
	Release(ctx context.Context) syscall.Errno
}

// See NodeGetattrer.
type FileGetattrer interface {
	Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno
}

type FileStatxer interface {
	Statx(ctx context.Context, flags uint32, mask uint32, out *fuse.StatxOut) syscall.Errno
}

// See NodeReader.
type FileReader interface {
	Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno)
}

// See NodeWriter.
type FileWriter interface {
	Write(ctx context.Context, data []byte, off int64) (written uint32, errno syscall.Errno)
}

// See NodeGetlker.
type FileGetlker interface {
	Getlk(ctx context.Context, owner uint64, lk *fuse.FileLock, flags uint32, out *fuse.FileLock) syscall.Errno
}

// See NodeSetlker.
type FileSetlker interface {
	Setlk(ctx context.Context, owner uint64, lk *fuse.FileLock, flags uint32) syscall.Errno
}

// See NodeSetlkwer.
type FileSetlkwer interface {
	Setlkw(ctx context.Context, owner uint64, lk *fuse.FileLock, flags uint32) syscall.Errno
}

// See NodeLseeker.
type FileLseeker interface {
	Lseek(ctx context.Context, off uint64, whence uint32) (uint64, syscall.Errno)
}

// See NodeFlusher.
type FileFlusher interface {
	Flush(ctx context.Context) syscall.Errno
}

// See NodeFsync.
type FileFsyncer interface {
	Fsync(ctx context.Context, flags uint32) syscall.Errno
}

// See NodeFsync.
type FileSetattrer interface {
	Setattr(ctx context.Context, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno
}

// See NodeAllocater.
type FileAllocater interface {
	Allocate(ctx context.Context, off uint64, size uint64, mode uint32) syscall.Errno
}

// See NodeIoctler.
type FileIoctler interface {
	Ioctl(ctx context.Context, cmd uint32, arg uint64, input []byte, output []byte) (result int32, errno syscall.Errno)
}

// Opens a directory. This supersedes NodeOpendirer, allowing to pass
// back flags (eg. FOPEN_CACHE_DIR).
type NodeOpendirHandler interface {
	OpendirHandle(ctx context.Context, flags uint32) (fh FileHandle, fuseFlags uint32, errno syscall.Errno)
}

// FileReaddirenter is a directory that supports reading.
type FileReaddirenter interface {
	// Read a single directory entry.
	Readdirent(ctx context.Context) (*fuse.DirEntry, syscall.Errno)
}

// FileFsyncer is a directory that supports fsyncdir.
type FileFsyncdirer interface {
	Fsyncdir(ctx context.Context, flags uint32) syscall.Errno
}

// FileSeekdirer is directory that supports seeking. `off` is an
// opaque uint64 value, where only the value 0 is reserved for the
// start of the stream. (See https://lwn.net/Articles/544520/ for
// background).
type FileSeekdirer interface {
	Seekdir(ctx context.Context, off uint64) syscall.Errno
}

// FileReleasedirer is a directory that supports a cleanup operation.
type FileReleasedirer interface {
	Releasedir(ctx context.Context, releaseFlags uint32)
}

// Options are options for the entire filesystem.
type Options struct {
	// MountOptions contain the options for mounting the fuse server.
	fuse.MountOptions

	// EntryTimeout, if non-nil, defines the overall entry timeout
	// for the file system. See [fuse.EntryOut] for more information.
	EntryTimeout *time.Duration

	// AttrTimeout, if non-nil, defines the overall attribute
	// timeout for the file system. See [fuse.AttrOut] for more
	// information.
	AttrTimeout *time.Duration

	// NegativeTimeout, if non-nil, defines the overall entry timeout
	// for failed lookups (fuse.ENOENT). See [fuse.EntryOut] for
	// more information.
	NegativeTimeout *time.Duration

	// FirstAutomaticIno is start of the automatic inode numbers that are handed
	// out sequentially.
	//
	// If unset, the default is 2^63.
	FirstAutomaticIno uint64

	// OnAdd, if non-nil, is an alternative way to specify the OnAdd
	// functionality of the root node.
	OnAdd func(ctx context.Context)

	// NullPermissions, if set, leaves null file permissions
	// alone. Otherwise, they are set to 755 (dirs) or 644 (other
	// files.), which is necessary for doing a chdir into the FUSE
	// directories.
	NullPermissions bool

	// UID, if nonzero, is the default UID to use instead of the
	// zero (zero) UID.
	UID uint32

	// GID, if nonzero, is the default GID to use instead of the
	// zero (zero) GID.
	GID uint32

	// ServerCallbacks are optional callbacks to stub out notification functions
	// for testing a filesystem without mounting it.
	ServerCallbacks ServerCallbacks

	// Logger is a sink for diagnostic messages. Diagnostic
	// messages are printed under conditions where we cannot
	// return error, but want to signal something seems off
	// anyway. If unset, no messages are printed.
	//
	// This field shadows (and thus, is distinct) from
	// MountOptions.Logger.
	Logger *log.Logger

	// RootStableAttr is an optional way to set e.g. Ino and/or Gen for
	// the root directory when calling fs.Mount(), Mode is ignored.
	RootStableAttr *StableAttr
}
