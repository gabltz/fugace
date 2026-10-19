//go:build ignore

// check verifies what fugace serves during the pebble scenario (run.sh).
//
//	go run check.go root <pebble-mgmt-url> <minica.pem> <out.pem>
//	go run check.go tls  <addr> <sni|-> <name> <root.pem>
//	go run check.go http <url> <host> <code> [header]
package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(1)
	}
}

func pool(file string) (*x509.CertPool, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(b) {
		return nil, errors.New("no certificate in " + file)
	}
	return p, nil
}

func run(args []string) error {
	if len(args) < 4 {
		return errors.New("usage: see the header comment")
	}
	switch args[0] {
	case "root":
		mini, err := pool(args[2])
		if err != nil {
			return err
		}
		c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: mini}}}
		resp, err := c.Get(args[1] + "/roots/0")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		return os.WriteFile(args[3], b, 0o644)
	case "tls":
		if len(args) < 5 {
			return errors.New("tls: missing root")
		}
		roots, err := pool(args[4])
		if err != nil {
			return err
		}
		sni := args[2]
		if sni == "-" {
			sni = "" // like a browser connecting by IP
		}
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", args[1], &tls.Config{
			ServerName:         sni,
			InsecureSkipVerify: true, // verified below against the pebble root, whatever the SNI
		})
		if err != nil {
			return err
		}
		defer conn.Close()
		certs := conn.ConnectionState().PeerCertificates
		inter := x509.NewCertPool()
		for _, c := range certs[1:] {
			inter.AddCert(c)
		}
		leaf := certs[0]
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: args[3], Roots: roots, Intermediates: inter}); err != nil {
			return fmt.Errorf("%s (sni %q): %w", args[1], sni, err)
		}
		if len(leaf.IPAddresses) != 0 {
			return fmt.Errorf("issued certificate names an IP: %v", leaf.IPAddresses)
		}
		fmt.Printf("%x\n", leaf.SerialNumber)
		return nil
	case "http":
		code, err := strconv.Atoi(args[3])
		if err != nil {
			return err
		}
		req, err := http.NewRequest(http.MethodGet, args[1], nil)
		if err != nil {
			return err
		}
		req.Host = args[2]
		c := &http.Client{
			Timeout:       10 * time.Second,
			Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		resp, err := c.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != code {
			return fmt.Errorf("%s: status %d, want %d", args[1], resp.StatusCode, code)
		}
		if len(args) > 4 && resp.Header.Get(args[4]) == "" {
			return fmt.Errorf("%s: header %s missing", args[1], args[4])
		}
		return nil
	}
	return errors.New("unknown mode " + args[0])
}
