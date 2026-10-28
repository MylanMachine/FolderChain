package engine

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/gob"
	"errors"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	mIndex uint8 = iota + 1
	mRequest
	mData
	mPing
	mPong
)

const chunkSize = 256 << 10

// Message is the single wire type; gob omits the fields that aren't set.
type Message struct {
	Kind  uint8
	Files []FileInfo
	Final bool
	Req   *Request
	Data  *DataChunk
	Stamp int64
}

type Request struct {
	ID     uint64
	Path   string
	Hash   []byte
	Offset int64
}

type DataChunk struct {
	ID     uint64
	Offset int64
	Bytes  []byte
	EOF    bool
	Err    string
}

const (
	pingEvery   = 5 * time.Second
	deadAfter   = 20 * time.Second
	writeTimout = 30 * time.Second
)

type peerConn struct {
	s        *session
	tc       *tls.Conn
	id, name string
	port     int
	outbound bool

	wmu sync.Mutex
	bw  *bufio.Writer
	enc *gob.Encoder

	lastRecv atomic.Int64
	rtt      atomic.Int64
	pongTo   atomic.Int64

	done     chan struct{}
	once     sync.Once
	sendKick chan struct{}
	pongKick chan struct{}

	indexReceived bool   // guarded by Engine.mu
	sentSeq       uint64 // owned by sendLoop
	initialSent   bool   // owned by sendLoop

	dmu       sync.Mutex
	nextReq   uint64
	downloads map[uint64]chan *DataChunk
	upSem     chan struct{}
}

func newPeerConn(s *session, tc *tls.Conn, h *authHello, outbound bool) *peerConn {
	pc := &peerConn{
		s: s, tc: tc, id: h.ID, name: h.Name, port: h.Port, outbound: outbound,
		done:      make(chan struct{}),
		sendKick:  make(chan struct{}, 1),
		pongKick:  make(chan struct{}, 1),
		downloads: map[uint64]chan *DataChunk{},
		upSem:     make(chan struct{}, 4),
	}
	if pc.name == "" {
		pc.name = h.ID
	}
	pc.bw = bufio.NewWriterSize(tc, 64<<10)
	pc.enc = gob.NewEncoder(pc.bw)
	pc.lastRecv.Store(time.Now().UnixNano())
	return pc
}

func (pc *peerConn) remoteIP() string { return hostOf(pc.tc.RemoteAddr().String()) }

func (pc *peerConn) lastRecvTime() time.Time { return time.Unix(0, pc.lastRecv.Load()) }

func (pc *peerConn) start() {
	go pc.readLoop()
	go pc.sendLoop()
}

func (pc *peerConn) close() {
	pc.once.Do(func() {
		close(pc.done)
		pc.tc.Close()
		pc.s.unregister(pc)
	})
}

func (pc *peerConn) kickSend() {
	select {
	case pc.sendKick <- struct{}{}:
	default:
	}
}

func (pc *peerConn) send(m *Message) error {
	pc.wmu.Lock()
	defer pc.wmu.Unlock()
	select {
	case <-pc.done:
		return errors.New("connection closed")
	default:
	}
	pc.tc.SetWriteDeadline(time.Now().Add(writeTimout))
	err := pc.enc.Encode(m)
	if err == nil {
		err = pc.bw.Flush()
	}
	if err != nil {
		go pc.close()
	}
	return err
}

// readLoop never writes to the connection itself (that could deadlock two
// devices that are both uploading); replies are handed to other goroutines.
func (pc *peerConn) readLoop() {
	defer pc.close()
	dec := gob.NewDecoder(bufio.NewReaderSize(pc.tc, 64<<10))
	for {
		pc.tc.SetReadDeadline(time.Now().Add(deadAfter + 10*time.Second))
		var m Message
		if err := dec.Decode(&m); err != nil {
			return
		}
		pc.lastRecv.Store(time.Now().UnixNano())
		switch m.Kind {
		case mIndex:
			pc.s.onIndex(pc, m.Files, m.Final)
		case mRequest:
			if m.Req != nil {
				go pc.serve(m.Req)
			}
		case mData:
			if m.Data != nil {
				pc.deliver(m.Data)
			}
		case mPing:
			pc.pongTo.Store(m.Stamp)
			select {
			case pc.pongKick <- struct{}{}:
			default:
			}
		case mPong:
			pc.rtt.Store(time.Now().UnixNano() - m.Stamp)
		}
	}
}

