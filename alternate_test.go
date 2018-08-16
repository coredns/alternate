package alternate

import (
	"fmt"
	"testing"

	"golang.org/x/net/context"

	"github.com/coredns/coredns/plugin/forward"
	"github.com/coredns/coredns/plugin/pkg/dnstest"
	"github.com/coredns/coredns/plugin/test"

	"github.com/miekg/dns"
)

// NewForward Creates server and forward plugin which forwards requests to server.
// Server provides response set by rcode. Server and forward must be disabled
// by method Close() after use.
func NewDummyForward(rcode int) (*dnstest.Server, *forward.Forward) {
	s := dnstest.NewServer(func(w dns.ResponseWriter, r *dns.Msg) {
		ret := new(dns.Msg)
		ret.SetReply(r)
		ret.Answer = append(ret.Answer, test.A("example.org. IN A 127.0.0.1"))
		ret.Rcode = rcode
		w.WriteMsg(ret)
	})

	p := forward.NewProxy(s.Addr, forward.DNS)
	f := forward.New()
	f.SetProxy(p)
	return s, f
}

// stubNextHandler is used to simulate a rewrite and forward plugin.
// It returns a stub Handler that returns the rcode and err specified when invoked.
// Also it adds edns0 option to given request.
func stubNextHandler(rcode int, err error) test.Handler {
	return test.HandlerFunc(func(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
		returnCode := rcode
		if r == nil {
			r = &dns.Msg{}
		}
		r.SetEdns0(4096, false)
		if rcode != dns.RcodeServerFailure {
			r.MsgHdr.Rcode = rcode
			returnCode = dns.RcodeSuccess
			w.WriteMsg(r)
		} else {
			w.WriteMsg(nil)
		}
		return returnCode, err
	})
}

// Test case for fallback
type fallbackTestCase struct {
	nextRcode     int // rcode to be returned by the stub Handler
	expectedRcode int // this is expected rcode by test forward plugin
}

func TestAlternate(t *testing.T) {
	// dummy Upstreams for servicing a specific rcode
	dummyRefusedServer, dummyRefusedForward := NewDummyForward(dns.RcodeRefused)
	defer dummyRefusedServer.Close()
	defer dummyRefusedForward.Close()
	testRules := map[int]rule{
		dns.RcodeNXRrset:       {forward: dummyRefusedForward},
		dns.RcodeServerFailure: {forward: dummyRefusedForward},
	}

	testCases := []fallbackTestCase{
		{
			nextRcode:     dns.RcodeNXRrset,
			expectedRcode: dns.RcodeRefused,
		},
		{
			nextRcode:     dns.RcodeServerFailure,
			expectedRcode: dns.RcodeRefused,
		},
		{
			//No such code in table. Remains as is
			nextRcode:     dns.RcodeBadName,
			expectedRcode: dns.RcodeBadName,
		},
		{
			//No such code in table. Remains as is
			nextRcode:     dns.RcodeRefused,
			expectedRcode: dns.RcodeRefused,
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("rcode = %v, expected rcode = %v", dns.RcodeToString[tc.nextRcode], dns.RcodeToString[tc.expectedRcode]), func(t *testing.T) {
			handler := New(nil)
			// One of rules has "original" flag set
			handler.original = true
			// create stub handler to return the test rcode
			handler.Next = stubNextHandler(tc.nextRcode, nil)
			// add rules
			handler.rules = testRules

			ctx := context.TODO()
			req := &dns.Msg{
				Question: []dns.Question{{
					Name:   "abc.com.",
					Qclass: dns.ClassINET,
					Qtype:  dns.TypeA,
				}},
			}

			rec := dnstest.NewRecorder(&test.ResponseWriter{})
			rcode, err := handler.ServeDNS(ctx, rec, req)

			// Ensure that no errors returned
			if rcode != dns.RcodeSuccess || err != nil {
				t.Errorf("Alternate returned code '%d' error '%v'. Expected RcodeSuccess (0) and no error",
					rcode, err)
			}

			// Ensure that returned code is correct
			if rec.Rcode != tc.expectedRcode {
				t.Errorf("Alternate returned code '%v (%d)', but expected '%v (%d)'",
					dns.RcodeToString[rec.Rcode], rec.Rcode, dns.RcodeToString[tc.expectedRcode], tc.expectedRcode)
			}
		})
	}
}
