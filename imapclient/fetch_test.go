package imapclient_test

import (
	"bufio"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestFetch(t *testing.T) {
	client, server := newClientServerPair(t, imap.ConnStateSelected)
	defer client.Close()
	defer server.Close()

	seqSet := imap.SeqSetNum(1)
	bodySection := &imap.FetchItemBodySection{}
	fetchOptions := &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{bodySection},
	}
	messages, err := client.Fetch(seqSet, fetchOptions).Collect()
	if err != nil {
		t.Fatalf("failed to fetch first message: %v", err)
	} else if len(messages) != 1 {
		t.Fatalf("len(messages) = %v, want 1", len(messages))
	}

	msg := messages[0]
	if len(msg.BodySection) != 1 {
		t.Fatalf("len(msg.BodySection) = %v, want 1", len(msg.BodySection))
	}
	b := msg.FindBodySection(bodySection)
	if b == nil {
		t.Fatalf("FindBodySection() = nil")
	}
	body := strings.ReplaceAll(string(b), "\r\n", "\n")
	if body != simpleRawMessage {
		t.Errorf("body mismatch: got \n%v\n but want \n%v", body, simpleRawMessage)
	}
}

func TestFetch_gmailLabels(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()

	go func() {
		io.WriteString(serverConn, "* OK [CAPABILITY IMAP4rev1 X-GM-EXT-1] ready\r\n")
		br := bufio.NewReader(serverConn)
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		tag, _, _ := strings.Cut(line, " ")
		// System labels come as atoms or quoted, other labels as astrings
		// encoded like mailbox names.
		io.WriteString(serverConn, `* 1 FETCH (UID 7 X-GM-LABELS (\Inbox "\\Important" Work "Muy Importante" "Caf&AOk-"))`+"\r\n")
		io.WriteString(serverConn, "* 2 FETCH (UID 8 X-GM-LABELS ())\r\n")
		io.WriteString(serverConn, `* 3 FETCH (UID 9 X-GM-LABELS ( \Sent))`+"\r\n")
		io.WriteString(serverConn, tag+" OK done\r\n")
	}()

	client := imapclient.New(clientConn, nil)
	defer client.Close()
	if err := client.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting() = %v", err)
	}

	messages, err := client.Fetch(imap.UIDSetNum(7, 8, 9), &imap.FetchOptions{GmailLabels: true}).Collect()
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("len(messages) = %v, want 3", len(messages))
	}
	want := []string{`\Inbox`, `\Important`, "Work", "Muy Importante", "Café"}
	if got := messages[0].GmailLabels; !reflect.DeepEqual(got, want) {
		t.Errorf("GmailLabels = %q, want %q", got, want)
	}
	if got := messages[1].GmailLabels; len(got) != 0 {
		t.Errorf("GmailLabels = %q, want none", got)
	}
	if got := messages[2].GmailLabels; !reflect.DeepEqual(got, []string{`\Sent`}) {
		t.Errorf("GmailLabels = %q, want [\\Sent]", got)
	}
}

func TestFetch_gmailIDs(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()

	cmdCh := make(chan string, 1)
	go func() {
		io.WriteString(serverConn, "* OK [CAPABILITY IMAP4rev1 X-GM-EXT-1] ready\r\n")
		br := bufio.NewReader(serverConn)
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		cmdCh <- line
		tag, _, _ := strings.Cut(line, " ")
		io.WriteString(serverConn, "* 1 FETCH (UID 7 X-GM-MSGID 1278455344230334865 X-GM-THRID 18446744073709551615)\r\n")
		io.WriteString(serverConn, tag+" OK done\r\n")
	}()

	client := imapclient.New(clientConn, nil)
	defer client.Close()
	if err := client.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting() = %v", err)
	}

	messages, err := client.Fetch(imap.UIDSetNum(7), &imap.FetchOptions{GmailMsgID: true, GmailThreadID: true}).Collect()
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if cmd := <-cmdCh; !strings.Contains(cmd, "X-GM-MSGID") || !strings.Contains(cmd, "X-GM-THRID") {
		t.Errorf("command %q does not request X-GM-MSGID and X-GM-THRID", cmd)
	}
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %v, want 1", len(messages))
	}
	if got := messages[0].GmailMsgID; got != 1278455344230334865 {
		t.Errorf("GmailMsgID = %v, want 1278455344230334865", got)
	}
	if got := messages[0].GmailThreadID; got != 18446744073709551615 {
		t.Errorf("GmailThreadID = %v, want 18446744073709551615", got)
	}
}

// errAfterConn fails reads with err once the server's side has been read.
type errAfterConn struct {
	net.Conn
	err error
}

func (c errAfterConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err == io.EOF {
		err = c.err
	}
	return n, err
}

// A stream that fails inside a literal with something other than io.EOF, as a
// decompressor or a reset connection does, must report that error rather than
// "a literal is open".
func TestFetch_literalReadError(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	errBroken := errors.New("stream broken")

	go func() {
		io.WriteString(serverConn, "* OK [CAPABILITY IMAP4rev1] ready\r\n")
		br := bufio.NewReader(serverConn)
		if _, err := br.ReadString('\n'); err != nil {
			return
		}
		io.WriteString(serverConn, "* 1 FETCH (UID 1 BODY[] {100}\r\n0123456789")
		serverConn.Close()
	}()

	client := imapclient.New(errAfterConn{clientConn, errBroken}, nil)
	defer client.Close()
	if err := client.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting() = %v", err)
	}

	cmd := client.Fetch(imap.UIDSetNum(1), &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{{}},
	})
	msg := cmd.Next()
	if msg == nil {
		t.Fatalf("Next() = nil, want a message")
	}
	for item := msg.Next(); item != nil; item = msg.Next() {
		if body, ok := item.(imapclient.FetchItemDataBodySection); ok {
			if _, err := io.ReadAll(body.Literal); !errors.Is(err, errBroken) {
				t.Errorf("reading the literal = %v, want %v", err, errBroken)
			}
		}
	}
	if err := cmd.Close(); err == nil || !strings.Contains(err.Error(), errBroken.Error()) {
		t.Errorf("Close() = %v, want the stream's error", err)
	}
}

// A literal the stream breaks inside of ends early. Reading it again, as
// discarding the rest of the message does, must not touch the connection the
// client has moved on with.
func TestFetch_literalCutShort(t *testing.T) {
	clientConn, serverConn := net.Pipe()

	go func() {
		io.WriteString(serverConn, "* OK [CAPABILITY IMAP4rev1] ready\r\n")
		br := bufio.NewReader(serverConn)
		if _, err := br.ReadString('\n'); err != nil {
			return
		}
		io.WriteString(serverConn, "* 1 FETCH (UID 1 BODY[] {100}\r\n0123456789")
		serverConn.Close()
	}()

	client := imapclient.New(clientConn, nil)
	defer client.Close()
	if err := client.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting() = %v", err)
	}

	cmd := client.Fetch(imap.UIDSetNum(1), &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{{}},
	})
	msg := cmd.Next()
	if msg == nil {
		t.Fatalf("Next() = nil, want a message")
	}
	for item := msg.Next(); item != nil; item = msg.Next() {
		if body, ok := item.(imapclient.FetchItemDataBodySection); ok {
			b, _ := io.ReadAll(body.Literal)
			if len(b) >= 100 {
				t.Errorf("read %d bytes of a literal cut at 10", len(b))
			}
		}
	}
	if err := cmd.Close(); err == nil {
		t.Errorf("Close() = nil, want the broken stream's error")
	}
}
