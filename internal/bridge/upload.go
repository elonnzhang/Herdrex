package bridge

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/elonnzhang/herdrex/pkg/rex"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
)

// maxUploadSize is the largest file an upload may carry. It bounds what one
// caller can make the bridge write to its disk; Rex has a limit of its own.
const maxUploadSize = 256 << 20

// uploadIdle is how long an upload batch waits for the rest of its files
// before the bridge forgets it.
const uploadIdle = 5 * time.Minute

// uploadBatch is the files one upload_begin call group expects. Rex types
// their paths into the terminal in index order once every file has arrived,
// so the bridge keeps them until then.
type uploadBatch struct {
	key     string
	client  string // the control connection that reserved it
	pane    string
	files   []*uploadFile
	created time.Time
}

// uploadFile is one file of a batch and the bytes received so far.
type uploadFile struct {
	name  string
	size  int64
	index int
	got   []byte
	path  string // the file written once its bytes all arrived
	done  bool
}

// uploadSpec is one file an upload_begin call reserves.
type uploadSpec struct {
	ref, name, batch string
	size             int64
	index, count     int
}

// parseUpload checks the upload_begin arguments.
func parseUpload(args P) (uploadSpec, error) {
	s := uploadSpec{
		ref: args.str("ref"), name: args.str("name"), batch: args.str("batch"),
		size: int64(args.num("size")), index: int(args.num("index")), count: int(args.num("count")),
	}
	if !strings.HasPrefix(s.ref, "upload:") {
		return s, rex.NewError(rex.CodeBadRequest, "ref must start with upload:", 400)
	}
	if _, err := uuid.Parse(strings.TrimPrefix(s.ref, "upload:")); err != nil {
		return s, rex.NewError(rex.CodeBadRequest, "ref must hold a UUID: "+err.Error(), 400)
	}
	if !fileName(s.name) {
		return s, rex.NewError(rex.CodeBadRequest, fmt.Sprintf("upload name %q is not a file name", s.name), 400)
	}
	if s.size < 0 || s.size > maxUploadSize {
		return s, rex.NewError(rex.CodeBadRequest, fmt.Sprintf("upload size %d is outside 0 to %d", s.size, maxUploadSize), 400)
	}
	if s.batch == "" {
		if s.index != 0 || s.count > 1 {
			return s, rex.NewError(rex.CodeBadRequest, "index and count need a batch", 400)
		}
		s.count = 1
	} else if s.count < 1 || s.index < 0 || s.index >= s.count {
		return s, rex.NewError(rex.CodeBadRequest, "a batch needs count, and index below it", 400)
	}
	return s, nil
}

// uploadBegin reserves a file for the terminal and answers with the ticket
// its rex.data.bytes.v1 stream uses. The paths are typed into the pane once
// the batch is complete, as if the user had pasted them.
func (b *Bridge) uploadBegin(ctx context.Context, c *rexsvr.Client, p P, pane string) (any, error) {
	sid := p.str("session_id")
	if !c.Attached(sid) {
		return nil, rex.NewError(rex.CodeConflict, "connection is not attached to this session", 409)
	}
	s, err := parseUpload(p.obj("args"))
	if err != nil {
		return nil, err
	}
	key, index, count := s.batch, s.index, s.count
	if key == "" {
		key = s.ref
	}
	b.upMu.Lock()
	for k, u := range b.uploads { // forget batches whose files never came
		if time.Since(u.created) > uploadIdle {
			delete(b.uploads, k)
		}
	}
	u := b.uploads[key]
	if u == nil {
		u = &uploadBatch{key: key, client: c.ID, pane: pane, files: make([]*uploadFile, count), created: time.Now()}
		b.uploads[key] = u
	}
	if u.pane != pane || u.client != c.ID || len(u.files) != count {
		b.upMu.Unlock()
		return nil, rex.NewError(rex.CodeConflict, "the batch is already reserved by another connection or pane", 409)
	}
	f := &uploadFile{name: s.name, size: s.size, index: index}
	u.files[index] = f
	b.upMu.Unlock()

	ticket, ttl, err := b.srv.UploadTicket(c, rex.DataBytesProtocol, func(ctx context.Context, st *rexsvr.UploadStream) {
		b.receive(ctx, u, f, st)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ticket": ticket, "expires_in_ms": ttl}, nil
}

// receive collects one file, then types the batch's paths once it is whole.
func (b *Bridge) receive(ctx context.Context, u *uploadBatch, f *uploadFile, st *rexsvr.UploadStream) {
	if f.size == 0 { // an empty file is whole before any byte arrives
		st.Close(1000, "")
		b.complete(ctx, u, f)
		return
	}
	for {
		msg, err := st.Read(ctx)
		if err != nil {
			b.abort(u, f, "the stream ended with %d of %d bytes", int64(len(f.got)), f.size)
			return
		}
		if int64(len(f.got))+int64(len(msg)) > f.size {
			st.Close(1008, "more bytes than the reserved size")
			b.abort(u, f, "more than %d bytes", f.size)
			return
		}
		f.got = append(f.got, msg...)
		if int64(len(f.got)) == f.size {
			st.Close(1000, "")
			b.complete(ctx, u, f)
			return
		}
	}
}

// complete writes a whole file and, when the batch is whole, types the paths
// into the pane in index order.
func (b *Bridge) complete(ctx context.Context, u *uploadBatch, f *uploadFile) {
	paths, err := b.store(u, f)
	if err != nil {
		log.Printf("upload %s: %v", f.name, err)
		b.abort(u, f, "could not store the file: %v", err)
		return
	}
	if len(paths) == 0 {
		return // the rest of the batch is still coming
	}
	text := strings.Join(paths, "\n")
	b.unscroll(ctx, u.pane)
	if err := b.pasteInput(ctx, u.pane, pasteStart+text+pasteEnd, text); err != nil {
		log.Printf("upload: typing the paths: %v", err)
	}
}

// store writes a whole file. It returns the batch's paths in index order once
// the batch is whole, and nil while files are still missing.
func (b *Bridge) store(u *uploadBatch, f *uploadFile) ([]string, error) {
	dir, err := os.MkdirTemp("", "herdrex-upload-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, f.name)
	if err := os.WriteFile(path, f.got, 0o600); err != nil {
		return nil, err
	}

	b.upMu.Lock()
	defer b.upMu.Unlock()
	f.path, f.done, f.got = path, true, nil
	paths := make([]string, 0, len(u.files))
	for _, x := range u.files {
		if x == nil || !x.done {
			return nil, nil
		}
		paths = append(paths, x.path)
	}
	delete(b.uploads, u.key)
	return paths, nil
}

// dropUploads forgets the batches a control connection reserved, since its
// files can no longer be completed by anyone else.
func (b *Bridge) dropUploads(client string) {
	b.upMu.Lock()
	defer b.upMu.Unlock()
	for k, u := range b.uploads {
		if u.client == client {
			delete(b.uploads, k)
		}
	}
}

// abort drops a file that never arrived whole, so the batch cannot complete.
func (b *Bridge) abort(u *uploadBatch, f *uploadFile, format string, args ...any) {
	log.Printf("upload %s: %s", f.name, fmt.Sprintf(format, args...))
	b.upMu.Lock()
	if b.uploads[u.key] == u {
		delete(b.uploads, u.key)
	}
	b.upMu.Unlock()
}

// fileName reports whether name is usable as a single file name.
func fileName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, 0) {
		return false
	}
	if filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return false
	}
	return strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0
}
