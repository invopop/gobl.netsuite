package netsuite

import (
	"testing"

	"github.com/invopop/gobl/num"
	"github.com/stretchr/testify/assert"
)

func TestLineAmountMatches(t *testing.T) {
	a := num.MakeAmount
	assert.True(t, lineAmountMatches(a(20, 0), a(150, 0), a(300000, 2)))
	assert.True(t, lineAmountMatches(a(3, 0), a(333333, 4), a(10000, 2)), "rounded as NetSuite does")
	assert.True(t, lineAmountMatches(a(15, 1), a(1999, 2), a(2999, 2)), "1.5 × 19.99 = 29.985, rounded up")
	assert.False(t, lineAmountMatches(a(3, 0), a(3333, 2), a(10000, 2)), "the rate kept with more decimals")
	assert.False(t, lineAmountMatches(a(2, 0), a(10, 0), a(2500, 2)), "an amount typed in")
}
