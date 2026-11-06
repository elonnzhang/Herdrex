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

// maxUploadCount is the most files one batch may hold. The bridge allocates
// a slot per file when the batch is reserved, so count needs a bound.
const maxUploadCount = 64

// uploadIdle is how long an upload batch may go without activity before the
// bridge forgets it.
const uploadIdle = 5 * time.Minute

// UploadStore writes an uploaded file where the pane's programs can read it
// and returns its path there. The default writes to this machine's temporary
// directory; herdrex ssh writes to the remote host, where Herdr runs.
type UploadStore interface {
	Store(ctx context.Context, name string, data []byte) (path string, err error)
}

// LocalUploads stores uploads in this machine's temporary directory.
type LocalUploads struct{}

// Store writes data to a new private directory under os.TempDir.
func (LocalUploads) Store(_ context.Context, name string, data []byte) (string, error) {
	dir, err := os.MkdirTemp("", "herdrex-upload-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// uploadBatch is the files one upload_begin call group expects. Rex types
// their paths into the terminal in index order once every file has arrived,
// so the bridge keeps them until then.
type uploadBatch struct {
	key    string
	client string // the control connection that reserved it
	pane   string
	files  []*uploadFile
	seen   time.Time // last reservation or byte; guarded by upMu
}

// uploadFile is one file of a batch and the bytes received so far.
type uploadFile struct {
	name  string
	size  int64
	index int
	got   []byte
	path  string // where the store put the file once its bytes all arrived
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
	} else if s.count < 1 || s.count > maxUploadCount || s.index < 0 || s.index >= s.count {
		return s, rex.NewError(rex.CodeBadRequest, fmt.Sprintf("a batch needs count from 1 to %d, and index below it", maxUploadCount), 400)
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
	for k, u := range b.uploads { // forget batches nobody has touched for a while
		if time.Since(u.seen) > uploadIdle {
			delete(b.uploads, k)
		}
	}
	u := b.uploads[key]
	if u == nil {
		u = &uploadBatch{key: key, client: c.ID, pane: pane, files: make([]*uploadFile, count)}
		b.uploads[key] = u
	}
	if u.pane != pane || u.client != c.ID || len(u.files) != count {
		b.upMu.Unlock()
		return nil, rex.NewError(rex.CodeConflict, "the batch is already reserved by another connection or pane", 409)
	}
	if u.files[index] != nil {
		b.upMu.Unlock()
		return nil, rex.NewError(rex.CodeConflict, fmt.Sprintf("file %d of the batch is already reserved", index), 409)
	}
	f := &uploadFile{name: s.name, size: s.size, index: index}
	u.files[index] = f
	u.seen = time.Now()
	b.upMu.Unlock()

	ticket, ttl, err := b.srv.UploadTicket(c, rex.DataBytesProtocol, func(ctx context.Context, st *rexsvr.UploadStream) {
		b.receive(ctx, u, f, st)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ticket": ticket, "expires_in_ms": ttl}, nil
}

// receive collects one file, stores it and, when the batch is whole, types
// the paths. The stream closes normally only after the file is stored, so a
// client never sees success for a file the bridge could not keep.
func (b *Bridge) receive(ctx context.Context, u *uploadBatch, f *uploadFile, st *rexsvr.UploadStream) {
	for int64(len(f.got)) < f.size {
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
		b.touch(u)
	}
	if err := b.complete(ctx, u, f); err != nil {
		st.Close(1011, "could not store the file")
		b.abort(u, f, "%v", err)
		return
	}
	st.Close(1000, "")
}

// touch keeps an active batch from expiring.
func (b *Bridge) touch(u *uploadBatch) {
	b.upMu.Lock()
	u.seen = time.Now()
	b.upMu.Unlock()
}

// complete stores a whole file and, when the batch is whole, types the paths
// into the pane in index order.
func (b *Bridge) complete(ctx context.Context, u *uploadBatch, f *uploadFile) error {
	paths, err := b.store(ctx, u, f)
	if err != nil || len(paths) == 0 {
		return err // len 0: the rest of the batch is still coming
	}
	text := strings.Join(paths, "\n")
	b.unscroll(ctx, u.pane)
	if err := b.pasteInput(ctx, u.pane, pasteStart+text+pasteEnd, text); err != nil {
		log.Printf("upload: typing the paths: %v", err)
	}
	return nil
}

// store writes a whole file through b.Uploads. Once the batch is whole it
// returns the paths in index order, each quoted as one shell word, and
// forgets the batch; while files are still missing it returns nil.
func (b *Bridge) store(ctx context.Context, u *uploadBatch, f *uploadFile) ([]string, error) {
	store := b.Uploads
	if store == nil {
		store = LocalUploads{}
	}
	path, err := store.Store(ctx, f.name, f.got)
	if err != nil {
		return nil, fmt.Errorf("could not store the file: %w", err)
	}
	b.upMu.Lock()
	defer b.upMu.Unlock()
	f.path, f.done, f.got = path, true, nil
	u.seen = time.Now()
	paths := make([]string, 0, len(u.files))
	for _, x := range u.files {
		if x == nil || !x.done {
			return nil, nil
		}
		paths = append(paths, shellWord(x.path))
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

// shellWord quotes a path for a POSIX shell when it holds anything but
// characters a shell leaves alone, so that a pasted path is one word and a
// file name such as "x;id;#" cannot run a command when the user presses Enter.
func shellWord(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+@%,=:", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
