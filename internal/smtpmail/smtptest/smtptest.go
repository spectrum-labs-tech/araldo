// SPDX-License-Identifier: AGPL-3.0-or-later

// Package smtptest is an SMTP server for tests: it accepts mail on
// 127.0.0.1, optionally over TLS (implicit or STARTTLS) and with a
// password, and keeps what it receives.
package smtptest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

// Mail is one message the server received.
type Mail struct {
	From string
	To   []string
	// Message is the parsed message; Raw its bytes.
	Message *mail.Message
	Raw     []byte
	// TLS reports whether it came over TLS; Auth is the username it signed
	// in with.
	TLS  bool
	Auth string
}

// Server is a running test server.
type Server struct {
	// Host and Port are where it listens.
	Host string
	Port int
	// Username and Password, when set, are required.
	Username, Password string
	// RejectRcpt, when set, answers RCPT TO with this reply ("550 no such
	// user", "451 try later").
	RejectRcpt string

	ln      net.Listener
	tlsConf *tls.Config
	client  *tls.Config
	mode    string

	mu   sync.Mutex
	mail []Mail
}

// Modes the server speaks.
const (
	Plain    = "none"
	StartTLS = "starttls"
	Implicit = "tls"
)

// New starts a server in mode, closed when t ends.
func New(t testing.TB, mode string) *Server {
	t.Helper()
	cert, pool := selfSigned(t)
	s := &Server{Host: "127.0.0.1", mode: mode, tlsConf: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	if mode == Implicit {
		s.ln = tls.NewListener(ln, s.tlsConf)
	}
	s.Port = s.ln.Addr().(*net.TCPAddr).Port
	s.client = &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
	go s.serve()
	t.Cleanup(func() { _ = s.ln.Close() })
	return s
}

// ClientTLS is a TLS configuration that trusts the server.
func (s *Server) ClientTLS() *tls.Config { return s.client.Clone() }

// Mail returns what the server received so far.
func (s *Server) Mail() []Mail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Mail(nil), s.mail...)
}

func (s *Server) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.session(conn)
	}
}

func (s *Server) session(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	_, isTLS := conn.(*tls.Conn)
	tp := textproto.NewConn(conn)
	reply := func(line string) { _ = tp.PrintfLine("%s", line) }
	reply("220 smtptest ready")
	var cur Mail
	authed := s.Username == ""
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			lines := []string{"250-smtptest"}
			if s.mode == StartTLS && !isTLS {
				lines = append(lines, "250-STARTTLS")
			}
			lines = append(lines, "250 AUTH PLAIN")
			for _, l := range lines {
				reply(l)
			}
		case "STARTTLS":
			if s.mode != StartTLS || isTLS {
				reply("503 not now")
				continue
			}
			reply("220 go ahead")
			tc := tls.Server(conn, s.tlsConf)
			hctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := tc.HandshakeContext(hctx)
			cancel()
			if err != nil {
				return
			}
			conn, isTLS = tc, true
			tp = textproto.NewConn(conn)
		case "AUTH":
			parts := strings.Fields(arg)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "PLAIN") {
				reply("504 PLAIN only")
				continue
			}
			raw, _ := base64.StdEncoding.DecodeString(parts[1])
			fields := strings.Split(string(raw), "\x00")
			if len(fields) != 3 || fields[1] != s.Username || fields[2] != s.Password {
				reply("535 authentication failed")
				continue
			}
			authed, cur.Auth = true, fields[1]
			reply("235 ok")
		case "MAIL":
			if !authed {
				reply("530 sign in first")
				continue
			}
			cur.From, cur.To = addr(arg), nil
			reply("250 ok")
		case "RCPT":
			if s.RejectRcpt != "" {
				reply(s.RejectRcpt)
				continue
			}
			cur.To = append(cur.To, addr(arg))
			reply("250 ok")
		case "DATA":
			reply("354 send it")
			raw, err := io.ReadAll(tp.DotReader())
			if err != nil {
				return
			}
			cur.Raw, cur.TLS = raw, isTLS
			cur.Message, _ = mail.ReadMessage(strings.NewReader(string(raw)))
			s.mu.Lock()
			s.mail = append(s.mail, cur)
			s.mu.Unlock()
			cur = Mail{Auth: cur.Auth}
			reply("250 queued")
		case "RSET":
			cur = Mail{Auth: cur.Auth}
			reply("250 ok")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 unknown")
		}
	}
}

// addr pulls the address out of "FROM:<a@b>" or "TO:<a@b>".
func addr(arg string) string {
	if i, j := strings.IndexByte(arg, '<'), strings.IndexByte(arg, '>'); i >= 0 && j > i {
		return arg[i+1 : j]
	}
	return arg
}

// selfSigned makes a certificate for 127.0.0.1 and a pool trusting it.
func selfSigned(t testing.TB) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smtptest"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}
