package webtransport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/lucas-clemente/quic-go"
	"github.com/lucas-clemente/quic-go/http3"
	"github.com/lucas-clemente/quic-go/quicvarint"
)

type Dialer struct {
	// TLSClientConfig specifies the TLS configuration to use.
	// If nil, the default configuration is used.
	TLSClientConf *tls.Config

	// DialFunc specifies an optional dial function for creating QUIC connections.
	// If DialFunc is nil, quic.DialAddrEarlyContext will be used.
	DialFunc func(ctx context.Context, addr string, tlsCfg *tls.Config, cfg *quic.Config) (quic.EarlyConnection, error)

	// StreamReorderingTime is the time an incoming WebTransport stream that cannot be associated
	// with a session is buffered.
	// This can happen if the response to a CONNECT request (that creates a new session) is reordered,
	// and arrives after the first WebTransport stream(s) for that session.
	// Defaults to 5 seconds.
	StreamReorderingTimeout time.Duration

	ctx       context.Context
	ctxCancel context.CancelFunc

	initOnce     sync.Once
	roundTripper *http3.RoundTripper

	conns sessionManager
}

func (d *Dialer) init() {
	timeout := d.StreamReorderingTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	d.conns = *newSessionManager(timeout)
	d.ctx, d.ctxCancel = context.WithCancel(context.Background())
	d.roundTripper = &http3.RoundTripper{
		TLSClientConfig:    d.TLSClientConf,
		QuicConfig:         &quic.Config{MaxIncomingStreams: 100, MaxIncomingUniStreams: 100},
		Dial:               d.DialFunc,
		EnableDatagrams:    true,
		AdditionalSettings: map[uint64]uint64{settingsEnableWebtransport: 1},
		StreamHijacker: func(ft http3.FrameType, conn quic.Connection, str quic.Stream) (hijacked bool, err error) {
			if ft != webTransportFrameType {
				return false, nil
			}
			id, err := quicvarint.Read(quicvarint.NewReader(str))
			if err != nil {
				return false, err
			}
			d.conns.AddStream(conn, str, sessionID(id))
			return true, nil
		},
		UniStreamHijacker: func(st http3.StreamType, conn quic.Connection, str quic.ReceiveStream) (hijacked bool) {
			if st != webTransportStreamType {
				return false
			}
			id, err := quicvarint.Read(quicvarint.NewReader(str))
			if err != nil {
				return false
			}
			d.conns.AddUniStream(conn, str, sessionID(id))
			return true
		},
	}
}

func (d *Dialer) Dial(ctx context.Context, urlStr string, reqHdr http.Header) (*http.Response, *Conn, error) {
	d.initOnce.Do(func() { d.init() })

	u, err := url.Parse(urlStr)
	if err != nil {
		return nil, nil, err
	}
	if reqHdr == nil {
		reqHdr = http.Header{}
	}
	reqHdr.Add(webTransportDraftOfferHeaderKey, "1")
	req := &http.Request{
		Method: http.MethodConnect,
		Header: reqHdr,
		Proto:  "webtransport",
		Host:   u.Host,
		URL:    u,
	}
	req = req.WithContext(ctx)

	rsp, err := d.roundTripper.RoundTripOpt(req, http3.RoundTripOpt{})
	if err != nil {
		return nil, nil, err
	}
	if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
		return rsp, nil, fmt.Errorf("received status %d", rsp.StatusCode)
	}
	hijacker, ok := rsp.Body.(http3.Hijacker)
	if !ok { // should never happen, unless quic-go changed the API
		return nil, nil, errors.New("failed to hijack")
	}
	qconn := hijacker.StreamCreator()
	id := sessionID(rsp.Body.(streamIDGetter).StreamID())
	conn := newConn(hijacker.Stream().Context(), id, qconn, rsp.Body)
	d.conns.AddSession(qconn, id, conn)
	return rsp, conn, nil
}

func (d *Dialer) Close() error {
	d.ctxCancel()
	return nil
}
