// Copyright 2016 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fuse

import (
	"bytes"
	"syscall"
	"unsafe"
)

func getxattr(path string, attr string, dest []byte) (sz int, errno int) {
	pathBs, err := syscall.BytePtrFromString(path)
	if err != nil {
		return 0, int(syscall.EINVAL)
	}

	attrBs, err := syscall.BytePtrFromString(attr)
	if err != nil {
		return 0, int(syscall.EINVAL)
	}

	size, _, errNo := syscall.Syscall6(
		syscall.SYS_GETXATTR,
		uintptr(unsafe.Pointer(pathBs)),
		uintptr(unsafe.Pointer(attrBs)),
		uintptr(unsafe.Pointer(&dest[0])),
		uintptr(len(dest)),
		0, 0)
	return int(size), int(errNo)
}

func GetXAttr(path string, attr string, dest []byte) (value []byte, errno int) {
	sz, errno := getxattr(path, attr, dest)

	for sz > cap(dest) && errno == 0 {
		dest = make([]byte, sz)
		sz, errno = getxattr(path, attr, dest)
	}

	if errno != 0 {
		return nil, errno
	}

	return dest[:sz], errno
}

func listxattr(path string, dest []byte) (sz int, errno int) {
	pathbs, err := syscall.BytePtrFromString(path)
	if err != nil {
		return 0, int(syscall.EINVAL)
	}

	var destPointer unsafe.Pointer
	if len(dest) > 0 {
		destPointer = unsafe.Pointer(&dest[0])
	}
	size, _, errNo := syscall.Syscall(
		syscall.SYS_LISTXATTR,
		uintptr(unsafe.Pointer(pathbs)),
		uintptr(destPointer),
		uintptr(len(dest)))

	return int(size), int(errNo)
}

func ListXAttr(path string) (attributes []string, errno int) {
	dest := make([]byte, 0)
	sz, errno := listxattr(path, dest)
	if errno != 0 {
		return nil, errno
	}

	for sz > cap(dest) && errno == 0 {
		dest = make([]byte, sz)
		sz, errno = listxattr(path, dest)
	}

	// -1 to drop the final empty slice.
	dest = dest[:sz-1]
	attributesBytes := bytes.Split(dest, []byte{0})
	attributes = make([]string, len(attributesBytes))
	for i, v := range attributesBytes {
		attributes[i] = string(v)
	}
	return attributes, errno
}

func Setxattr(path string, attr string, data []byte, flags int) (errno int) {
	pathbs, err := syscall.BytePtrFromString(path)
	if err != nil {
		return int(syscall.EINVAL)
	}

	attrbs, err := syscall.BytePtrFromString(attr)
	if err != nil {
		return int(syscall.EINVAL)
	}

	_, _, errNo := syscall.Syscall6(
		syscall.SYS_SETXATTR,
		uintptr(unsafe.Pointer(pathbs)),
		uintptr(unsafe.Pointer(attrbs)),
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		uintptr(flags), 0)

	return int(errNo)
}

func Removexattr(path string, attr string) (errno int) {
	pathbs, err := syscall.BytePtrFromString(path)
	if err != nil {
		return int(syscall.EINVAL)
	}

	attrbs, err := syscall.BytePtrFromString(attr)
	if err != nil {
		return int(syscall.EINVAL)
	}

	_, _, errNo := syscall.Syscall(
		syscall.SYS_REMOVEXATTR,
		uintptr(unsafe.Pointer(pathbs)),
		uintptr(unsafe.Pointer(attrbs)), 0)
	return int(errNo)
}
