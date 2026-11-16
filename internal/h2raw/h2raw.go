// Package h2raw sends one HTTP/2 request with exactly the header fields given, for tests
// of what a server does with requests Go's own client refuses to send (a host field that
// differs from :authority, a malformed content-length).
package h2raw

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// Response is what the server answered on the request's stream.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	Reset  bool // the stream was reset (RST_STREAM) instead of completed
}

// Do sends fields (pseudo-header fields first, in order) and body on stream 1 of a new
// TLS connection to addr, and reads the response.
func Do(addr string, tlsConf *tls.Config, fields [][2]string, body []byte) (*Response, error) {
	conf := tlsConf.Clone()
	conf.NextProtos = []string{"h2"}
	c, err := tls.Dial("tcp", addr, conf)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, http2.ClientPreface); err != nil {
		return nil, err
	}
	fr := http2.NewFramer(c, c)
	fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	if err := fr.WriteSettings(); err != nil {
		return nil, err
	}
	var hb bytes.Buffer
	enc := hpack.NewEncoder(&hb)
	for _, f := range fields {
		enc.WriteField(hpack.HeaderField{Name: f[0], Value: f[1]})
	}
	if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: hb.Bytes(),
		EndStream: body == nil, EndHeaders: true}); err != nil {
		return nil, err
	}
	if body != nil {
		if err := fr.WriteData(1, true, body); err != nil {
			return nil, err
		}
	}
	resp := &Response{Header: http.Header{}}
	for {
		f, err := fr.ReadFrame()
		if err != nil {
			return nil, err
		}
		switch f := f.(type) {
		case *http2.SettingsFrame:
			if !f.IsAck() {
				fr.WriteSettingsAck()
			}
		case *http2.MetaHeadersFrame:
			for _, hf := range f.Fields {
				if hf.Name == ":status" {
					resp.Status, _ = strconv.Atoi(hf.Value)
				} else {
					resp.Header.Add(hf.Name, hf.Value)
				}
			}
			if f.StreamEnded() {
				return resp, nil
			}
		case *http2.DataFrame:
			resp.Body = append(resp.Body, f.Data()...)
			if f.StreamEnded() {
				return resp, nil
			}
		case *http2.RSTStreamFrame:
			resp.Reset = true
			return resp, nil
		case *http2.GoAwayFrame:
			return nil, fmt.Errorf("h2raw: GOAWAY %v", f.ErrCode)
		}
	}
}
