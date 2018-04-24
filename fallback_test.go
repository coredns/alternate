package fallback

import (
	"fmt"
	"testing"

	"golang.org/x/net/context"

	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/pkg/dnstest"
	"github.com/coredns/coredns/plugin/pkg/healthcheck"
	"github.com/coredns/coredns/plugin/proxy"
	"github.com/coredns/coredns/plugin/test"
	"github.com/coredns/coredns/request"

	"github.com/miekg/dns"
)

// stubNextHandler is used to simulate a rewrite and proxy plugin.
// It returns a stub Handler that returns the rcode and err specified when invoked.
// Also it adds edns0 option to given request.
func stubNextHandler(rcode int, err error) test.Handler {
	return test.HandlerFunc(func(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
		return_code := rcode
		if r == nil {
			r = &dns.Msg{}
		}
		r.SetEdns0(4096, false)
		if rcode != dns.RcodeServerFailure {
			r.MsgHdr.Rcode = rcode
			return_code = dns.RcodeSuccess
			w.WriteMsg(r)
		} else {
			w.WriteMsg(nil)
		}
		return return_code, err
	})
}

// testProxyCreator implements the proxyCreator interface.
// Used by unit test to verify that the proxyCreator.New() method is called as expected
type testProxyCreator struct {
	expectedUpstream proxy.Upstream
	called           int
	t                *testing.T
	handler          *dummyHandler
}

func (c *testProxyCreator) New(trace plugin.Handler, upstream proxy.Upstream) plugin.Handler {
	c.called++

	// Ensure that it is called with the expected Upstream
	if c.expectedUpstream != nil && c.expectedUpstream != upstream {
		c.t.Errorf("Expected upstream passed to proxyCreator is '%v', but got '%v'",
			c.expectedUpstream, upstream)

		return nil
	}

	if c.handler != nil {
		return c.handler
	}
	return &proxy.Proxy{Trace: trace, Upstreams: &[]proxy.Upstream{upstream}}
}

// dummyHandler implements the plugin.Handler interface
// Saves last dns.Msg passed to serveDNS() call
type dummyHandler struct {
	lastRequest *dns.Msg
}

func (h *dummyHandler) Name() string { return "dummyHandler" }
func (h *dummyHandler) ServeDNS(c context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	h.lastRequest = r
	return 0, nil
}

// dummyUpstream implements the proxy.Upstream interface
// It is used to fake an upstream used for creating a proxy.
type dummyUpstream struct {
	rcode int
}

func (u dummyUpstream) From() string { return "" }

func (u dummyUpstream) Select() *healthcheck.UpstreamHost { return nil }

func (u dummyUpstream) IsAllowedDomain(s string) bool { return false }

func (u dummyUpstream) Exchanger() proxy.Exchanger { return dummyExchanger{} }

func (u dummyUpstream) Stop() error { return nil }

// dummyExchanger implements the proxy.Exchanger interface
// It is used solely to implement the dummyUpstream above
type dummyExchanger struct{}

func (e dummyExchanger) Exchange(ctx context.Context, addr string, state request.Request) (*dns.Msg, error) {
	return &dns.Msg{}, nil
}

func (e dummyExchanger) Protocol() string { return "" }

func (e dummyExchanger) Transport() string { return "" }

func (e dummyExchanger) OnStartup(p *proxy.Proxy) error { return nil }

func (e dummyExchanger) OnShutdown(p *proxy.Proxy) error { return nil }

// Test case for fallback
type fallbackTestCase struct {
	rcode            int            // rcode to be returned by the stub Handler
	expectedUpstream proxy.Upstream // this upstream is expected when testProxyCreator is called
	original         bool           // fallback block is configured to use original query
}

