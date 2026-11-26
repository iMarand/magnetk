package chat

// Large attachments travel through the file engine, like media in WhatsApp:
// the sender encrypts the file with a fresh random key (AES-256-GCM in 1 MiB
// segments, compressed first when the content is compressible), shares the
// ciphertext, and sends a sealed chat message that carries only the link,
// the key and the plaintext SHA-256. The receiver downloads it - peer to
// peer when possible, resumable, verified chunk by chunk - decrypts it,
// checks the hash and acknowledges. Whoever obtains the link without the
// message gets ciphertext only.
//
// Encrypted file format:
//
//	"MKE1" | u32 segment size | u8 flags (1 = deflated before encryption) | 8-byte nonce prefix
//	segments: AES-256-GCM(key, prefix || u32 counter, plaintext segment, aad = "mke1" | counter | last)

import (
	"bufio"
	"compress/flate"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iMarand/magnetk/go/pkg/hrtime"
	"github.com/iMarand/magnetk/go/pkg/node"
)

const (
	// KindFile is an attachment sent through the file engine.
	KindFile = "file"

	// InlineLimit: attachments up to this size travel inside the message;
	// bigger ones go through the file engine (SendFile).
	InlineLimit = 256 << 10
	// MaxFileAttachment bounds SendFile.
	MaxFileAttachment = 4 << 30

	segSize     = 1 << 20
	fileMagic   = "MKE1"
	flagDeflate = 1
)

// outgoingFile is a sent file kept shared until it is acknowledged.
type outgoingFile struct {
	hash, path string
	expires    time.Time
}

// SendFile sends the file at path (any size up to 4 GiB) through the file
// engine, end-to-end encrypted when the recipient supports it. mime and
// durMs describe it (e.g. audio/mp4 and the length of a voice note).
func (b *Box) SendFile(ctx context.Context, to, path, caption, mime string, durMs int) (Msg, error) {
	n := b.node()
	if n == nil {
		return Msg{}, errors.New("not connected")
	}
	st, err := os.Stat(path)
	if err != nil {
		return Msg{}, err
	}
	if st.Size() > MaxFileAttachment {
		return Msg{}, fmt.Errorf("file too large (max %s)", fmtSize(MaxFileAttachment))
	}
	b.mu.Lock()
	if to == "" {
		to = b.last
	}
	policy := b.policy
	b.mu.Unlock()
	if to == "" {
		return Msg{}, errors.New("no recipient yet")
	}
	m := New(b.self, to, KindFile, caption, mime, durMs, nil)
	m.Name = filepath.Base(path)
	m.Size = int(st.Size())

	encrypted := false
	shared := path
	if b.key != nil {
		if _, err := b.peerKey(ctx, n, to); err == nil {
			encrypted = true
		} else if errors.Is(err, ErrKeyChanged) || policy.RequireEncryption {
			return m, err
		}
	} else if policy.RequireEncryption {
		return m, ErrNotEncrypted
	}
	if encrypted {
		key := make([]byte, 32)
		rand.Read(key)
		out := filepath.Join(b.outboxDir(), m.ID+".mke")
		sum, err := encryptFile(path, out, key)
		if err != nil {
			return m, err
		}
		m.SHA256, m.FileKey, shared = sum, base64.StdEncoding.EncodeToString(key), out
	} else {
		sum, err := fileSHA256(path)
		if err != nil {
			return m, err
		}
		m.SHA256 = sum
	}
	sf, err := n.Share(ctx, shared, nil)
	if err != nil {
		return m, err
	}
	m.Link = sf.Link
	b.mu.Lock()
	b.outFiles[m.ID] = outgoingFile{hash: sf.Hash, path: map[bool]string{true: shared, false: ""}[encrypted], expires: time.Now().Add(48 * time.Hour)}
	b.mu.Unlock()

	wire, err := b.wrap(ctx, n, &m, nil, policy)
	if err != nil {
		b.dropOutgoing(m.ID)
		return m, err
	}
	if b.pins != nil {
		b.pins.SetAllowed(to, true)
	}
	b.mu.Lock()
	b.sent[m.ID] = hrtime.Now()
	b.last = to
	b.add(m)
	b.mu.Unlock()
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := n.Send(sctx, to, wire, node.WithReceipt()); err != nil {
		b.dropOutgoing(m.ID)
		return m, err
	}
	return m, nil
}

