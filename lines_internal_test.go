package netsuite

import (
	"math/rand"
	"testing"

	"github.com/invopop/gobl/num"
	"github.com/invopop/gobl/org"
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

// TestPerQuantityPrice checks lines priced for their quantity come to their
// amount as GOBL calculates them (bill/line_calculate.go, currency
// rounding), with whole and fractional quantities.
func TestPerQuantityPrice(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		qty := num.MakeAmount(int64(r.Intn(99999)+1), 5)
		if i%3 == 0 {
			qty = num.MakeAmount(int64(r.Intn(500)+1), 0)
		}
		amount := num.MakeAmount(int64(r.Intn(10000000)+1), 2)
		price := perQuantityPrice(amount, qty)
		it := &org.Item{Price: &price, Per: &qty}
		sum := it.PerUnit(price.RescaleUp(2).Multiply(qty)).Rescale(2)
		if !assert.True(t, sum.Equals(amount), "%s for %s came to %s", amount, qty, sum) {
			return
		}
	}
}
