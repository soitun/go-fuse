// Copyright 2016 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fuse

import (
	"syscall"
)

const useSingleReader = false

func (ms *Server) systemWrite(req *request) Status {
	if req.flatDataSize() == 0 {
		err := handleEINTR(func() error {
			_, err := syscall.Write(ms.mountFd, req.outputBuf)
			return err
		})
		return ToStatus(err)
	}
	if req.fdData != nil {
		if ms.canSplice {
			err := ms.trySplice(req, req.fdData)
			if err == nil {
				req.readResult.Done()
				return OK
			}
			ms.opts.Logger.Println("trySplice:", err)
		}

		sz := req.flatDataSize()
		buf := ms.allocOut(req, uint32(sz))
		req.flatData, req.status = req.fdData.Bytes(buf)
		req.serializeHeader(len(req.flatData))
	}

	_, err := writev(ms.mountFd, [][]byte{req.outputBuf, req.flatData})
	if req.readResult != nil {
		req.readResult.Done()
	}
	return ToStatus(err)
}
