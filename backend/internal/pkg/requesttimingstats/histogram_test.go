package requesttimingstats

import (
	"math/rand/v2"
	"testing"
)

func TestContinuousQuantilesContainedAfterMergeAndRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(17, 31))
	for _, metric := range []Metric{Upload, Rate, Body, Real} {
		for _, n := range []int{1, 2, 3, 30, 501, 5000} {
			h, other := New(metric), New(metric)
			values := make([]float64, n)
			for i := range values {
				values[i] = rng.Float64() * 1e8
				if i%7 == 0 {
					values[i] = 0
				}
				if metric != Rate {
					values[i] = float64(int64(values[i]))
				}
				if i%2 == 0 {
					_ = h.Add(values[i])
				} else {
					_ = other.Add(values[i])
				}
			}
			if err := h.Merge(other); err != nil {
				t.Fatal(err)
			}
			encoded, err := h.Encode()
			if err != nil {
				t.Fatal(err)
			}
			h, err = Decode(encoded, metric)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range []float64{0, .1, .5, .9, .95, 1} {
				_, bounds := h.Quantile(p)
				exact := *Exact(values, p)
				if bounds == nil || exact < bounds.Lo-1e-6 || (bounds.Hi != nil && exact > *bounds.Hi+1e-6) {
					t.Fatalf("metric=%d n=%d p=%g exact=%g bounds=%+v", metric, n, p, exact, bounds)
				}
			}
		}
	}
}

func TestSparseFarRanksZeroUnderflowAndOverflow(t *testing.T) {
	h := New(Upload)
	_ = h.Add(0)
	_ = h.Add(1000000)
	_, b := h.Quantile(.5)
	if b.Lo > 500000 || b.Hi == nil || *b.Hi < 500000 {
		t.Fatal(b)
	}
	r := New(Rate)
	_ = r.Add(0.000001)
	_, b = r.Quantile(.5)
	if b.Lo != 0 || b.Hi == nil || *b.Hi != .001 {
		t.Fatal(b)
	}
	_ = r.Add(1e15)
	value, b := r.Quantile(.95)
	if value != nil || !b.Overflow || b.Hi != nil {
		t.Fatal(b)
	}
}

func TestDecoderRejectsCorruptionAndSchemaCapacity(t *testing.T) {
	for metric, edges := range metricEdges {
		if len(edges)+2 > 512 {
			t.Fatalf("metric %d has too many buckets: %d", metric, len(edges)+2)
		}
	}
	for _, data := range [][]byte{nil, {2, 1, 0}, {1, 1, 255}, {1, 1, 1, 0, 0}, {1, 1, 0, 1}} {
		if _, err := Decode(data, Upload); err == nil {
			t.Fatalf("accepted corrupt %v", data)
		}
	}
}
