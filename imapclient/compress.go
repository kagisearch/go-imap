package imapclient

import (
	"bufio"
	"bytes"
	"compress/flate"
	"fmt"
	"io"
)

// Compress sends a COMPRESS DEFLATE command. Once it succeeds, everything
// sent and received over the connection is compressed.
//
// Unlike other commands, this method blocks until the command completes.
//
// This requires support for the COMPRESS=DEFLATE extension.
func (c *Client) Compress() error {
	upgradeDone := make(chan struct{})
	cmd := &compressCommand{upgradeDone: upgradeDone}
	enc := c.beginCommand("COMPRESS", cmd)
	enc.SP().Atom("DEFLATE")
	enc.flush()
	defer enc.end()

	// The client MUST NOT send further commands until the server has
	// answered and both sides compress

	if err := cmd.wait(); err != nil {
		return err
	}

	// The decoder goroutine will invoke Client.upgradeCompress, unless the
	// connection breaks before it reads the rest of the response
	select {
	case <-upgradeDone:
		return nil
	case <-c.decCh:
		if c.decErr != nil {
			return fmt.Errorf("imapclient: connection broke while enabling compression: %w", c.decErr)
		}
		return fmt.Errorf("imapclient: connection closed while enabling compression")
	}
}

// upgradeCompress switches the connection to DEFLATE after the server has sent
// an OK response. It runs in the decoder goroutine.
func (c *Client) upgradeCompress(compress *compressCommand) {
	defer close(compress.upgradeDone)

	// Drain buffered data from our bufio.Reader: the server compresses
	// everything it sends after its OK response
	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, c.br, int64(c.br.Buffered())); err != nil {
		panic(err) // unreachable
	}

	var r io.Reader = c.stream
	if buf.Len() > 0 {
		r = io.MultiReader(&buf, c.stream)
	}

	rw := c.options.wrapReadWriter(struct {
		io.Reader
		io.Writer
	}{
		Reader: flate.NewReader(r),
		Writer: flushingWriter{newFlateWriter(c.stream)},
	})

	c.br.Reset(rw)
	// As with STARTTLS, the bufio.Writer can't be re-used, it races with
	// Client.Compress
	c.bw = bufio.NewWriter(rw)
}

func newFlateWriter(w io.Writer) *flate.Writer {
	fw, err := flate.NewWriter(w, flate.DefaultCompression)
	if err != nil {
		panic(err) // unreachable: the level is valid
	}
	return fw
}

// flushingWriter flushes the compressor after each write, so the server can
// decompress a command as soon as the client is done sending it.
type flushingWriter struct {
	w *flate.Writer
}

func (fw flushingWriter) Write(b []byte) (int, error) {
	n, err := fw.w.Write(b)
	if err != nil {
		return n, err
	}
	return n, fw.w.Flush()
}

type compressCommand struct {
	commandBase

	upgradeDone chan<- struct{}
}
