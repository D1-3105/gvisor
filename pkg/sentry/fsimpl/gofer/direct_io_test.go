// Copyright 2026 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gofer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/context"
)

func TestHostDirectHandles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	dir, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dir)
	fs := &filesystem{}
	parentImpl := &directfsInode{controlFD: dir}
	parentImpl.impl = parentImpl
	parent := &dentry{inode: &parentImpl.inode}
	impl := &directfsInode{}
	impl.fs = fs
	impl.impl = impl
	d := &dentry{inode: &impl.inode, name: "file"}
	d.parent.Store(parent)
	ctx := context.Background()
	defer func() {
		d.inode.handleMu.Lock()
		d.inode.closeDirectHandles(ctx)
		d.inode.handleMu.Unlock()
	}()
	fs.renameMu.RLock()
	defer fs.renameMu.RUnlock()
	writer, err := d.directHandle(ctx, true)
	if err == unix.EINVAL || err == unix.EOPNOTSUPP {
		t.Skipf("host filesystem does not support direct I/O: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	flags, err := unix.FcntlInt(uintptr(writer.fd), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_DIRECT == 0 {
		t.Fatalf("direct writer flags=%#x, err=%v", flags, err)
	}
	again, err := d.directHandle(ctx, true)
	if err != nil || again.fd != writer.fd {
		t.Fatalf("handle not reused: %v, %v", again, err)
	}
	reader, err := d.directHandle(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if reader.fd == writer.fd {
		t.Fatal("reader shares write-only descriptor")
	}
	buf, err := unix.Mmap(-1, 0, 8192, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Munmap(buf)
	for n := range buf[:4096] {
		buf[n] = byte(n % 251)
	}
	if n, err := unix.Pwrite(int(writer.fd), buf[:4096], 0); n != 4096 || err != nil {
		t.Fatalf("direct write=%d, %v", n, err)
	}
	if n, err := unix.Pread(int(reader.fd), buf[4096:], 0); n != 4096 || err != nil {
		t.Fatalf("direct read=%d, %v", n, err)
	}
	if !bytes.Equal(buf[:4096], buf[4096:]) {
		t.Fatal("direct round trip differs")
	}
	normal, err := unix.Open(path, unix.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(normal)
	if n, err := unix.Pwrite(normal, []byte("x"), 1); n != 1 || err != nil {
		t.Fatalf("buffered write affected by direct handles: %d, %v", n, err)
	}
	d.inode.handleMu.Lock()
	d.inode.closeDirectHandles(ctx)
	d.inode.closeDirectHandles(ctx)
	d.inode.handleMu.Unlock()
	if _, err := unix.FcntlInt(uintptr(writer.fd), unix.F_GETFL, 0); err != unix.EBADF {
		t.Fatalf("writer leaked: %v", err)
	}
	if _, err := unix.FcntlInt(uintptr(reader.fd), unix.F_GETFL, 0); err != unix.EBADF {
		t.Fatalf("reader leaked: %v", err)
	}
	reopened, err := d.directHandle(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := unix.Pwrite(int(reopened.fd), buf[:4096], 4096); n != 4096 || err != nil {
		t.Fatalf("reopened direct write=%d, %v", n, err)
	}
}