func (b *Box) outboxDir() string {
	d := filepath.Join(b.dir, ".outbox")
	os.MkdirAll(d, 0o700)
	return d
}

// dropOutgoing stops sharing a sent file (after its ack, or on failure).
func (b *Box) dropOutgoing(id string) {
	b.mu.Lock()
	of, ok := b.outFiles[id]
	delete(b.outFiles, id)
	b.mu.Unlock()
	if !ok {
		return
	}
	if n := b.node(); n != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		n.Unshare(ctx, of.hash)
		cancel()
	}
	if of.path != "" {
		os.Remove(of.path)
	}
}

// fetchFile downloads, decrypts and verifies a received file attachment,
// then reports it and sends the ack.
func (b *Box) fetchFile(m Msg) {
	n := b.node()
	if n == nil {
		return
	}
	m.Pending = true
	b.on(m) // "receiving..." for UIs
	m.Pending = false
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	os.MkdirAll(b.dir, 0o755)
	name := safeName(strings.TrimSuffix(m.Name, filepath.Ext(m.Name))) + filepath.Ext(m.Name)
	if name == "" || name == "." {
		name = "file" + extFor(m.Mime)
	}
	final := filepath.Join(b.dir, safeName(m.From)+"-"+safeName(m.ID)+"-"+name)
	target := final
	if m.FileKey != "" {
		target = filepath.Join(b.outboxDir(), "in-"+safeName(m.ID)+".mke")
	}
	_, err := n.Download(ctx, m.Link, target, &node.DownloadOptions{})
	if err == nil && m.FileKey != "" {
		var key []byte
		if key, err = base64.StdEncoding.DecodeString(m.FileKey); err == nil {
			err = decryptFile(target, final, key)
		}
		os.Remove(target)
	}
	if err != nil {
		m.Error = "attachment download failed: " + err.Error()
		b.on(m)
		return
	}
	sum, _ := fileSHA256(final)
	m.Verified = sum == m.SHA256
	m.Path = final
	if !m.Verified {
		m.Error = "attachment SHA-256 mismatch"
	}
	b.mu.Lock()
	for i := len(b.hist) - 1; i >= 0; i-- {
		if b.hist[i].ID == m.ID {
			b.hist[i] = m
			break
		}
	}
	b.mu.Unlock()
	b.on(m)
	if m.Verified {
		b.sendAck(m.From, m.ID)
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func segAAD(counter uint32, last bool) []byte {
	a := []byte("mke1")
	a = binary.BigEndian.AppendUint32(a, counter)
	if last {
		return append(a, 1)
	}
	return append(a, 0)
}

func segNonce(prefix []byte, counter uint32) []byte {
	return binary.BigEndian.AppendUint32(append([]byte(nil), prefix...), counter)
}

// encryptFile writes the encrypted form of src to dst and returns the
// plaintext SHA-256.
func encryptFile(src, dst string, key []byte) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	st, _ := in.Stat()
	compress := identityLooksCompressible(in, st.Size())
	in.Seek(0, io.SeekStart)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer out.Close()
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	prefix := make([]byte, 8)
	rand.Read(prefix)
	hdr := append([]byte(fileMagic), 0, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(hdr[4:], segSize)
	if compress {
		hdr[8] = flagDeflate
	}
	bw := bufio.NewWriterSize(out, 1<<20)
	bw.Write(hdr)
	bw.Write(prefix)

	sum := sha256.New()
	var plain io.Reader = io.TeeReader(in, sum)
	if compress {
		pr, pw := io.Pipe()
		raw := plain
		go func() {
			zw, _ := flate.NewWriter(pw, flate.BestSpeed)
			_, err := io.Copy(zw, raw)
			if err == nil {
				err = zw.Close()
			}
			pw.CloseWithError(err)
		}()
		plain = pr
	}
	buf := make([]byte, segSize)
	next := make([]byte, segSize)
	nCur, errCur := io.ReadFull(plain, buf)
	for counter := uint32(0); ; counter++ {
		if errCur != nil && errCur != io.ErrUnexpectedEOF && errCur != io.EOF {
			return "", errCur
		}
		last := errCur != nil
		var nNext int
		var errNext error
		if !last {
			nNext, errNext = io.ReadFull(plain, next)
			if errNext == io.EOF {
				last = true
			}
		}
		ct := aead.Seal(nil, segNonce(prefix, counter), buf[:nCur], segAAD(counter, last))
		if _, err := bw.Write(ct); err != nil {
			return "", err
		}
		if last {
			break
		}
		buf, next = next, buf
		nCur, errCur = nNext, errNext
	}
	if err := bw.Flush(); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), out.Sync()
}

// decryptFile reverses encryptFile; truncation or any change is detected.
func decryptFile(src, dst string, key []byte) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	br := bufio.NewReaderSize(in, 1<<20)
	hdr := make([]byte, 9+8)
	if _, err := io.ReadFull(br, hdr); err != nil || string(hdr[:4]) != fileMagic {
		return errors.New("not an encrypted magnetk file")
	}
	seg := int(binary.BigEndian.Uint32(hdr[4:8]))
	if seg <= 0 || seg > 64<<20 {
		return errors.New("bad segment size")
	}
	compressed := hdr[8]&flagDeflate != 0
	prefix := hdr[9:17]
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	aead, _ := cipher.NewGCM(block)
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		var r io.Reader = pr
		if compressed {
			r = flate.NewReader(pr)
		}
		_, err := io.Copy(out, r)
		if err == nil {
			err = out.Close()
		} else {
			out.Close()
		}
		pr.CloseWithError(err)
		done <- err
	}()
	ctLen := seg + aead.Overhead()
	cur := make([]byte, ctLen)
	nCur, errCur := io.ReadFull(br, cur)
	var derr error
	for counter := uint32(0); ; counter++ {
		if errCur != nil && errCur != io.ErrUnexpectedEOF {
			if errCur == io.EOF && counter > 0 {
				derr = errors.New("encrypted file is truncated")
			} else {
				derr = errCur
			}
			break
		}
		next := make([]byte, ctLen)
		nNext, errNext := 0, errCur
		last := errCur == io.ErrUnexpectedEOF
		if !last {
			nNext, errNext = io.ReadFull(br, next)
			last = errNext == io.EOF
		}
		pt, err := aead.Open(nil, segNonce(prefix, counter), cur[:nCur], segAAD(counter, last))
		if err != nil {
			derr = errors.New("encrypted file was modified or the key is wrong")
			break
		}
		if _, err := pw.Write(pt); err != nil {
			derr = err
			break
		}
		if last {
			break
		}
		cur, nCur, errCur = next, nNext, errNext
	}
	pw.CloseWithError(derr)
	if werr := <-done; derr == nil {
		derr = werr
	}
	if derr != nil {
		os.Remove(tmp)
		return derr
	}
	os.Remove(dst)
	return os.Rename(tmp, dst)
}

// identityLooksCompressible samples the start of f (see files.isCompressible).
func identityLooksCompressible(f *os.File, size int64) bool {
	n := int64(256 << 10)
	if size < n {
		n = size
	}
	if n < 4<<10 {
		return false
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, 0); err != nil && err != io.EOF {
		return false
	}
	var c countWriter
	zw, _ := flate.NewWriter(&c, flate.BestSpeed)
	zw.Write(buf)
	zw.Close()
	return float64(c) < 0.85*float64(n)
}

type countWriter int64

func (c *countWriter) Write(p []byte) (int, error) { *c += countWriter(len(p)); return len(p), nil }