func (pc *peerConn) sendLoop() {
	defer pc.close()
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	if pc.sendIndex() != nil {
		return
	}
	for {
		select {
		case <-pc.done:
			return
		case <-t.C:
			if time.Since(pc.lastRecvTime()) > deadAfter {
				pc.s.e.log.Printf("%s: no response for %v, dropping connection", pc.name, deadAfter)
				return
			}
			if pc.send(&Message{Kind: mPing, Stamp: time.Now().UnixNano()}) != nil {
				return
			}
		case <-pc.pongKick:
			if pc.send(&Message{Kind: mPong, Stamp: pc.pongTo.Load()}) != nil {
				return
			}
		case <-pc.sendKick:
			if pc.sendIndex() != nil {
				return
			}
		}
	}
}

// sendIndex sends every local entry changed since the last send. The first
// call sends the whole index and ends with a Final marker.
func (pc *peerConn) sendIndex() error {
	for {
		e := pc.s.e
		e.mu.Lock()
		var batch []FileInfo
		for _, f := range pc.s.idx.Files {
			if f.Seq > pc.sentSeq {
				batch = append(batch, *cloneInfo(f))
			}
		}
		e.mu.Unlock()
		if len(batch) == 0 {
			break
		}
		sort.Slice(batch, func(i, j int) bool { return batch[i].Seq < batch[j].Seq })
		if len(batch) > 500 {
			batch = batch[:500]
		}
		if err := pc.send(&Message{Kind: mIndex, Files: batch}); err != nil {
			return err
		}
		pc.sentSeq = batch[len(batch)-1].Seq
	}
	if !pc.initialSent {
		pc.initialSent = true
		return pc.send(&Message{Kind: mIndex, Final: true})
	}
	return nil
}

// ---- downloads (we are the receiver) ----

func (pc *peerConn) request(path string, hash []byte, offset int64) (uint64, chan *DataChunk, error) {
	pc.dmu.Lock()
	pc.nextReq++
	id := pc.nextReq
	ch := make(chan *DataChunk, 64)
	pc.downloads[id] = ch
	pc.dmu.Unlock()
	err := pc.send(&Message{Kind: mRequest, Req: &Request{ID: id, Path: path, Hash: hash, Offset: offset}})
	if err != nil {
		pc.endRequest(id)
	}
	return id, ch, err
}

func (pc *peerConn) endRequest(id uint64) {
	pc.dmu.Lock()
	delete(pc.downloads, id)
	pc.dmu.Unlock()
}

func (pc *peerConn) deliver(d *DataChunk) {
	pc.dmu.Lock()
	ch := pc.downloads[d.ID]
	pc.dmu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- d:
	case <-pc.done:
	case <-time.After(deadAfter):
		// receiver gave up on this request; drop it
		pc.endRequest(d.ID)
	}
}

// ---- uploads (we are the sender) ----

func (pc *peerConn) serve(r *Request) {
	select {
	case pc.upSem <- struct{}{}:
	case <-pc.done:
		return
	}
	defer func() { <-pc.upSem }()
	fail := func(msg string) { pc.send(&Message{Kind: mData, Data: &DataChunk{ID: r.ID, Err: msg}}) }

	s, e := pc.s, pc.s.e
	e.mu.Lock()
	f := s.idx.Files[r.Path]
	ok := f != nil && !f.Deleted && !f.Dir && bytes.Equal(f.Hash, r.Hash) && validPath(r.Path)
	var size int64
	key := "up:" + pc.id + ":" + r.Path
	if ok {
		size = f.Size
		s.transfers[key] = &Transfer{Path: r.Path, Size: size, Done: r.Offset, Peer: pc.name, Upload: true, Started: time.Now()}
	}
	e.mu.Unlock()
	if !ok {
		fail("file no longer available")
		return
	}
	defer func() {
		e.mu.Lock()
		delete(s.transfers, key)
		e.mu.Unlock()
	}()

	fh, err := os.Open(absPath(s.root, r.Path))
	if err != nil {
		fail(err.Error())
		return
	}
	defer fh.Close()
	if _, err := fh.Seek(r.Offset, io.SeekStart); err != nil {
		fail(err.Error())
		return
	}
	off := r.Offset
	for {
		buf := make([]byte, chunkSize)
		n, err := io.ReadFull(fh, buf)
		if n > 0 {
			if pc.send(&Message{Kind: mData, Data: &DataChunk{ID: r.ID, Offset: off, Bytes: buf[:n]}}) != nil {
				return
			}
			off += int64(n)
			e.mu.Lock()
			if t := s.transfers[key]; t != nil {
				t.Done = off
			}
			e.mu.Unlock()
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			pc.send(&Message{Kind: mData, Data: &DataChunk{ID: r.ID, Offset: off, EOF: true}})
			e.mu.Lock()
			e.eventLocked("out", "Sent %s to %s", r.Path, pc.name)
			e.mu.Unlock()
			return
		}
		if err != nil {
			fail(err.Error())
			return
		}
	}
}
