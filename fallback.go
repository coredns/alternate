// Package fallback implements a fallback plugin for CoreDNS
package fallback

import (
	"golang.org/x/net/context"

	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/pkg/nonwriter"
	"github.com/coredns/coredns/plugin/proxy"

	"fmt"

	"github.com/miekg/dns"
)

// Fallback plugin allows an alternate set of upstreams be specified which will be used
// if the plugin chain returns specific error messages.
type Fallback struct {
	Next     plugin.Handler
	trace    plugin.Handler
	rules    map[int]rule
	original bool // At least one rule has "original" flag
	proxy    proxyCreator
}

type rule struct {
	original      bool
	proxyUpstream proxy.Upstream
}

// proxyCreator creates a proxy with the specified upstream.
type proxyCreator interface {
	New(trace plugin.Handler, upstream proxy.Upstream) plugin.Handler
}

// fallbackProxyCreator implements the proxyCreator interface
// Used by the fallback plugin to create proxy using specified for upstream.
type fallbackProxyCreator struct{}

func (f fallbackProxyCreator) New(trace plugin.Handler, upstream proxy.Upstream) plugin.Handler {
	return &proxy.Proxy{Trace: trace, Upstreams: &[]proxy.Upstream{upstream}}
}

func New(trace plugin.Handler) (f *Fallback) {
	return &Fallback{trace: trace, rules: make(map[int]rule), proxy: fallbackProxyCreator{}}
}

// ServeDNS implements the plugin.Handler interface.
func (f Fallback) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	// If fallback has original option set for any code then copy original request to use it instead of changed
	var originalRequest *dns.Msg
	if f.original {
		originalRequest = r.Copy()
	}
	nw := nonwriter.New(w)
	rcode, err := plugin.NextOrFailure(f.Name(), f.Next, ctx, nw, r)

	//By default the rules_index is equal rcode, so in such way we handle the case
	//when rcode is SERVFAIL and nw.Msg is nil, otherwise we use nw.Msg.Rcode
	//because, for example, for the following cases like NXDOMAIN, REFUSED the rcode is 0 (returned by proxy)
	//A proxy doesn't return 0 only in case SERVFAIL
	rules_index := rcode
	if nw.Msg != nil {
		rules_index = nw.Msg.Rcode
	}

	if u, ok := f.rules[rules_index]; ok {
		p := f.proxy.New(f.trace, u.proxyUpstream)
		if p == nil {
			return dns.RcodeServerFailure, fmt.Errorf("cannot create fallback proxy")
		}
		if u.original && originalRequest != nil {
			return p.ServeDNS(ctx, w, originalRequest)
		}
		return p.ServeDNS(ctx, w, r)
	}
	if nw.Msg != nil {
		w.WriteMsg(nw.Msg)
	}
	return rcode, err
}

// Name implements the Handler interface.
func (f Fallback) Name() string { return "fallback" }
