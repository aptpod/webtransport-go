package webtransport

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/lucas-clemente/quic-go"
)

type Stream interface {
	io.Reader
	io.Writer
	io.Closer

	CancelRead(ErrorCode)
	CancelWrite(ErrorCode)

	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

type stream struct {
	str quic.Stream
	*receiveStream
	*sendStream
}

var _ Stream = &stream{}

func newStream(str quic.Stream) *stream {
	return &stream{
		str:           str,
		receiveStream: newReceiveStream(str),
		sendStream:    newSendStream(str),
	}
}

func (s *stream) Read(b []byte) (int, error) {
	return s.receiveStream.Read(b)
}

func (s *stream) Write(b []byte) (int, error) {
	return s.sendStream.Write(b)
}

func (s *stream) CancelRead(e ErrorCode) {
	s.receiveStream.CancelRead(e)
}

func (s *stream) CancelWrite(e ErrorCode) {
	s.sendStream.CancelWrite(e)
}

func (s *stream) Close() error {
	return maybeConvertStreamError(s.str.Close())
}

func (s *stream) SetDeadline(t time.Time) error {
	return s.str.SetDeadline(t)
}

func (s *stream) SetReadDeadline(t time.Time) error {
	return s.receiveStream.SetReadDeadline(t)
}

func (s *stream) SetWriteDeadline(t time.Time) error {
	return s.sendStream.SetWriteDeadline(t)
}

func maybeConvertStreamError(err error) error {
	if err == nil {
		return nil
	}
	var streamErr *quic.StreamError
	if errors.As(err, &streamErr) {
		errorCode, cerr := httpCodeToWebtransportCode(streamErr.ErrorCode)
		if cerr != nil {
			return fmt.Errorf("stream reset, but failed to convert stream error %d: %w", streamErr.ErrorCode, cerr)
		}
		return &StreamError{ErrorCode: errorCode}
	}
	return err
}
