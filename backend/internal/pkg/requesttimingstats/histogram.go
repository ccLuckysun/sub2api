// Package requesttimingstats implements versioned, mergeable timing distributions.
// Bounds use the same two order-statistic ranks as PostgreSQL percentile_cont.
package requesttimingstats

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
)

const Version byte = 1
const MaxEncodedBytes = 64 << 10

type Metric byte

const (
	Upload Metric = iota + 1
	Rate
	Body
	Real
)

type Bounds struct {
	Lo       float64  `json:"lo"`
	Hi       *float64 `json:"hi"`
	Overflow bool     `json:"overflow"`
}

type Histogram struct {
	Metric Metric
	N      uint64
	Counts map[uint16]uint64
}

var metricEdges = map[Metric][]float64{
	Upload: makeEdges(1, float64(math.MaxInt32)+1, true),
	Real:   makeEdges(1, float64(math.MaxInt32)+1, true),
	Body:   makeEdges(1, math.Exp2(63), true),
	Rate:   makeEdges(.001, 1e12, false),
}

func makeEdges(minimum, maximum float64, integer bool) []float64 {
	edges := []float64{minimum}
	for value := minimum * 1.10; value < maximum; value *= 1.10 {
		next := value
		if integer {
			next = math.Ceil(value)
		}
		if next > edges[len(edges)-1] && next < maximum {
			edges = append(edges, next)
		}
	}
	return append(edges, maximum)
}

func New(metric Metric) *Histogram {
	return &Histogram{Metric: metric, Counts: make(map[uint16]uint64)}
}

func (h *Histogram) Add(value float64) error {
	edges, ok := metricEdges[h.Metric]
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return errors.New("invalid histogram sample")
	}
	if h.N == math.MaxInt64 {
		return errors.New("histogram sample count overflow")
	}
	var bucket uint16
	if value > 0 {
		bucket = uint16(sort.Search(len(edges), func(i int) bool { return edges[i] > value }) + 1)
	}
	if h.Counts == nil {
		h.Counts = make(map[uint16]uint64)
	}
	h.Counts[bucket]++
	h.N++
	return nil
}

func (h *Histogram) Merge(other *Histogram) error {
	if other == nil || other.Metric != h.Metric || other.N > math.MaxInt64-h.N {
		return errors.New("incompatible histogram")
	}
	if h.Counts == nil {
		h.Counts = make(map[uint16]uint64)
	}
	for bucket, count := range other.Counts {
		h.Counts[bucket] += count
	}
	h.N += other.N
	return nil
}

func (h *Histogram) Encode() ([]byte, error) {
	if _, ok := metricEdges[h.Metric]; !ok {
		return nil, errors.New("invalid histogram metric")
	}
	keys := make([]int, 0, len(h.Counts))
	for bucket, count := range h.Counts {
		if count > 0 {
			keys = append(keys, int(bucket))
		}
	}
	sort.Ints(keys)
	data := []byte{Version, byte(h.Metric)}
	data = binary.AppendUvarint(data, uint64(len(keys)))
	previous := 0
	for _, bucket := range keys {
		data = binary.AppendUvarint(data, uint64(bucket-previous))
		data = binary.AppendUvarint(data, h.Counts[uint16(bucket)])
		previous = bucket
	}
	if len(data) > MaxEncodedBytes {
		return nil, errors.New("histogram encoding exceeds budget")
	}
	return data, nil
}

func Decode(data []byte, metric Metric) (*Histogram, error) {
	if len(data) < 3 || len(data) > MaxEncodedBytes || data[0] != Version || data[1] != byte(metric) {
		return nil, errors.New("invalid histogram version or metric")
	}
	edges, ok := metricEdges[metric]
	if !ok {
		return nil, errors.New("invalid histogram metric")
	}
	data = data[2:]
	read := func() (uint64, error) {
		value, n := binary.Uvarint(data)
		if n <= 0 {
			return 0, errors.New("invalid histogram varint")
		}
		data = data[n:]
		return value, nil
	}
	count, err := read()
	if err != nil || count > 512 {
		return nil, errors.New("invalid histogram bucket count")
	}
	h := New(metric)
	var bucket uint64
	for i := uint64(0); i < count; i++ {
		delta, err := read()
		if err != nil || (i > 0 && delta == 0) {
			return nil, errors.New("invalid histogram bucket delta")
		}
		if delta > uint64(len(edges)+1)-bucket {
			return nil, errors.New("histogram bucket outside schema")
		}
		bucket += delta
		n, err := read()
		if err != nil || n == 0 || n > math.MaxInt64-h.N {
			return nil, errors.New("invalid histogram sample count")
		}
		h.Counts[uint16(bucket)] = n
		h.N += n
	}
	if len(data) != 0 {
		return nil, errors.New("trailing histogram bytes")
	}
	return h, nil
}

func (h *Histogram) orderBounds(rank uint64) Bounds {
	edges := metricEdges[h.Metric]
	var seen uint64
	for bucket := 0; bucket <= len(edges)+1; bucket++ {
		seen += h.Counts[uint16(bucket)]
		if seen <= rank {
			continue
		}
		if bucket == 0 {
			zero := 0.0
			return Bounds{Hi: &zero}
		}
		lower := 0.0
		if bucket > 1 {
			lower = edges[bucket-2]
		}
		if bucket == len(edges)+1 {
			return Bounds{Lo: lower, Overflow: true}
		}
		upper := edges[bucket-1]
		return Bounds{Lo: lower, Hi: &upper}
	}
	return Bounds{Overflow: true}
}

func (h *Histogram) Quantile(p float64) (*float64, *Bounds) {
	if h.N == 0 || p < 0 || p > 1 || math.IsNaN(p) {
		return nil, nil
	}
	rank := float64(h.N-1) * p
	lower, upper := h.orderBounds(uint64(math.Floor(rank))), h.orderBounds(uint64(math.Ceil(rank)))
	alpha := rank - math.Floor(rank)
	bounds := &Bounds{Lo: (1-alpha)*lower.Lo + alpha*upper.Lo, Overflow: lower.Overflow || upper.Overflow}
	if bounds.Overflow || lower.Hi == nil || upper.Hi == nil {
		return nil, bounds
	}
	hi := (1-alpha)**lower.Hi + alpha**upper.Hi
	bounds.Hi = &hi
	value := (bounds.Lo + hi) / 2
	return &value, bounds
}

// Exact implements PostgreSQL percentile_cont, mutating the input by sorting.
func Exact(values []float64, p float64) *float64 {
	if len(values) == 0 || p < 0 || p > 1 {
		return nil
	}
	sort.Float64s(values)
	rank := float64(len(values)-1) * p
	a, b := values[int(math.Floor(rank))], values[int(math.Ceil(rank))]
	value := a + (b-a)*(rank-math.Floor(rank))
	return &value
}

func SchemaChecksum() string {
	// Changing any boundary requires a new version, not a different checksum.
	return fmt.Sprintf("timing-v%d-ratio1.10-msi32-bytesi63-rate.001-1e12", Version)
}
