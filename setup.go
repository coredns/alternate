package alternate

import (
	"fmt"
	"strings"

	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"

	"github.com/coredns/caddy"
	"github.com/miekg/dns"
)

func init() {
	caddy.RegisterPlugin("alternate", caddy.Plugin{
		ServerType: "dns",
		Action:     setup,
	})
}

func setup(c *caddy.Controller) error {
	a := New()

	for c.Next() {
		// shift cursor past alternate
		if !c.Next() {
			return c.ArgErr()
		}

		var (
			original bool
			rcodes   []int
			err      error
		)

		if original, err = getOriginal(c); err != nil {
			return err
		}

		if rcodes, err = getRCodes(c); err != nil {
			return err
		}

		for _, rcode := range rcodes {
			if _, ok := a.rules[rcode]; ok {
				return fmt.Errorf("rcode '%s' is specified more than once", dns.RcodeToString[rcode])
			}
		}

		handler, err := initForward(c)
		if err != nil {
			return plugin.Error("alternate", err)
		}
		a.handlers = append(a.handlers, handler)

		for _, rcode := range rcodes {
			a.rules[rcode] = rule{original: original, handler: handler}
		}
		if original {
			a.original = true
		}
	}

	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		a.Next = next
		return a
	})

	c.OnShutdown(func() error {
		for _, handler := range a.handlers {
			if err := handler.OnShutdown(); err != nil {
				return err
			}
		}
		return nil
	})

	return nil
}

const original = "original"

func getOriginal(c *caddy.Controller) (bool, error) {
	if c.Val() == original {
		// shift cursor past original
		if !c.Next() {
			return false, c.ArgErr()
		}
		return true, nil
	}

	return false, nil
}

func getRCodes(c *caddy.Controller) ([]int, error) {
	in := strings.Split(c.Val(), ",")

	rcodes := make(map[int]interface{}, len(in))

	for _, rcode := range in {
		var rc int
		var ok bool

		if rc, ok = dns.StringToRcode[strings.ToUpper(rcode)]; !ok {
			return nil, fmt.Errorf("%s is not a valid rcode", rcode)
		}

		rcodes[rc] = nil
	}

	results := make([]int, 0, len(rcodes))
	for r := range rcodes {
		results = append(results, r)
	}

	return results, nil
}
