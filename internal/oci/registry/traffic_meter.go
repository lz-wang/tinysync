package registry

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

type trafficMeter struct {
	bytes int64
}

func newTrafficMeter() *trafficMeter {
	return &trafficMeter{}
}

func (m *trafficMeter) add(n int64) {
	atomic.AddInt64(&m.bytes, n)
}

func (m *trafficMeter) bytesTransferred() int64 {
	return atomic.LoadInt64(&m.bytes)
}

type meterTransport struct {
	base  http.RoundTripper
	meter *trafficMeter
}

func (t *meterTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.meter != nil && shouldMeterRequest(req) {
		req = req.Clone(req.Context())
		req.Body = &countingReadCloser{
			ReadCloser: req.Body,
			onRead: func(n int) {
				t.meter.add(int64(n))
			},
		}
	}
	return t.base.RoundTrip(req)
}

type countingReadCloser struct {
	io.ReadCloser
	onRead func(n int)
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if n > 0 && c.onRead != nil {
		c.onRead(n)
	}
	return n, err
}

func shouldMeterRequest(req *http.Request) bool {
	if req == nil || req.Body == nil {
		return false
	}

	switch req.Method {
	case http.MethodPost, http.MethodPatch, http.MethodPut:
	default:
		return false
	}

	path := req.URL.Path
	return strings.Contains(path, "/blobs/uploads") ||
		strings.Contains(path, "/manifests/") ||
		strings.Contains(path, "/blobs/")
}
