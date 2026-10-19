package imapclient_test

import (
	"bufio"
	"bytes"
	"compress/flate"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestClient_Compress(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()

	body := strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit.\r\n", 2000)

	errCh := make(chan error, 1)
	go func() {
		errCh <- func() error {
			io.WriteString(serverConn, "* OK [CAPABILITY IMAP4rev1 COMPRESS=DEFLATE] ready\r\n")
			br := bufio.NewReader(serverConn)
			line, err := br.ReadString('\n')
			if err != nil {
				return err
			}
			tag, cmd, _ := strings.Cut(strings.TrimSpace(line), " ")
			if cmd != "COMPRESS DEFLATE" {
				return fmt.Errorf("got command %q, want COMPRESS DEFLATE", cmd)
			}
			io.WriteString(serverConn, tag+" OK DEFLATE active\r\n")

			zr := bufio.NewReader(flate.NewReader(br))
			zw, err := flate.NewWriter(serverConn, flate.DefaultCompression)
			if err != nil {
				return err
			}
			for _, want := range []string{"NOOP", "UID FETCH 1 (UID BODY.PEEK[])"} {
				line, err := zr.ReadString('\n')
				if err != nil {
					return err
				}
				tag, cmd, _ := strings.Cut(strings.TrimSpace(line), " ")
				if cmd != want {
					return fmt.Errorf("got command %q, want %q", cmd, want)
				}
				if want != "NOOP" {
					fmt.Fprintf(zw, "* 1 FETCH (UID 1 BODY[] {%d}\r\n%s)\r\n", len(body), body)
				}
				io.WriteString(zw, tag+" OK done\r\n")
				if err := zw.Flush(); err != nil {
					return err
				}
			}
			return nil
		}()
	}()

	client := imapclient.New(clientConn, nil)
	defer client.Close()
	if err := client.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting() = %v", err)
	}
	if !client.Caps().Has(imap.CapCompressDeflate) {
		t.Fatalf("server does not advertise COMPRESS=DEFLATE")
	}
	if err := client.Compress(); err != nil {
		t.Fatalf("Compress() = %v", err)
	}
	if err := client.Noop().Wait(); err != nil {
		t.Fatalf("Noop() = %v", err)
	}
	messages, err := client.Fetch(imap.UIDSetNum(1), &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{{Peek: true}},
	}).Collect()
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if len(messages) != 1 || len(messages[0].BodySection) != 1 {
		t.Fatalf("Fetch() = %v messages, want 1 with a body", len(messages))
	}
	if got := messages[0].BodySection[0].Bytes; !bytes.Equal(got, []byte(body)) {
		t.Errorf("body is %d bytes, want the %d sent", len(got), len(body))
	}
	if err := <-errCh; err != nil {
		t.Errorf("server: %v", err)
	}
}

func TestClient_Compress_refused(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()

	go func() {
		io.WriteString(serverConn, "* OK [CAPABILITY IMAP4rev1 COMPRESS=DEFLATE] ready\r\n")
		br := bufio.NewReader(serverConn)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			tag, _, _ := strings.Cut(line, " ")
			if strings.Contains(line, "COMPRESS") {
				io.WriteString(serverConn, tag+" NO [COMPRESSIONACTIVE] already compressed\r\n")
			} else {
				io.WriteString(serverConn, tag+" OK done\r\n")
			}
		}
	}()

	client := imapclient.New(clientConn, nil)
	defer client.Close()
	if err := client.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting() = %v", err)
	}
	if err := client.Compress(); err == nil {
		t.Fatalf("Compress() = nil, want the server's NO")
	}
	// The connection keeps working uncompressed
	if err := client.Noop().Wait(); err != nil {
		t.Fatalf("Noop() = %v", err)
	}
}
