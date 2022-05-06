package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"golang.org/x/sync/errgroup"

	"github.com/lucas-clemente/quic-go/http3"
	"github.com/marten-seemann/webtransport-go"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if len(os.Args) != 3 {
		return errors.New("usage: cmd/echo cert.pem key.pem")
	}
	cert := os.Args[1]
	key := os.Args[2]
	log.Printf("cert[%s], key[%s]", cert, key)

	s := &webtransport.Server{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
		H3: http3.Server{
			Server: &http.Server{
				Addr: "[::1]:4433",
				TLSConfig: &tls.Config{
					Certificates: []tls.Certificate{},
					NextProtos:   []string{"h3"},
				},
			},
		},
	}
	setHandler(s, newEchoHandler())

	return s.ListenAndServeTLS(cert, key)
}

func setHandler(s *webtransport.Server, connHandler func(*webtransport.Conn)) {
	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		log.Print("in handler")
		conn, err := s.Upgrade(w, r)
		if err != nil {
			log.Printf("upgrading failed: %s", err)
			w.WriteHeader(500)
			return
		}

		log.Print("upgrade done")
		connHandler(conn)
	})
	s.H3.Handler = mux
}

func newEchoHandler() func(*webtransport.Conn) {
	return func(conn *webtransport.Conn) {
		log.Print("opened a conn")

		defer conn.Close()
		eg, ctx := errgroup.WithContext(conn.Context())

		eg.Go(func() error {
			for {
				log.Print("trying to accept stream")
				str, err := conn.AcceptStream(ctx)
				if err != nil {
					return fmt.Errorf("failed to accespt stream: %w", err)
				}
				log.Print("accepted a stream")

				got, err := io.ReadAll(str)
				if err != nil {
					return fmt.Errorf("failed to read from stream: %w", err)
				}
				log.Printf("got by stream: %q", string(got))
				if _, err := str.Write(got); err != nil {
					return fmt.Errorf("stream echo failed: %w", err)
				}
				if err := str.Close(); err != nil {
					return fmt.Errorf("failed to close stream: %s", err)
				}
				log.Printf("bidi stream echo done")
			}
		})

		eg.Go(func() error {
			for {
				log.Print("trying to accept unistream")
				str, err := conn.AcceptUniStream(ctx)
				if err != nil {
					return fmt.Errorf("failed to accespt unistream: %w", err)
				}
				log.Print("accepted an unistream")
				got, err := io.ReadAll(str)
				if err != nil {
					return fmt.Errorf("failed to read from unistream: %w", err)
				}
				log.Printf("got by unistream: %q", string(got))
				str.CancelRead(webtransport.ErrorCode(uint8(0)))

				sender, err := conn.OpenUniStream()
				if err != nil {
					return fmt.Errorf("failed to open unistream: %w", err)
				}
				if _, err := sender.Write(got); err != nil {
					return fmt.Errorf("unistream echo failed: %w", err)
				}
				log.Printf("uni stream echo done")
				if err := sender.Close(); err != nil {
					return fmt.Errorf("failed to close unistream: %s", err)
				}
			}
		})

		eg.Go(func() error {
			for {
				log.Print("trying to read a datagram message")
				msg, err := conn.ReceiveMessage()
				if err != nil {
					return fmt.Errorf("failed to read a datagram message: %w", err)
				}
				log.Printf("got a datagram message: %q", string(msg))
				if err := conn.SendMessage(msg); err != nil {
					return fmt.Errorf("failed to send a datagram message: %w", err)
				}
				log.Printf("datagram echo done")
			}
		})

		if err := eg.Wait(); err != nil {
			log.Printf("connection closed with error: %s", err)
			return
		}
		log.Printf("connection closed")
	}
}