func TestFallback(t *testing.T) {
	// dummy Upstreams for servicing a specific rcode
	dummyRefusedUpstream := &dummyUpstream{rcode: dns.RcodeRefused}
	dummyServeFailureUpstream := &dummyUpstream{rcode: dns.RcodeServerFailure}
	dummyNxDomainUpstream := &dummyUpstream{rcode: dns.RcodeNameError}
	dummyUpstreams := []*dummyUpstream{
		dummyRefusedUpstream,
		dummyServeFailureUpstream,
		dummyNxDomainUpstream,
	}

	testCases := []fallbackTestCase{
		{
			rcode:            dns.RcodeRefused,
			expectedUpstream: dummyRefusedUpstream,
			original:         false,
		},
		{
			rcode:            dns.RcodeServerFailure,
			expectedUpstream: dummyServeFailureUpstream,
			original:         false,
		},
		{
			rcode:            dns.RcodeNameError,
			expectedUpstream: dummyNxDomainUpstream,
			original:         true,
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("rcode = %d", tc.rcode), func(t *testing.T) {
			handler := New(nil)
			// One of rules has "original" flag set
			handler.original = true
			// create stub handler to return the test rcode
			handler.Next = stubNextHandler(tc.rcode, nil)
			// add dummyUpstreams to upstream map according to the rcode field
			for _, u := range dummyUpstreams {
				handler.rules[u.rcode] = rule{original: tc.original, proxyUpstream: u}
			}
			proxyCreator := &testProxyCreator{
				t:                t,
				expectedUpstream: tc.expectedUpstream,
				handler:          &dummyHandler{}}
			handler.proxy = proxyCreator

			ctx := context.TODO()
			req := &dns.Msg{
				Question: []dns.Question{{
					Name:   "abc.com",
					Qclass: dns.ClassINET,
					Qtype:  dns.TypeA,
				}},
			}

			rec := dnstest.NewRecorder(&test.ResponseWriter{})
			_, _ = handler.ServeDNS(ctx, rec, req)

			// Ensure that the proxyCreator is called once
			if proxyCreator.called != 1 {
				t.Errorf("Expect proxy creator to be called once, but got '%d", proxyCreator.called)
			}
			// Ensure that if original is defined then Edns Record added by next
			// plugin is not sent to request
			edns0IsSet := (proxyCreator.handler.lastRequest.IsEdns0() == nil)
			if tc.original && !edns0IsSet {
				t.Errorf("Expect that if original is set then edns record is not proxied")
			}
			if !tc.original && edns0IsSet {
				t.Errorf("Expect that if original is not set then edns record added by next plugin is proxied")
			}
		})
	}
}

func TestFallbackNotCalled(t *testing.T) {
	// dummy Upstreams for servicing REFUSED
	dummyRefusedUpstream := &dummyUpstream{rcode: dns.RcodeRefused}

	handler := New(nil)

	// fallback only handle REFUSED
	handler.rules[dummyRefusedUpstream.rcode] = rule{original: false, proxyUpstream: dummyRefusedUpstream}

	proxyCreator := &testProxyCreator{t: t, expectedUpstream: nil}
	handler.proxy = proxyCreator

	ctx := context.TODO()
	req := &dns.Msg{
		Question: []dns.Question{{
			Name:   "abc.com",
			Qclass: dns.ClassINET,
			Qtype:  dns.TypeA,
		}},
	}

	rec := dnstest.NewRecorder(&test.ResponseWriter{})

	// call fallback twice, once with stub returning NXDOMAIN...
	handler.Next = stubNextHandler(dns.RcodeNameError, nil)
	_, _ = handler.ServeDNS(ctx, rec, req)
	// ....then with stub returning SERVFAIL
	handler.Next = stubNextHandler(dns.RcodeServerFailure, nil)
	_, _ = handler.ServeDNS(ctx, rec, req)

	// The proxyCreator should never be called
	if proxyCreator.called != 0 {
		t.Errorf("Expect proxy creator to not be called, but got '%d'", proxyCreator.called)
	}
}

func TestFallbackCalledMany(t *testing.T) {
	// dummy Upstreams for servicing REFUSED
	dummyRefusedUpstream := &dummyUpstream{rcode: dns.RcodeRefused}

	handler := New(nil)
	handler.Next = stubNextHandler(dns.RcodeRefused, nil)
	// fallback only handle REFUSED
	handler.rules[dummyRefusedUpstream.rcode] = rule{original: false, proxyUpstream: dummyRefusedUpstream}
	proxyCreator := &testProxyCreator{t: t, expectedUpstream: dummyRefusedUpstream}
	handler.proxy = proxyCreator

	ctx := context.TODO()
	req := &dns.Msg{
		Question: []dns.Question{{
			Name:   "abc.com",
			Qclass: dns.ClassINET,
			Qtype:  dns.TypeA,
		}},
	}

	rec := dnstest.NewRecorder(&test.ResponseWriter{})
	_, _ = handler.ServeDNS(ctx, rec, req)
	_, _ = handler.ServeDNS(ctx, rec, req)

	// The proxyCreator should be called twice
	if proxyCreator.called != 2 {
		t.Errorf("Expect proxy creator to be called twice, but got '%d'", proxyCreator.called)
	}
}
