package job

import (
	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgkern"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// axKernelInboundTotals sums, per inbound, the client traffic a kernel-mode
// AmneziaWG interface carried itself: nothing else moves that inbound's counters (3AX-UI).
type axKernelInboundTotals struct {
	tags   map[int]string
	totals map[string]*xray.Traffic
}

func newAxKernelInboundTotals(kernel []amneziawgkern.Desired) *axKernelInboundTotals {
	t := &axKernelInboundTotals{tags: map[int]string{}, totals: map[string]*xray.Traffic{}}
	for _, d := range kernel {
		// Xray meters a routed inbound through its TPROXY listener, under the same tag.
		if !d.RouteThroughXray && d.Instance.Tag != "" {
			t.tags[d.Instance.Id] = d.Instance.Tag
		}
	}
	return t
}

func (t *axKernelInboundTotals) add(inboundID int, up, down int64) {
	tag, ok := t.tags[inboundID]
	if !ok {
		return
	}
	total := t.totals[tag]
	if total == nil {
		total = &xray.Traffic{IsInbound: true, Tag: tag}
		t.totals[tag] = total
	}
	total.Up += up
	total.Down += down
}

func (t *axKernelInboundTotals) traffics() []*xray.Traffic {
	out := make([]*xray.Traffic, 0, len(t.totals))
	for _, total := range t.totals {
		out = append(out, total)
	}
	return out
}
