package discovery

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGroup(t *testing.T) {

	d := EventNotif{}
	tbl := []struct {
		inp string
		out string
	}{
		{
			inp: "docker.umputun.com:5500/radio-t/webstats:latest",
			out: "radio-t",
		},
		{
			inp: "docker.umputun.com/some/webstats",
			out: "some",
		},
		{
			inp: "docker.umputun.com/some/blah/webstats",
			out: "some",
		},
		{
			inp: "docker.umputun.com/webstats:xxx",
			out: "",
		},
	}

	for _, tt := range tbl {
		assert.Equal(t, tt.out, d.group(tt.inp))
	}
}
