package webtransport

import (
	"io"
	"time"

	"github.com/lucas-clemente/quic-go"
)

type ReceiveStream interface {
	io.Reader

	CancelRead(ErrorCode)
	SetReadDeadline(time.Time) error
}

type receiveStream struct {
	str quic.ReceiveStream
}

var _ ReceiveStream = &receiveStream{}

func newReceiveStream(str quic.ReceiveStream) *receiveStream {
	return &receiveStream{str}
}

func (s *receiveStream) Read(b []byte) (int, error) {
	n, err := s.str.Read(b)
	return n, maybeConvertStreamError(err)
}

func (s *receiveStream) CancelRead(e ErrorCode) {
	s.str.CancelRead(webtransportCodeToHTTPCode(e))
}

func (s *receiveStream) SetReadDeadline(t time.Time) error {
	return maybeConvertStreamError(s.str.SetReadDeadline(t))
}
