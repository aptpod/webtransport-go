package webtransport

import (
	"io"
	"time"

	"github.com/lucas-clemente/quic-go"
)

type SendStream interface {
	io.Writer
	io.Closer

	CancelWrite(ErrorCode)
	SetWriteDeadline(time.Time) error
}

type sendStream struct {
	str quic.SendStream
}

var _ SendStream = &sendStream{}

func newSendStream(str quic.SendStream) *sendStream {
	return &sendStream{str}
}

func (s *sendStream) Write(b []byte) (int, error) {
	n, err := s.str.Write(b)
	return n, maybeConvertStreamError(err)
}

func (s *sendStream) Close() error {
	return s.str.Close()
}

func (s *sendStream) CancelWrite(e ErrorCode) {
	s.str.CancelWrite(webtransportCodeToHTTPCode(e))
}

func (s *sendStream) SetWriteDeadline(t time.Time) error {
	return maybeConvertStreamError(s.str.SetWriteDeadline(t))
}
