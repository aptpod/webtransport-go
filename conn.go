package webtransport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/lucas-clemente/quic-go"
	"github.com/lucas-clemente/quic-go/http3"
	"github.com/lucas-clemente/quic-go/quicvarint"
	"github.com/marten-seemann/webtransport-go/internal/logging"
)

// sessionID is the WebTransport Session ID
type sessionID uint64

type Conn struct {
	logger     logging.Logger
	sessionID  sessionID
	qconn      http3.StreamCreator
	requestStr io.Closer

	ctx       context.Context
	ctxCancel context.CancelFunc

	acceptMx   sync.Mutex
	acceptChan chan struct{}
	// Contains all the streams waiting to be accepted.
	// There's no explicit limit to the length of the queue, but it is implicitly
	// limited by the stream flow control provided by QUIC.
	acceptQueue []quic.Stream

	acceptUniMx   sync.Mutex
	acceptUniChan chan struct{}
	// Contains all the streams waiting to be accepted.
	// There's no explicit limit to the length of the queue, but it is implicitly
	// limited by the stream flow control provided by QUIC.
	acceptUniQueue []quic.ReceiveStream

	rcvDatagramQueue chan []byte
}

func newConn(ctx context.Context, sessionID sessionID, qconn http3.StreamCreator, requestStr io.Closer) *Conn {
	c := &Conn{
		logger:           logging.DefaultLogger.WithPrefix("conn"),
		sessionID:        sessionID,
		qconn:            qconn,
		requestStr:       requestStr,
		acceptChan:       make(chan struct{}, 1),
		acceptUniChan:    make(chan struct{}, 1),
		rcvDatagramQueue: make(chan []byte, 128),
	}
	c.ctx, c.ctxCancel = context.WithCancel(ctx)

	return c
}

func (c *Conn) addStream(str quic.Stream) {
	c.acceptMx.Lock()
	defer c.acceptMx.Unlock()

	c.acceptQueue = append(c.acceptQueue, str)
	select {
	case c.acceptChan <- struct{}{}:
	default:
	}
}

func (c *Conn) addUniStream(str quic.ReceiveStream) {
	c.acceptUniMx.Lock()
	defer c.acceptUniMx.Unlock()

	c.acceptUniQueue = append(c.acceptUniQueue, str)
	select {
	case c.acceptUniChan <- struct{}{}:
	default:
	}
}

// Context returns a context that is closed when the connection is closed.
func (c *Conn) Context() context.Context {
	return c.ctx
}

func (c *Conn) AcceptStream(ctx context.Context) (Stream, error) {
	var str quic.Stream
	c.acceptMx.Lock()
	if len(c.acceptQueue) > 0 {
		str = c.acceptQueue[0]
		c.acceptQueue = c.acceptQueue[1:]
	}
	c.acceptMx.Unlock()
	if str != nil {
		return newStream(str), nil
	}

	select {
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.acceptChan:
		return c.AcceptStream(ctx)
	}
}

func (c *Conn) AcceptUniStream(ctx context.Context) (ReceiveStream, error) {
	var str quic.ReceiveStream
	c.acceptUniMx.Lock()
	if len(c.acceptUniQueue) > 0 {
		str = c.acceptUniQueue[0]
		c.acceptUniQueue = c.acceptUniQueue[1:]
	}
	c.acceptUniMx.Unlock()
	if str != nil {
		return newReceiveStream(str), nil
	}

	select {
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.acceptUniChan:
		return c.AcceptUniStream(ctx)
	}
}

func (c *Conn) OpenStream() (Stream, error) {
	str, err := c.qconn.OpenStream()
	if err != nil {
		return nil, err
	}
	if err := c.writeStreamHeader(str); err != nil {
		return nil, err
	}
	return newStream(str), nil
}

func (c *Conn) OpenStreamSync(ctx context.Context) (Stream, error) {
	str, err := c.qconn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	// TODO: this should probably respect the context
	if err := c.writeStreamHeader(str); err != nil {
		return nil, err
	}
	return newStream(str), nil
}

func (c *Conn) OpenUniStream() (SendStream, error) {
	str, err := c.qconn.OpenUniStream()
	if err != nil {
		return nil, err
	}
	if err := c.writeUniStreamHeader(str); err != nil {
		return nil, err
	}
	return newSendStream(str), nil
}

func (c *Conn) OpenUniStreamSync(ctx context.Context) (SendStream, error) {
	str, err := c.qconn.OpenUniStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.writeUniStreamHeader(str); err != nil {
		return nil, err
	}
	return newSendStream(str), nil
}

func (c *Conn) writeStreamHeader(str quic.SendStream) error {
	buf := bytes.NewBuffer(make([]byte, 0, 10)) // 2 bytes for the frame type, up to 8 bytes for the session ID
	quicvarint.Write(buf, uint64(webTransportFrameType))
	quicvarint.Write(buf, uint64(c.sessionID))
	_, err := str.Write(buf.Bytes())
	return err
}

func (c *Conn) writeUniStreamHeader(str quic.SendStream) error {
	buf := bytes.NewBuffer(make([]byte, 0, 10)) // 2 bytes for the stream type, up to 8 bytes for the session ID
	quicvarint.Write(buf, uint64(webTransportStreamType))
	quicvarint.Write(buf, uint64(c.sessionID))
	_, err := str.Write(buf.Bytes())
	return err
}

func (c *Conn) LocalAddr() net.Addr {
	return c.qconn.LocalAddr()
}

func (c *Conn) RemoteAddr() net.Addr {
	return c.qconn.RemoteAddr()
}

func (c *Conn) SendMessage(b []byte) error {
	buf := &bytes.Buffer{}
	quicvarint.Write(buf, uint64(c.sessionID))
	buf.Write(b)
	return c.qconn.SendMessage(buf.Bytes())
}

func (c *Conn) ReceiveMessage() ([]byte, error) {
	select {
	case data := <-c.rcvDatagramQueue:
		return data, nil
	case <-c.ctx.Done():
		return nil, errors.New("conn closed")
	}
}

func (c *Conn) handleDatagram(data []byte) {
	select {
	case c.rcvDatagramQueue <- data:
	default:
		c.logger.Infof("Discarding DATAGRAM frame (%d bytes payload)", len(data))
	}
}

func (c *Conn) Close() error {
	c.ctxCancel()
	return c.requestStr.Close()
}
